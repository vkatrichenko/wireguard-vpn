package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// webhookStatusResponse is the JSON shape returned by all three /api/webhook
// endpoints. It carries ONLY the masked (scheme+host) URL — never the secret
// path/token — so the configured webhook is never exposed in a response body.
// The field names match the spec-008 contract consumed by the Slice-4 htmx card.
type webhookStatusResponse struct {
	Enabled        bool   `json:"enabled"`
	CurrentMasked  string `json:"current_masked"`
	OverrideActive bool   `json:"override_active"`
}

// webhookStatus builds the response view from the holder. A nil holder (webhook
// wiring absent, e.g. tests) yields the all-disabled view rather than panicking,
// so the status endpoint always renders.
func (s *server) webhookStatus() webhookStatusResponse {
	if s.webhookCfg == nil {
		return webhookStatusResponse{}
	}
	st := s.webhookCfg.Status()
	return webhookStatusResponse{
		Enabled:        st.Enabled,
		CurrentMasked:  st.MaskedURL,
		OverrideActive: st.OverrideActive,
	}
}

// writeWebhookStatus marshals and writes the current status as JSON 200. Shared
// by the GET handler and the success paths of set/revert so all three return an
// identical shape.
func (s *server) writeWebhookStatus(w http.ResponseWriter) {
	body, err := json.Marshal(s.webhookStatus())
	if err != nil {
		// All fields are stdlib-marshallable, so this is a logic bug, not an
		// operator-reachable path.
		slog.Error("/api/webhook: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleGetWebhook serves GET /api/webhook — the current effective webhook state
// as {enabled, current_masked, override_active}. The URL is reported MASKED only
// (scheme+host); the secret path/token is never written to the body. A nil
// holder reports the disabled view, never a 500.
func (s *server) handleGetWebhook(w http.ResponseWriter, _ *http.Request) {
	s.writeWebhookStatus(w)
}

// handleSetWebhook serves POST /api/webhook — install a runtime override for the
// alert webhook URL. The URL is read from a form field `url` (htmx posts form
// data in Slice 4) or, when the request is application/json, from a {"url":"..."}
// body. It must be a well-formed https:// URL with a non-empty host; an http://,
// malformed, or empty value is rejected 400 and the holder is left UNCHANGED. On
// success the holder is updated and the new (masked) status is returned 200. A
// nil holder responds 503 (the management surface is not wired). The accepted URL
// is a secret, so neither it nor any rejected value is logged in full.
func (s *server) handleSetWebhook(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	if s.webhookCfg == nil {
		http.Error(w, "webhook management unavailable", http.StatusServiceUnavailable)
		return
	}

	raw, err := readWebhookURL(r)
	if err != nil {
		if htmx {
			s.renderWebhookCard(w, "Could not read webhook URL.", "error")
			return
		}
		writeWebhookError(w, "could not read webhook url")
		return
	}

	if verr := validateWebhookURL(raw); verr != "" {
		if htmx {
			// htmx does not swap a 4xx response by default, so the HTMX path
			// renders the card at 200 with the validation reason shown inline
			// rather than a 400 the operator would never see. The holder is
			// still left UNCHANGED — validation ran before any Set.
			s.renderWebhookCard(w, verr, "error")
			return
		}
		writeWebhookError(w, verr)
		return
	}

	s.webhookCfg.Set(raw)
	if htmx {
		s.renderWebhookCard(w, "Saved.", "success")
		return
	}
	s.writeWebhookStatus(w)
}

// handleRevertWebhook serves POST /api/webhook/revert — drop any runtime override
// and restore the effective URL to the boot seed (which may itself be empty =
// disabled). Returns the resulting (masked) status 200. A nil holder responds 503.
func (s *server) handleRevertWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookCfg == nil {
		http.Error(w, "webhook management unavailable", http.StatusServiceUnavailable)
		return
	}
	s.webhookCfg.Revert()
	if isHTMX(r) {
		s.renderWebhookCard(w, "Reverted to deploy value.", "info")
		return
	}
	s.writeWebhookStatus(w)
}

// testWebhookMessage is the canned alert sent by POST /api/webhook/test. It is
// clearly labelled as a test, carries no secret and no host detail, and is safe
// to deliver to whatever the holder currently points at.
const testWebhookMessage = "✅ Test alert from wireguard-dashboard"

// webhookTestResponse is the JSON shape returned by POST /api/webhook/test.
// Reason is omitted on success and is always a FIXED, generic string on failure
// — it never echoes the (secret) webhook URL or any host detail, so a test send
// cannot leak the target via the body.
type webhookTestResponse struct {
	Delivered bool   `json:"delivered"`
	Reason    string `json:"reason,omitempty"`
}

// handleTestWebhook serves POST /api/webhook/test — deliver a canned test alert
// to the currently-effective webhook (override or seed) via the same notifier
// machinery the poller uses, and report whether it landed. A nil holder/notifier
// responds 503 (management surface not wired). When no URL is configured it
// returns 200 {delivered:false, reason:"no webhook configured"} — a clear
// non-delivery, NOT a 500, and with NO HTTP attempted (the notifier no-ops on an
// empty URL, and we short-circuit on !Enabled() so the intent is explicit). On a
// delivery error it returns 200 {delivered:false, reason:"delivery failed"} — a
// fixed reason so the secret URL never reaches the body even though the notifier
// already redacts. Success is 200 {delivered:true}.
func (s *server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	if s.webhookCfg == nil || s.webhookNotifier == nil {
		http.Error(w, "webhook management unavailable", http.StatusServiceUnavailable)
		return
	}

	if !s.webhookCfg.Enabled() {
		if htmx {
			s.renderWebhookCard(w, "No webhook configured.", "info")
			return
		}
		writeWebhookTest(w, webhookTestResponse{Delivered: false, Reason: "no webhook configured"})
		return
	}

	if err := s.webhookNotifier.Notify(r.Context(), testWebhookMessage); err != nil {
		// The notifier already redacted the URL in err and its logs; we still use
		// a FIXED reason here so no host/path can reach the response body.
		slog.Warn("/api/webhook/test: delivery failed", "err", err)
		if htmx {
			s.renderWebhookCard(w, "Delivery failed.", "error")
			return
		}
		writeWebhookTest(w, webhookTestResponse{Delivered: false, Reason: "delivery failed"})
		return
	}

	if htmx {
		s.renderWebhookCard(w, "Test alert delivered.", "success")
		return
	}
	writeWebhookTest(w, webhookTestResponse{Delivered: true})
}

// writeWebhookTest marshals a webhookTestResponse as JSON 200. The endpoint
// reports outcome (delivered / not) in the body, not via status code, so the
// htmx card can swap a result regardless of whether the test landed.
func writeWebhookTest(w http.ResponseWriter, resp webhookTestResponse) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(resp)
	_, _ = w.Write(body)
}

// readWebhookURL extracts the candidate URL from the request. A JSON content-type
// is decoded as {"url":"..."}; anything else is treated as form-encoded and read
// via PostFormValue("url"). The raw value is returned untrimmed-of-scheme so the
// validator can reject it precisely.
func readWebhookURL(r *http.Request) (string, error) {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if strings.TrimSpace(ct) == "application/json" {
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", err
		}
		return strings.TrimSpace(body.URL), nil
	}
	return strings.TrimSpace(r.PostFormValue("url")), nil
}

// validateWebhookURL returns "" when raw is a well-formed https:// URL with a
// non-empty host, else a short human-readable reason. The error string never
// echoes the candidate URL (it may be a secret).
func validateWebhookURL(raw string) string {
	if raw == "" {
		return "webhook url is required"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "webhook url is not a valid URL"
	}
	if u.Scheme != "https" {
		return "webhook url must use https"
	}
	if u.Host == "" {
		return "webhook url must include a host"
	}
	return ""
}

// isHTMX reports whether the request came from htmx, which sets HX-Request:
// "true" on every ajax it issues. The webhook write handlers content-negotiate
// on this: htmx callers get a re-rendered card fragment (HTML), plain callers
// keep the unchanged Slice 2/3 JSON contract.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// renderWebhookCard re-renders the `webhook-card` fragment from the CURRENT
// (post-mutation) masked status plus an outcome message, as 200 HTML. Used by
// the HTMX path of set/test/revert. The status is masked-only and the message
// is a fixed string, so the secret URL never reaches the body. A render error
// 500s — the fragment IS the whole response, so htmx leaves the prior card in
// place and the next tab tick retries.
func (s *server) renderWebhookCard(w http.ResponseWriter, msg, kind string) {
	data := aboutWebhookCardData{
		Status:      s.webhookStatus(),
		Message:     msg,
		MessageKind: kind,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "webhook-card", data); err != nil {
		slog.Error("/api/webhook: card render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
	}
}

// writeWebhookError writes a 400 with a JSON {"error": msg}. msg is a fixed
// reason string, never the candidate URL, so a secret can't leak via the body.
func writeWebhookError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	body, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(body)
}

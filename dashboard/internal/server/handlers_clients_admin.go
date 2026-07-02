package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"wireguard-dashboard/internal/clients"
	"wireguard-dashboard/internal/db"
)

// The runtime client-management write handlers (spec 015, Slice 6). They mirror
// the webhook write handlers' dual-path convention exactly: an HX-Request gets a
// re-rendered HTML fragment (the `clients-card` swapped via outerHTML), 200 even
// for a validation failure so htmx actually swaps the inline error; a plain
// (JSON) caller keeps the REST contract with the appropriate 4xx. A nil clients
// service responds 503, matching the webhook precedent for an unwired management
// surface.
//
// On the success path the live wg-apply already happened inside the Service
// method (Add/Update/Delete each run the applier under the write mutex), so the
// handler's only remaining job is to re-render the list from live DB state.

// handleAddClient serves POST /api/clients. Body is form-encoded for htmx or
// JSON otherwise: name, public_key, optional address (empty → auto-allocate),
// optional note.
func (s *server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	if s.clientsSvc == nil {
		http.Error(w, "client management unavailable", http.StatusServiceUnavailable)
		return
	}

	p, err := parseClientAdd(r)
	if err != nil {
		s.respondClientError(w, r, htmx, http.StatusBadRequest, "could not read request body")
		return
	}

	if _, err := s.clientsSvc.Add(r.Context(), p); err != nil {
		slog.Warn("POST /api/clients: add rejected", "err", err)
		s.respondClientError(w, r, htmx, clientErrorStatus(err), err.Error())
		return
	}

	s.respondClientSuccess(w, r, htmx, fmt.Sprintf("Added client %q.", p.Name))
}

// handleUpdateClient serves PATCH /api/clients/{name}. Only the supplied fields
// are applied (PATCH semantics): a JSON body uses absent vs. present keys; a
// form body uses present-vs-absent form fields. Editable: name, public_key,
// address, note, enabled.
func (s *server) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	if s.clientsSvc == nil {
		http.Error(w, "client management unavailable", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		s.respondClientError(w, r, htmx, http.StatusNotFound, "client not found")
		return
	}

	p, err := parseClientUpdate(r)
	if err != nil {
		s.respondClientError(w, r, htmx, http.StatusBadRequest, "could not read request body")
		return
	}

	if _, err := s.clientsSvc.Update(r.Context(), name, p); err != nil {
		slog.Warn("PATCH /api/clients: update rejected", "name", name, "err", err)
		s.respondClientError(w, r, htmx, clientErrorStatus(err), err.Error())
		return
	}

	s.respondClientSuccess(w, r, htmx, fmt.Sprintf("Updated client %q.", name))
}

// handleDeleteClient serves DELETE /api/clients/{name}. A missing name is a 404
// on the JSON path; the existence check runs before the delete so an idempotent
// no-op delete can still report not-found to a REST caller. On the htmx path a
// missing name renders the card with an inline error (200).
func (s *server) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	if s.clientsSvc == nil {
		http.Error(w, "client management unavailable", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		s.respondClientError(w, r, htmx, http.StatusNotFound, "client not found")
		return
	}

	// Service.Delete is idempotent (a missing name is a no-op) so we check
	// existence first to give a REST caller a true 404 rather than a silent 200.
	existing, err := s.clientsSvc.List(r.Context())
	if err != nil {
		slog.Error("DELETE /api/clients: list failed", "err", err)
		s.respondClientError(w, r, htmx, http.StatusInternalServerError, err.Error())
		return
	}
	if !containsClientName(existing, name) {
		s.respondClientError(w, r, htmx, http.StatusNotFound, fmt.Sprintf("no client named %q", name))
		return
	}

	if err := s.clientsSvc.Delete(r.Context(), name); err != nil {
		slog.Error("DELETE /api/clients: delete failed", "name", name, "err", err)
		s.respondClientError(w, r, htmx, http.StatusInternalServerError, err.Error())
		return
	}

	s.respondClientSuccess(w, r, htmx, fmt.Sprintf("Removed client %q.", name))
}

// respondClientSuccess re-renders the clients-card fragment (htmx, 200 HTML with
// an outcome message) or echoes the live list as JSON (plain caller). Both read
// fresh state via buildClientsTabData so the table reflects the just-applied
// mutation.
func (s *server) respondClientSuccess(w http.ResponseWriter, r *http.Request, htmx bool, msg string) {
	if htmx {
		data := s.buildClientsTabData(r.Context())
		data.Message = msg
		data.MessageKind = "success"
		s.renderClientsCard(w, data)
		return
	}
	s.writeClientsJSON(w, r)
}

// respondClientError reports a rejected mutation. The htmx path always returns
// 200 with the clients-card fragment carrying the reason inline (htmx will not
// swap a 4xx); the JSON path returns the mapped status with {"error": msg}.
func (s *server) respondClientError(w http.ResponseWriter, r *http.Request, htmx bool, status int, msg string) {
	if htmx {
		// Build the card from current (unchanged) state plus the error line. A
		// list-fetch failure inside buildClientsTabData surfaces as data.Error;
		// the inline message still renders the rejection reason.
		data := s.buildClientsTabData(r.Context())
		data.Message = msg
		data.MessageKind = "error"
		s.renderClientsCard(w, data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(body)
}

// renderClientsCard writes the `clients-card` fragment as 200 HTML. The fragment
// keeps id="clients" so the next add/edit/remove control can re-target it for an
// outerHTML swap (the webhook-card idiom). A render error 500s — the fragment is
// the whole response, so htmx leaves the prior card in place and the next tab
// tick retries.
func (s *server) renderClientsCard(w http.ResponseWriter, data clientsTabData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "clients-card", data); err != nil {
		slog.Error("clients-card render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
	}
}

// writeClientsJSON writes the live joined client list as JSON 200 — the
// REST-path success body for a mutation, identical in shape to GET /api/clients.
func (s *server) writeClientsJSON(w http.ResponseWriter, r *http.Request) {
	data := s.buildClientsTabData(r.Context())
	if data.Error != "" {
		http.Error(w, data.Error, http.StatusInternalServerError)
		return
	}
	rows := data.Rows
	if rows == nil {
		rows = []ClientRow{}
	}
	body, err := json.Marshal(rows)
	if err != nil {
		slog.Error("clients JSON marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// clientErrorStatus maps a Service error to its HTTP status for the JSON path:
// a not-found (Update on a missing name) is 404; everything else — validation,
// uniqueness conflict, out-of-subnet override, subnet exhaustion — is a 400, a
// client-correctable rejection.
func clientErrorStatus(err error) int {
	if errors.Is(err, clients.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// containsClientName reports whether name is present in the client set.
func containsClientName(cs []db.Client, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// parseClientAdd extracts the add parameters from a JSON or form-encoded body.
func parseClientAdd(r *http.Request) (clients.AddParams, error) {
	if isJSONRequest(r) {
		var body struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
			Address   string `json:"address"`
			Note      string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return clients.AddParams{}, err
		}
		return clients.AddParams{
			Name:      strings.TrimSpace(body.Name),
			PublicKey: strings.TrimSpace(body.PublicKey),
			Address:   strings.TrimSpace(body.Address),
			Note:      strings.TrimSpace(body.Note),
		}, nil
	}
	if err := r.ParseForm(); err != nil {
		return clients.AddParams{}, err
	}
	return clients.AddParams{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		PublicKey: strings.TrimSpace(r.PostFormValue("public_key")),
		Address:   strings.TrimSpace(r.PostFormValue("address")),
		Note:      strings.TrimSpace(r.PostFormValue("note")),
	}, nil
}

// parseClientUpdate extracts PATCH fields, honouring present-vs-absent so an
// unsupplied field leaves the column unchanged. JSON uses pointer presence; a
// form body uses PostForm.Has so an explicitly-blank field (e.g. note cleared)
// is distinguishable from an omitted one.
func parseClientUpdate(r *http.Request) (clients.UpdateParams, error) {
	var p clients.UpdateParams
	if isJSONRequest(r) {
		var body struct {
			Name      *string `json:"name"`
			PublicKey *string `json:"public_key"`
			Address   *string `json:"address"`
			Note      *string `json:"note"`
			Enabled   *bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return p, err
		}
		p.Name = trimPtr(body.Name)
		p.PublicKey = trimPtr(body.PublicKey)
		p.Address = trimPtr(body.Address)
		p.Note = trimPtr(body.Note)
		p.Enabled = body.Enabled
		return p, nil
	}
	if err := r.ParseForm(); err != nil {
		return p, err
	}
	if r.PostForm.Has("name") {
		v := strings.TrimSpace(r.PostForm.Get("name"))
		p.Name = &v
	}
	if r.PostForm.Has("public_key") {
		v := strings.TrimSpace(r.PostForm.Get("public_key"))
		p.PublicKey = &v
	}
	if r.PostForm.Has("address") {
		v := strings.TrimSpace(r.PostForm.Get("address"))
		p.Address = &v
	}
	if r.PostForm.Has("note") {
		v := strings.TrimSpace(r.PostForm.Get("note"))
		p.Note = &v
	}
	if r.PostForm.Has("enabled") {
		b := parseFormBool(r.PostForm.Get("enabled"))
		p.Enabled = &b
	}
	return p, nil
}

// trimPtr returns a pointer to the trimmed string when s is non-nil, preserving
// the present-vs-absent distinction the JSON PATCH path relies on.
func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

// parseFormBool accepts the htmx/form truthy spellings ("true"/"1"/"on"); any
// other value (including "false"/"0"/"off"/"") is false.
func parseFormBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "on", "yes":
		return true
	default:
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
		return false
	}
}

// isJSONRequest reports whether the request carries an application/json body,
// mirroring readWebhookURL's content-type sniff (parameters after ';' ignored).
func isJSONRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct) == "application/json"
}

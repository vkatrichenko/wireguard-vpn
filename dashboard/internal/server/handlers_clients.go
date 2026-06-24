package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/history"
	"wireguard-dashboard/internal/p95"
)

// clientDetailData is the view-model for the inline expand fragment rendered
// by `client-detail.html`. P95Bps is the 95th-percentile per-sample throughput
// (rx+tx) across the active range; 0 when fewer than two samples are present.
// Range is the active window string (one of the four-value enum 1h|6h|24h|7d)
// — hardcoded to "24h" here until Slice 9 wires the `?range=` parser through.
// Unexported and co-located here — mirrors clientsTabData's pattern in
// handlers_partial_tabs.go.
type clientDetailData struct {
	PublicKey string
	Range     string
	P95Bps    int64
	// Connection-history summary (spec 006 Slice 1), derived at request time
	// from handshake_events over the same window as the chart. Online mirrors
	// the live client-list indicator; LastSeenText is the human "last seen N
	// ago" string ("never" when the peer has no handshake in range);
	// SessionCount / ConnectedTime summarise the inferred sessions.
	Online        bool
	LastSeenText  string
	SessionCount  int
	ConnectedTime time.Duration
	// Timeline fields (spec 006 Slice 2): the derived session spans plus the
	// [From, To] window are serialised into a <script type="application/json">
	// block inside the fragment so client-timeline.js can draw the online-band
	// floating-bar chart from already-rendered data — no second round-trip, and
	// the render test can assert the bands reflect the seeded handshakes. From/To
	// bound the time x-axis so an empty/partial history still renders a correctly
	// scaled (if empty) timeline rather than an auto-fit axis on nothing.
	Sessions []history.Session
	From     time.Time
	To       time.Time
	// TimelineJSON is the marshalled timelinePayload, emitted verbatim inside a
	// <script type="application/json"> block. template.JS so html/template emits
	// it as a trusted script literal rather than HTML-escaping the JSON.
	TimelineJSON template.JS
}

// timelinePayload is the shape embedded as JSON in the detail fragment and read
// by client-timeline.js. Times are RFC3339 (the Chart.js time scale + date-fns
// adapter parse ISO strings directly). Sessions is always non-nil so the JS
// `JSON.parse(...).sessions.forEach` path is safe on an empty history.
type timelinePayload struct {
	From     time.Time         `json:"from"`
	To       time.Time         `json:"to"`
	Sessions []history.Session `json:"sessions"`
}

// clientHistoryResponse is the JSON payload of GET /api/clients/{name}/history.
// Sessions is always a non-nil slice so the encoder emits "[]" not "null" for a
// peer with no handshakes in the range. ConnectedSeconds is the summed session
// duration in whole seconds (the JSON-friendly form of history.Summary's
// ConnectedTime). Range/From/To echo the resolved window so the front-end can
// label the timeline and run the same stale-data check as the chart endpoints.
type clientHistoryResponse struct {
	Name             string            `json:"name"`
	PublicKey        string            `json:"public_key"`
	Range            string            `json:"range"`
	From             time.Time         `json:"from"`
	To               time.Time         `json:"to"`
	Online           bool              `json:"online"`
	LastSeen         time.Time         `json:"last_seen,omitzero"`
	LastSeenText     string            `json:"last_seen_text"`
	SessionCount     int               `json:"session_count"`
	ConnectedSeconds int64             `json:"connected_seconds"`
	Sessions         []history.Session `json:"sessions"`
}

// handleGetClients returns the JSON snapshot of the joined client list:
// every row is one peer combining manifest metadata (name, tunnel address)
// with live `wg show wg0 dump` state (latest handshake, byte counters,
// endpoint). The join itself lives in buildClientRows so the page-render
// path (handleIndex) can reuse it without duplicating logic.
//
// Either underlying fetch failing produces a 500 with the error in the body,
// matching the sibling /api/server and /api/service handlers. We do not fall
// back to a partial response (e.g. "live state failed, here's the manifest")
// — without the join the UI can't show status, which is the whole point of
// the endpoint.
//
// Request timeouts are inherited from r.Context(): both clientsfile.Load
// (file read, ctx reserved) and wg.Show (exec.CommandContext) honor the
// parent http.Server's deadlines.
func (s *server) handleGetClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /api/clients: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	peers, err := s.wgSvc.Show(r.Context())
	if err != nil {
		slog.Error("GET /api/clients: wg show failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows := buildClientRows(clients, peers, time.Now(), s.geoipSvc)

	// json.Marshal of a nil slice emits "null"; an empty `[]ClientRow{}`
	// emits "[]". buildClientRows always returns a non-nil slice (it
	// constructs via make([]ClientRow, 0, ...)) so the output is always
	// a valid JSON array.
	body, err := json.Marshal(rows)
	if err != nil {
		slog.Error("GET /api/clients: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleGetPartialClientDetail renders the inline expand fragment for one
// client row: a chart canvas wrapper (JS hydrates it in Slice 5 sub-task 6)
// plus the 24h p95 throughput figure. The pubkey path-param is validated
// against the clientsfile manifest — unknown / empty keys return 404 so a
// stale link or a typo doesn't render a chart against nothing.
//
// Error model:
//   - clientsfile load failure prevents the pubkey-validity check, so we
//     500 the request (we can't safely confirm or deny the key).
//   - DB query failure is logged and falls through to a P95Bps=0 render
//     rather than a 500; htmx would otherwise leave the previous swap
//     content in place, which is a worse UX than a brief "0 B/s" cell.
//
// Range is hardcoded to 24h here — the `?range=` parser lands in Slice 9.
func (s *server) handleGetPartialClientDetail(w http.ResponseWriter, r *http.Request) {
	pubkey := r.PathValue("pubkey")
	if pubkey == "" {
		http.NotFound(w, r)
		return
	}

	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /partial/clients/.../detail: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	found := false
	for _, c := range clients {
		if c.PublicKey == pubkey {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	now := time.Now()
	from := now.Add(-24 * time.Hour)

	data := clientDetailData{
		PublicKey:    pubkey,
		Range:        "24h",
		LastSeenText: "never",
		// Non-nil so the embedded JSON is "[]" not "null" even before the
		// history query runs (or if it fails) — the timeline JS iterates it.
		Sessions: []history.Session{},
		From:     from,
		To:       now,
	}

	rows, err := s.metricsDB.QueryClientTraffic(r.Context(), pubkey, from, now)
	if err != nil {
		slog.Error("GET /partial/clients/.../detail: query client_traffic failed", "err", err, "pubkey", pubkey)
		// Fall through with P95Bps = 0 — see error-model note above.
	} else {
		data.P95Bps = p95ThroughputBps(rows)
	}

	// Connection-history summary over the same 24h window. A query failure
	// degrades the same way as the P95 path above: the panel still renders
	// with the "never"/zero summary rather than 500-ing the whole fragment.
	events, herr := s.metricsDB.QueryHandshakeEventsByKey(r.Context(), pubkey, from, now)
	if herr != nil {
		slog.Error("GET /partial/clients/.../detail: query handshake_events failed", "err", herr, "pubkey", pubkey)
	} else {
		summary := history.Derive(handshakeTimes(events), now, history.SessionGapThreshold, onlineThreshold)
		data.Online = summary.Online
		data.LastSeenText = summary.LastSeenText
		data.SessionCount = summary.SessionCount
		data.ConnectedTime = summary.ConnectedTime
		data.Sessions = summary.Sessions
	}

	// Marshal the timeline payload once, server-side, and hand the fragment a
	// pre-escaped JSON string. A marshal failure degrades to an empty timeline
	// (sessions:[]) rather than 500-ing the panel — same posture as the P95 and
	// history-summary fall-throughs above.
	tlJSON, merr := json.Marshal(timelinePayload{From: data.From, To: data.To, Sessions: data.Sessions})
	if merr != nil {
		slog.Error("GET /partial/clients/.../detail: timeline marshal failed", "err", merr, "pubkey", pubkey)
		tlJSON = []byte(`{"from":null,"to":null,"sessions":[]}`)
	}
	data.TimelineJSON = template.JS(tlJSON)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "client-detail", data); err != nil {
		slog.Error("GET /partial/clients/.../detail: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// p95ThroughputBps computes the 95th-percentile per-sample throughput (in
// bytes/sec) across consecutive client_traffic rows. Throughput between two
// adjacent samples is (Δrx + Δtx) / Δt seconds. Fewer than two rows or a
// non-positive Δt yields 0 — the chart's empty/cold-start state.
//
// Percentile via internal/p95.Nearest (nearest-rank, no interpolation).
func p95ThroughputBps(rows []db.ClientTraffic) int64 {
	if len(rows) < 2 {
		return 0
	}
	rates := make([]float64, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		dt := rows[i].TS.Sub(rows[i-1].TS).Seconds()
		if dt <= 0 {
			continue
		}
		dRx := rows[i].RxBytesCum - rows[i-1].RxBytesCum
		dTx := rows[i].TxBytesCum - rows[i-1].TxBytesCum
		// Negative deltas (counter resets after a service restart) collapse
		// to zero rather than pollute the percentile with a negative outlier.
		if dRx < 0 {
			dRx = 0
		}
		if dTx < 0 {
			dTx = 0
		}
		rates = append(rates, float64(dRx+dTx)/dt)
	}
	return int64(p95.Nearest(rates))
}

// handshakeTimes projects a slice of db.HandshakeEvent down to its timestamps,
// the only field history.Derive needs. Returns a non-nil empty slice for empty
// input so Derive's "no handshakes" branch is reached cleanly.
func handshakeTimes(events []db.HandshakeEvent) []time.Time {
	out := make([]time.Time, len(events))
	for i, e := range events {
		out[i] = e.TS
	}
	return out
}

// handleGetClientHistory serves the per-client connection history JSON for the
// timeline view (spec 006 §2.1). The {name} path-param is resolved against the
// clientsfile manifest — matching the sibling /api/clients/{name}/config route
// — so the public URL stays friendly; the handshake_events query then runs on
// the resolved public key. Range is validated against the shared four-value
// enum (1h/6h/24h/7d, default 24h); any other value is a 400.
//
// Status model mirrors the other /api/clients handlers:
//   - 404 — empty or unknown name (a stale link must not render history for a
//     peer that isn't in the manifest).
//   - 400 — out-of-enum ?range=.
//   - 500 — manifest read or DB query failure (we can't produce a correct
//     answer, and unlike the inline panel there's no partial render to fall
//     back to).
//
// An empty history (no handshakes in the range) is NOT an error: the response
// carries sessions:[] with online=false and last_seen_text:"never".
func (s *server) handleGetClientHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}
	duration, ok := rangeEnumMap[rangeStr]
	if !ok {
		http.Error(w, fmt.Sprintf(rangeEnumErrMsg, rangeStr), http.StatusBadRequest)
		return
	}

	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /api/clients/{name}/history: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	client, ok := clientsfile.ByName(clients)[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	now := time.Now().UTC()
	from := now.Add(-duration)
	events, err := s.metricsDB.QueryHandshakeEventsByKey(r.Context(), client.PublicKey, from, now)
	if err != nil {
		slog.Error("GET /api/clients/{name}/history: query handshake_events failed", "err", err, "name", name)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	summary := history.Derive(handshakeTimes(events), now, history.SessionGapThreshold, onlineThreshold)

	resp := clientHistoryResponse{
		Name:             name,
		PublicKey:        client.PublicKey,
		Range:            rangeStr,
		From:             from,
		To:               now,
		Online:           summary.Online,
		LastSeen:         summary.LastSeen,
		LastSeenText:     summary.LastSeenText,
		SessionCount:     summary.SessionCount,
		ConnectedSeconds: int64(summary.ConnectedTime.Seconds()),
		Sessions:         summary.Sessions,
	}

	body, err := json.Marshal(resp)
	if err != nil {
		slog.Error("GET /api/clients/{name}/history: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

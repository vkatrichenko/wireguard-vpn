package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// fakeServerinfoSvcHistory returns a serverinfo.Service whose IMDS + wg runner
// succeed with canned values — enough to satisfy server.New and any incidental
// serverinfo fetch; this suite asserts on the history path, not server info.
func fakeServerinfoSvcHistory() *serverinfo.Service {
	return &serverinfo.Service{
		IMDS: fakeIMDS{ip: histFakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(histFakeKey + "\n"), nil
		},
	}
}

// histResp mirrors the unexported clientHistoryResponse JSON contract — defined
// locally because the test lives in the external server_test package and can't
// reference the handler's private type.
type histResp struct {
	Name             string `json:"name"`
	PublicKey        string `json:"public_key"`
	Range            string `json:"range"`
	Online           bool   `json:"online"`
	LastSeenText     string `json:"last_seen_text"`
	SessionCount     int    `json:"session_count"`
	ConnectedSeconds int64  `json:"connected_seconds"`
	Sessions         []struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"sessions"`
}

const (
	histName    = "alice"
	histAddr    = "172.16.15.5/32"
	histPubKey  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	histEndpt   = "198.51.100.42:51820"
	histFakeIP  = "203.0.113.1"
	histFakeKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
)

// newHistoryServer builds a handler wired with a one-entry manifest (histName /
// histPubKey) and the supplied DB, so each test controls exactly which
// handshake_events exist. The wg/proc/disk/etc. seams use the package's shared
// fakes — this suite only exercises the manifest+DB path.
func newHistoryServer(t *testing.T, testDB *db.DB) http.Handler {
	t.Helper()
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))
	handler, err := server.New(
		dashboard.WebFS(),
		fakeServerinfoSvcHistory(),
		&systemdSvc,
		seededClientsfileSvc(histName, histAddr, histPubKey),
		seededWgSvc(histPubKey, histEndpt, histAddr, 10*time.Second, 1, 2),
		fakeProcSvc(),
		testDB,
		nil,
		fakeDiskSvc(),
		fakeProcessesSvc(),
		fakeNetdevSvc(),
	)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler
}

// TestHandleGetClientHistory_Seeded drives the populated path: two handshakes
// inside the 24h window (a recent one → online), asserting the derived summary
// and that all four range values are accepted, an unknown name 404s, and an
// out-of-enum range 400s.
func TestHandleGetClientHistory_Seeded(t *testing.T) {
	testDB := newTestDB(t)
	now := time.Now()
	if err := testDB.InsertHandshakeEvents(context.Background(), []db.HandshakeEvent{
		{TS: now.Add(-2 * time.Minute), PublicKey: histPubKey, Name: histName},
		{TS: now.Add(-10 * time.Second), PublicKey: histPubKey, Name: histName},
	}); err != nil {
		t.Fatalf("seed handshake events: %v", err)
	}
	handler := newHistoryServer(t, testDB)

	t.Run("default range, online, one session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients/"+histName+"/history", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var got histResp
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
		}
		if got.Name != histName || got.PublicKey != histPubKey {
			t.Errorf("name/pubkey = %q/%q, want %q/%q", got.Name, got.PublicKey, histName, histPubKey)
		}
		if got.Range != "24h" {
			t.Errorf("range = %q, want 24h (default)", got.Range)
		}
		if !got.Online {
			t.Errorf("online = false, want true (last handshake 10s ago)")
		}
		// The two handshakes are 110s apart (< 10m gap) → one session.
		if got.SessionCount != 1 {
			t.Errorf("session_count = %d, want 1", got.SessionCount)
		}
		if len(got.Sessions) != 1 {
			t.Fatalf("len(sessions) = %d, want 1", len(got.Sessions))
		}
		if got.ConnectedSeconds <= 0 {
			t.Errorf("connected_seconds = %d, want > 0", got.ConnectedSeconds)
		}
	})

	t.Run("all four range values accepted", func(t *testing.T) {
		for _, rng := range []string{"1h", "6h", "24h", "7d"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients/"+histName+"/history?range="+rng, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("range %q: status = %d, want 200; body=%s", rng, rec.Code, rec.Body.String())
				continue
			}
			var got histResp
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Errorf("range %q: decode: %v", rng, err)
				continue
			}
			if got.Range != rng {
				t.Errorf("range %q: echoed range = %q", rng, got.Range)
			}
		}
	})

	t.Run("unknown name 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients/nobody/history", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid range 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients/"+histName+"/history?range=99x", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleGetClientHistory_EmptyHistory proves that a known client with no
// handshakes in the window returns 200 with an empty (but non-null) timeline,
// never an error.
func TestHandleGetClientHistory_EmptyHistory(t *testing.T) {
	handler := newHistoryServer(t, newTestDB(t)) // empty DB

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients/"+histName+"/history", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// sessions must serialise as [] not null — the encoder would emit null for
	// a nil slice, which the front-end timeline can't iterate.
	if !strings.Contains(rec.Body.String(), `"sessions":[]`) {
		t.Errorf("body missing `\"sessions\":[]` (empty non-null timeline):\n%s", rec.Body.String())
	}
	var got histResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.Online {
		t.Errorf("online = true, want false (no handshakes)")
	}
	if got.SessionCount != 0 || len(got.Sessions) != 0 {
		t.Errorf("session_count/len = %d/%d, want 0/0", got.SessionCount, len(got.Sessions))
	}
	if got.LastSeenText != "never" {
		t.Errorf("last_seen_text = %q, want %q", got.LastSeenText, "never")
	}
}

// TestHandleGetPartialClientDetail_HistorySummary proves the expand-panel
// fragment renders the connection-history summary block (status + session count
// + connected-time labels) seeded from handshake_events. The route is keyed by
// pubkey (the panel already has it); a recent handshake makes the row online.
func TestHandleGetPartialClientDetail_HistorySummary(t *testing.T) {
	testDB := newTestDB(t)
	now := time.Now()
	if err := testDB.InsertHandshakeEvents(context.Background(), []db.HandshakeEvent{
		{TS: now.Add(-3 * time.Minute), PublicKey: histPubKey, Name: histName},
		{TS: now.Add(-15 * time.Second), PublicKey: histPubKey, Name: histName},
	}); err != nil {
		t.Fatalf("seed handshake events: %v", err)
	}
	handler := newHistoryServer(t, testDB)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partial/clients/"+histPubKey+"/detail", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Fragment invariant shared with the other partial tests.
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}
	for _, want := range []string{
		`class="client-history"`,
		"Sessions (24h)",
		"Connected (24h)",
		// Last handshake 15s ago → online branch renders the status span.
		`class="status-online"`,
		"online",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandleGetPartialClientDetail_Timeline proves the Slice-2 timeline surface
// is present in the rendered fragment: the timeline canvas with its data-range
// attribute, and the embedded JSON data block whose sessions reflect the seeded
// handshakes. The two handshakes are 110s apart (< the 10m gap) → exactly one
// session band, and the band's [start,end] must straddle the seeded timestamps.
func TestHandleGetPartialClientDetail_Timeline(t *testing.T) {
	testDB := newTestDB(t)
	now := time.Now()
	hsEarly := now.Add(-3 * time.Minute)
	hsLate := now.Add(-70 * time.Second)
	if err := testDB.InsertHandshakeEvents(context.Background(), []db.HandshakeEvent{
		{TS: hsEarly, PublicKey: histPubKey, Name: histName},
		{TS: hsLate, PublicKey: histPubKey, Name: histName},
	}); err != nil {
		t.Fatalf("seed handshake events: %v", err)
	}
	handler := newHistoryServer(t, testDB)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partial/clients/"+histPubKey+"/detail", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}

	// The timeline canvas + its data-range attribute must be present.
	if !strings.Contains(body, `id="client-timeline-`+histPubKey+`"`) {
		t.Errorf("body missing timeline canvas\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, `class="client-timeline"`) {
		t.Errorf("body missing client-timeline class\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, `data-range="24h"`) {
		t.Errorf("body missing timeline data-range=\"24h\"\n--- body ---\n%s", body)
	}

	// The embedded JSON data block must be present and its decoded sessions must
	// reflect the seeded handshakes: exactly one band straddling [hsEarly, hsLate].
	marker := `<script id="client-timeline-data-` + histPubKey + `" type="application/json">`
	idx := strings.Index(body, marker)
	if idx == -1 {
		t.Fatalf("body missing timeline data <script> block\n--- body ---\n%s", body)
	}
	rest := body[idx+len(marker):]
	end := strings.Index(rest, "</script>")
	if end == -1 {
		t.Fatalf("unterminated timeline data <script> block\n--- body ---\n%s", body)
	}
	raw := strings.TrimSpace(rest[:end])

	var tl struct {
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
		Sessions []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(raw), &tl); err != nil {
		t.Fatalf("decode embedded timeline JSON: %v; raw=%q", err, raw)
	}

	if len(tl.Sessions) != 1 {
		t.Fatalf("embedded sessions = %d, want 1 (110s apart, < 10m gap)\nraw=%s", len(tl.Sessions), raw)
	}
	// The single band must span the two seeded handshakes (allow a 1s rounding
	// slop from the INTEGER unix-seconds storage at the DB boundary).
	band := tl.Sessions[0]
	if band.Start.After(hsEarly.Add(time.Second)) || band.Start.Before(hsEarly.Add(-time.Second)) {
		t.Errorf("band start = %v, want ~%v (earliest seeded handshake)", band.Start, hsEarly)
	}
	if band.End.After(hsLate.Add(time.Second)) || band.End.Before(hsLate.Add(-time.Second)) {
		t.Errorf("band end = %v, want ~%v (latest seeded handshake)", band.End, hsLate)
	}
	// The window must bound the band so the JS time axis scales correctly.
	if !tl.From.Before(band.Start) || !tl.To.After(band.End) {
		t.Errorf("window [%v,%v] does not bound band [%v,%v]", tl.From, tl.To, band.Start, band.End)
	}
}

// TestHandleGetPartialClientDetail_TimelineEmpty proves that a client with no
// handshakes in the window still renders the timeline canvas + a non-null,
// empty sessions array (so the JS draws an empty lane, never throws).
func TestHandleGetPartialClientDetail_TimelineEmpty(t *testing.T) {
	handler := newHistoryServer(t, newTestDB(t)) // empty DB

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/partial/clients/"+histPubKey+"/detail", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="client-timeline-`+histPubKey+`"`) {
		t.Errorf("body missing timeline canvas\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("body missing empty non-null sessions array\n--- body ---\n%s", body)
	}
}

package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGetPreservesSinglyEncodedPathSegment is a regression guard for the
// double-percent-encoding bug in do(): a caller (per the tool handlers in
// internal/tools) percent-encodes a dynamic path segment exactly once via
// url.PathEscape before calling Get. do() must send that segment across the
// wire byte-for-byte — not re-escape it — or a pubkey/name containing "/"
// turns "%2F" into "%252F" and 404s against the dashboard's mux.
//
// Before the fix, do() routed path through url.URL{Path: path}, whose
// u.String() re-escapes the already-escaped "%2F" into "%252F". This test
// fails against that code (decoded segment retains a literal "%2F", and
// EscapedPath() shows "%252F") and passes once do() builds the request from
// a raw URL string instead.
func TestGetPreservesSinglyEncodedPathSegment(t *testing.T) {
	const rawPubkey = "OYR4niUZ/Ay5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4=+x"

	var gotEscapedPath, gotDecodedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath = r.URL.EscapedPath()
		gotDecodedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	client := New(addr)

	// Mirrors get_client_metrics' path construction in internal/tools/tools.go:
	// a literal, slash-terminated prefix plus exactly one url.PathEscape call
	// on the dynamic segment.
	path := "/api/metrics/client/" + url.PathEscape(rawPubkey)
	q := url.Values{}
	q.Set("range", "1h")

	if _, err := client.Get(context.Background(), path, q); err != nil {
		t.Fatalf("Get: %v", err)
	}

	wantSegment := "/api/metrics/client/" + rawPubkey
	if gotDecodedPath != wantSegment {
		t.Fatalf("decoded request path = %q, want %q (a literal %%2F/%%2B surviving decode means do() double-encoded the segment)", gotDecodedPath, wantSegment)
	}
	if !strings.Contains(gotEscapedPath, "%2F") {
		t.Fatalf("EscapedPath() = %q, want it to contain a singly-encoded %%2F", gotEscapedPath)
	}
	if strings.Contains(gotEscapedPath, "%252F") {
		t.Fatalf("EscapedPath() = %q, contains %%252F: the pubkey's %%2F was re-escaped by do()", gotEscapedPath)
	}
}

// TestGetPreservesQueryAlongsideEscapedPath guards against a fix that
// preserves path-escaping but breaks the ?range= query passthrough that
// get_client_metrics/get_client_history rely on.
func TestGetPreservesQueryAlongsideEscapedPath(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	client := New(addr)

	path := "/api/clients/" + url.PathEscape("laptop/backup") + "/history"
	q := url.Values{}
	q.Set("range", "7d")

	if _, err := client.Get(context.Background(), path, q); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := gotQuery.Get("range"); got != "7d" {
		t.Fatalf("range query param = %q, want %q", got, "7d")
	}
}

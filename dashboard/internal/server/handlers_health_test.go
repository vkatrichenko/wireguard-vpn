package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clientstore"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// failingStore is a clientstore.Store whose Load always hard-fails (a
// non-404, non-empty error) — used to drive clients.Service.ReconcileFromStore
// into its storeReady=false branch via the exported API only, without
// reaching into the clients package's unexported fields from this external
// test package.
type failingStore struct{}

func (failingStore) Load(context.Context) ([]clientstore.Entry, error) {
	return nil, errors.New("clientstore: simulated hard failure")
}

func (failingStore) Save(context.Context, []clientstore.Entry) error { return nil }

// healthTestInfoSvc returns a serverinfo.Service satisfied with canned IMDS +
// wg values — enough to satisfy server.New; this suite asserts on the health
// endpoint, not server info.
func healthTestInfoSvc() *serverinfo.Service {
	return &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
}

// TestHandleHealth_LocalMode_OmitsClientStoreField proves /api/health in local
// mode renders the original byte-stable `{"ok":true}` body with no
// client_store_ready field at all — the field would be a meaningless
// always-true signal without an S3 bridge to report on.
func TestHandleHealth_LocalMode_OmitsClientStoreField(t *testing.T) {
	systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))
	handler, err := server.New(dashboard.WebFS(), healthTestInfoSvc(), &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, emptyClientsSvc(t), "local", nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if body != `{"ok":true}` {
		t.Errorf("body = %q, want the byte-stable %q (local mode must never emit client_store_ready)", body, `{"ok":true}`)
	}
	if strings.Contains(body, "client_store_ready") {
		t.Errorf("body unexpectedly contains client_store_ready in local mode: %s", body)
	}
}

// TestHandleHealth_CloudMode_IncludesClientStoreReady proves cloud mode adds
// the client_store_ready boolean, sourced live from clientsSvc.StoreReady():
// true for a freshly-constructed (default-ready) Service, false after a hard
// ReconcileFromStore load error latches it offline.
func TestHandleHealth_CloudMode_IncludesClientStoreReady(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		clientsSvc := emptyClientsSvc(t)
		systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))

		handler, err := server.New(dashboard.WebFS(), healthTestInfoSvc(), &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, clientsSvc, "cloud", nil)
		if err != nil {
			t.Fatalf("server.New: %v", err)
		}

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var got struct {
			OK               bool `json:"ok"`
			ClientStoreReady bool `json:"client_store_ready"`
		}
		if !strings.Contains(rec.Body.String(), "client_store_ready") {
			t.Fatalf("body missing client_store_ready in cloud mode: %s", rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
		}
		if !got.ClientStoreReady {
			t.Errorf("client_store_ready = false, want true for a freshly-constructed Service")
		}
	})

	t.Run("offline after hard reconcile error", func(t *testing.T) {
		clientsSvc := emptyClientsSvc(t)
		clientsSvc.SetStore(failingStore{})
		// The hard-error branch returns a non-nil error (logged loudly by
		// main.go in production) AND sets storeReady false — we only care
		// about the latter here.
		_ = clientsSvc.ReconcileFromStore(context.Background(), nil)

		systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))
		handler, err := server.New(dashboard.WebFS(), healthTestInfoSvc(), &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, clientsSvc, "cloud", nil)
		if err != nil {
			t.Fatalf("server.New: %v", err)
		}

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"client_store_ready":false`) {
			t.Errorf("body = %s, want client_store_ready:false after a hard reconcile error", rec.Body.String())
		}
	})
}

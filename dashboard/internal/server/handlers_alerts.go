package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// handleGetAlerts serves GET /api/alerts — the current in-UI alert state as
// JSON. The body mirrors alerts.Status:
//
//	{
//	  "enabled": bool,                                  // webhook configured?
//	  "active":  [ {condition, key, detail, since} ],   // currently FIRING
//	  "recent":  [ {condition, key, kind, detail, at} ] // recent transitions
//	}
//
// The data comes from the poller-written status holder via a deep-copied
// Snapshot, so this handler does NO evaluator access — the evaluator is not
// concurrency-safe and is touched only from the poller goroutine. A nil holder
// (alerting wiring absent, e.g. in tests) yields {"enabled":false,"active":[],
// "recent":[]} via alertSnapshot — never a 500. The only 500 path is a marshal
// failure, which would be a logic bug (all fields are stdlib-marshallable).
func (s *server) handleGetAlerts(w http.ResponseWriter, _ *http.Request) {
	body, err := json.Marshal(s.alertSnapshot())
	if err != nil {
		slog.Error("GET /api/alerts: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

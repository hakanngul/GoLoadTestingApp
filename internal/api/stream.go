package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
)

// handleStream serves GET /api/runs/{id}/stream as Server-Sent Events.
//
// For an active run it relays runner events (`metric` with a
// model.MetricPoint, `stats` with a model.LiveStats, `status`/`done` with
// a model.Run) until the done
// event, the channel closing or the client disconnecting, with a `: ping`
// comment every heartbeat interval. For a run that is not active it sends
// a single `done` event carrying the stored run and returns.
func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, cancel, err := s.runner.Subscribe(id)
	if err != nil {
		if !errors.Is(err, runner.ErrNotRunning) {
			s.writeErr(w, r, err)
			return
		}
		run, gerr := s.store.GetRun(r.Context(), id)
		if gerr != nil {
			s.writeErr(w, r, gerr)
			return
		}
		run = normalizeRun(run)
		startSSE(w)
		if err := writeEvent(w, runner.EventDone, run); err == nil {
			flush(w)
		}
		return
	}
	defer cancel()

	startSSE(w)
	flush(w)

	tick := time.NewTicker(s.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flush(w)
		case ev, ok := <-events:
			if !ok {
				return
			}
			var payload any
			switch {
			case ev.Metric != nil:
				payload = ev.Metric
			case ev.Stats != nil:
				payload = ev.Stats
			case ev.Run != nil:
				payload = normalizeRun(*ev.Run)
			}
			if err := writeEvent(w, ev.Type, payload); err != nil {
				s.log.LogAttrs(r.Context(), slog.LevelDebug, "sse write failed",
					slog.String("run_id", id), slog.String("error", err.Error()))
				return
			}
			flush(w)
			if ev.Type == runner.EventDone {
				return
			}
		}
	}
}

// startSSE writes the event-stream response headers and lifts any server
// write deadline, which would otherwise cut long-lived streams.
func startSSE(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
}

// writeEvent writes one SSE frame. JSON never contains raw newlines, so a
// single data line suffices.
func writeEvent(w io.Writer, typ runner.EventType, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
	return err
}

func flush(w http.ResponseWriter) {
	_ = http.NewResponseController(w).Flush()
}

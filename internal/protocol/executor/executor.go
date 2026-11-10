// Package executor builds the protocol.Executor for a scenario, choosing
// the protocol per step: HTTP steps run on protocol/httpx, WebSocket steps
// (type ws) on protocol/wsx. Scenarios may mix both; steps then run in
// scenario order within one iteration, sharing variables, cookies (the
// virtual user's protocol.Session), the coordinated
// omission rule (first step measured from `intended`), think times and
// stop-on-first-failure.
//
// Runners, workers and the CLI should construct executors through New
// rather than calling httpx.New directly, so that ws scenarios work
// everywhere.
package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/wsx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// Options configures the per-protocol executors.
type Options struct {
	// HTTP tunes the HTTP transport (MaxConnsPerHost, DisableKeepAlives).
	HTTP httpx.Options
	// WS tunes WebSocket steps.
	WS wsx.Options
}

// New returns an executor for sc and a function that releases idle
// resources (pooled connections); call it once the run is over.
//
// A scenario made only of HTTP steps gets exactly the *httpx.Executor that
// httpx.New returns, so its behaviour is unchanged; one made only of ws
// steps gets a *wsx.Executor; a mix gets a composite executor.
//
// If sc.Auth is set, that executor is wrapped by the auth layer (see
// auth.go), which logs in / caches credentials and injects the auth
// header into every HTTP step and WebSocket handshake.
func New(sc *scenario.Scenario, opts Options) (protocol.Executor, func(), error) {
	if sc == nil {
		return nil, nil, errors.New("executor: nil scenario")
	}
	if sc.Auth != nil {
		return newAuth(sc, opts)
	}
	return newPlain(sc, opts)
}

// newPlain builds the protocol executor for sc, ignoring sc.Auth.
func newPlain(sc *scenario.Scenario, opts Options) (protocol.Executor, func(), error) {
	var nHTTP, nWS int
	for i := range sc.Steps {
		if sc.Steps[i].IsWS() {
			nWS++
		} else {
			nHTTP++
		}
	}
	switch {
	case nWS == 0:
		h, err := httpx.New(sc, opts.HTTP)
		if err != nil {
			return nil, nil, err
		}
		return h, h.Close, nil
	case nHTTP == 0:
		w, err := wsx.New(sc, opts.WS)
		if err != nil {
			return nil, nil, err
		}
		return w, w.Close, nil
	}

	// Mixed: build each protocol executor from its own steps only and
	// remember where each scenario step landed.
	httpSc, wsSc := *sc, *sc
	httpSc.Steps = make([]scenario.Step, 0, nHTTP)
	wsSc.Steps = make([]scenario.Step, 0, nWS)
	m := &mixed{steps: make([]stepRef, len(sc.Steps)), cookies: sc.Target.CookiesEnabled()}
	for i := range sc.Steps {
		st := sc.Steps[i]
		ref := stepRef{ws: st.IsWS(), think: st.ThinkTime.Duration}
		if ref.ws {
			ref.idx = len(wsSc.Steps)
			wsSc.Steps = append(wsSc.Steps, st)
		} else {
			ref.idx = len(httpSc.Steps)
			httpSc.Steps = append(httpSc.Steps, st)
		}
		m.steps[i] = ref
	}
	var err error
	if m.http, err = httpx.New(&httpSc, opts.HTTP); err != nil {
		return nil, nil, fmt.Errorf("executor: %w", err)
	}
	if m.ws, err = wsx.New(&wsSc, opts.WS); err != nil {
		m.http.Close()
		return nil, nil, fmt.Errorf("executor: %w", err)
	}
	return m, m.close, nil
}

// stepRef locates one scenario step in its protocol executor.
type stepRef struct {
	ws    bool
	idx   int
	think time.Duration
}

// mixed runs scenarios that contain both HTTP and ws steps.
type mixed struct {
	http  *httpx.Executor
	ws    *wsx.Executor
	steps []stepRef
	// cookies: HTTP and ws steps of an iteration share the virtual
	// user's protocol.Session (cookie jar).
	cookies bool
}

func (m *mixed) close() {
	m.http.Close()
	m.ws.Close()
}

// Iterate implements protocol.Executor with the same semantics as
// httpx.Executor.Iterate, dispatching each step to its protocol.
func (m *mixed) Iterate(ctx context.Context, vars map[string]string, intended time.Time, sink func(protocol.Result)) {
	if m.cookies {
		ctx, _ = protocol.EnsureSession(ctx) // open model: a new visitor
	}
	for i, ref := range m.steps {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		if i == 0 && !intended.IsZero() && intended.Before(start) {
			start = intended
		}
		var extracted []string
		if ref.ws {
			var ok bool
			if extracted, ok = m.ws.Step(ctx, ref.idx, vars, start, sink); !ok {
				return
			}
		} else {
			var res protocol.Result
			res, extracted = m.http.Step(ctx, ref.idx, vars, start)
			if ctx.Err() != nil {
				return // run cancelled: not a failure of the target
			}
			sink(res)
			if res.Err != nil {
				return
			}
		}
		if len(extracted) > 0 {
			if vars == nil {
				vars = make(map[string]string, len(extracted)/2)
			}
			for j := 0; j < len(extracted); j += 2 {
				vars[extracted[j]] = extracted[j+1]
			}
		}
		if ref.think > 0 && !sleep(ctx, ref.think) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

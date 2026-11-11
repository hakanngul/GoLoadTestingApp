// Package notify delivers "run finished" and "suite finished" notifications
// to the notification channels stored in the database (Slack incoming
// webhooks and generic JSON webhooks).
//
// Delivery is asynchronous: RunFinished and SuiteFinished only enqueue the
// event and never block the caller. A small pool of workers loads the
// enabled channels, filters them (event subscription, only-failures) and
// posts with a 10 second timeout per request. Network errors and 5xx
// answers are retried twice with a growing pause; 4xx answers are not
// retried. A channel URL is a secret and is never logged or put in an
// error: errors from the HTTP client are stripped of it.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// Defaults for Options.
const (
	// DefaultWorkers is the number of delivery goroutines.
	DefaultWorkers = 4
	// DefaultQueue is how many finished runs may wait for delivery; further
	// events are dropped (and logged) rather than blocking the runner.
	DefaultQueue = 256
	// DefaultTimeout bounds one HTTP request.
	DefaultTimeout = 10 * time.Second
	// DefaultBackoff is the pause before the first retry; it doubles for
	// the second.
	DefaultBackoff = time.Second
	// maxRetries is the number of retries after the first attempt.
	maxRetries = 2
)

// Options configures a Notifier.
type Options struct {
	// Store holds the channels. Required.
	Store store.NotificationStore
	// PublicURL is the externally reachable base URL of the UI (for example
	// "https://goload.example.com"). Links are omitted when empty.
	PublicURL string
	// Client sends the requests. nil = a client with Timeout.
	Client *http.Client
	// Logger receives delivery failures (never URLs). nil = discard.
	Logger *slog.Logger
	// Workers is the number of delivery goroutines. 0 = DefaultWorkers.
	Workers int
	// Queue is the pending-event capacity. 0 = DefaultQueue.
	Queue int
	// Timeout bounds one request. 0 = DefaultTimeout.
	Timeout time.Duration
	// Backoff is the pause before the first retry (doubling). 0 =
	// DefaultBackoff.
	Backoff time.Duration
}

// Notifier sends notifications. It is safe for concurrent use.
type Notifier struct {
	st      store.NotificationStore
	base    string
	client  *http.Client
	log     *slog.Logger
	timeout time.Duration
	backoff time.Duration

	jobs chan job
	wg   sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// job is one finished run or suite run waiting for delivery.
type job struct {
	event string
	run   model.Run
	suite model.SuiteRun
}

// New starts a Notifier. Call Close to stop it.
func New(o Options) *Notifier {
	n := &Notifier{
		st:      o.Store,
		base:    strings.TrimRight(o.PublicURL, "/"),
		client:  o.Client,
		log:     o.Logger,
		timeout: o.Timeout,
		backoff: o.Backoff,
	}
	if n.timeout <= 0 {
		n.timeout = DefaultTimeout
	}
	if n.backoff <= 0 {
		n.backoff = DefaultBackoff
	}
	if n.client == nil {
		n.client = &http.Client{Timeout: n.timeout}
	}
	if n.log == nil {
		n.log = slog.New(slog.DiscardHandler)
	}
	workers, queue := o.Workers, o.Queue
	if workers <= 0 {
		workers = DefaultWorkers
	}
	if queue <= 0 {
		queue = DefaultQueue
	}
	n.jobs = make(chan job, queue)
	n.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go n.worker()
	}
	return n
}

// Close stops accepting events, delivers those already queued and waits
// for the workers, or until ctx ends.
func (n *Notifier) Close(ctx context.Context) {
	n.mu.Lock()
	if !n.closed {
		n.closed = true
		close(n.jobs)
	}
	n.mu.Unlock()
	done := make(chan struct{})
	go func() { n.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// RunFinished queues a run_finished notification for r (a run in a final
// status). It never blocks; it is the runner's OnFinish hook.
func (n *Notifier) RunFinished(r model.Run) {
	n.enqueue(job{event: model.EventRunFinished, run: r})
}

// SuiteFinished queues a suite_finished notification for r. It never
// blocks; it is the suite service's OnFinish hook.
func (n *Notifier) SuiteFinished(r model.SuiteRun) {
	n.enqueue(job{event: model.EventSuiteFinished, suite: r})
}

func (n *Notifier) enqueue(j job) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	select {
	case n.jobs <- j:
	default:
		n.log.Warn("notify: queue full, dropping event", "event", j.event)
	}
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for j := range n.jobs {
		n.handle(j)
	}
}

// handle loads the channels for j and delivers to each that matches.
func (n *Notifier) handle(j job) {
	ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
	chans, err := n.st.ListNotificationChannels(ctx)
	cancel()
	if err != nil {
		n.log.Error("notify: list channels", "error", err)
		return
	}
	for _, c := range chans {
		if !matches(c, j) {
			continue
		}
		body, err := n.payload(c, j)
		if err != nil {
			n.log.Error("notify: build payload", "channel", c.ID, "event", j.event, "error", err)
			continue
		}
		if err := n.deliver(context.Background(), c.URL, body); err != nil {
			n.log.Error("notify: delivery failed", "channel", c.ID, "event", j.event, "error", err)
		}
	}
}

// IsFailure reports whether a final run status counts as a failure for the
// only-failures filter: failed and error, not stopped.
func IsFailure(s model.RunStatus) bool {
	return s == model.RunFailed || s == model.RunError
}

// isFinal reports whether s is a final status.
func isFinal(s model.RunStatus) bool {
	switch s {
	case model.RunPassed, model.RunFailed, model.RunStopped, model.RunError:
		return true
	}
	return false
}

// matches reports whether channel c wants job j.
func matches(c model.NotificationChannel, j job) bool {
	if !c.Enabled {
		return false
	}
	subscribed := false
	for _, e := range c.Events {
		if e == j.event {
			subscribed = true
			break
		}
	}
	if !subscribed {
		return false
	}
	status := j.run.Status
	if j.event == model.EventSuiteFinished {
		status = j.suite.Status
	}
	if !isFinal(status) {
		return false
	}
	return !c.OnlyFailures || IsFailure(status)
}

// payload builds the request body for channel c.
func (n *Notifier) payload(c model.NotificationChannel, j job) ([]byte, error) {
	if c.Kind == model.NotifySlack {
		text := n.runText(j.run)
		if j.event == model.EventSuiteFinished {
			text = n.suiteText(j.suite)
		}
		return json.Marshal(map[string]string{"text": text})
	}
	if j.event == model.EventSuiteFinished {
		return json.Marshal(map[string]any{"event": j.event, "suite_run": j.suite})
	}
	run, err := runWithoutYAML(j.run)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"event": j.event, "run": run})
}

// runWithoutYAML returns r as a JSON object without its (large) scenario.
func runWithoutYAML(r model.Run) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "yaml")
	return m, nil
}

// SendTest synchronously posts a test message to c and returns the
// delivery error, if any. The error never contains the channel URL.
func (n *Notifier) SendTest(ctx context.Context, c model.NotificationChannel) error {
	var v any
	if c.Kind == model.NotifySlack {
		v = map[string]string{"text": fmt.Sprintf("GoLoad: test notification for channel \"%s\". Notifications are working.", slackEscape(c.Name))}
	} else {
		v = map[string]any{"event": "test", "message": "GoLoad test notification", "channel": c.Name}
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return n.deliver(ctx, c.URL, body)
}

// statusError is a non-2xx answer.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("endpoint answered HTTP %d", e.code) }

// deliver posts body to rawURL, retrying network errors and 5xx answers.
func (n *Notifier) deliver(ctx context.Context, rawURL string, body []byte) error {
	var err error
	pause := n.backoff
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(pause)
			select {
			case <-ctx.Done():
				t.Stop()
				return err
			case <-t.C:
			}
			pause *= 2
		}
		err = n.post(ctx, rawURL, body)
		if err == nil || !retryable(err) {
			return err
		}
	}
	return err
}

// retryable reports whether err is worth another attempt: network errors
// and 5xx answers, but not 4xx.
func retryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code >= 500
	}
	return !errors.Is(err, context.Canceled)
}

// post makes one request with the per-request timeout.
func (n *Notifier) post(ctx context.Context, rawURL string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return stripURL(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GoLoad-notifier")
	resp, err := n.client.Do(req)
	if err != nil {
		return stripURL(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{code: resp.StatusCode}
	}
	return nil
}

// stripURL removes the request URL that net/http embeds in its errors.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("request failed: %w", ue.Err)
	}
	return err
}

// MaskURL returns a display form of a secret webhook URL: scheme, host and
// the last four characters ("https://hooks.slack.com/…a1b2"). Anything
// that does not parse as an absolute URL is fully masked.
func MaskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "…"
	}
	tail := raw
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	} else {
		tail = ""
	}
	return u.Scheme + "://" + u.Host + "/…" + tail
}

// View converts a channel to its API form, with the URL masked.
func View(c model.NotificationChannel) model.NotificationView {
	events := c.Events
	if events == nil {
		events = []string{}
	}
	return model.NotificationView{
		ID: c.ID, Name: c.Name, Kind: c.Kind, URLMasked: MaskURL(c.URL), Events: events,
		OnlyFailures: c.OnlyFailures, Enabled: c.Enabled, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// Package apiclient is a small client for the GoLoad REST API (see
// internal/api/doc.go): bearer-token auth, JSON in and out, retries of
// GETs on transient errors, and name-or-ID resolution of tests and suites.
// It is shared by `goloadtest remote run` and the MCP server.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// Defaults used by New and for a zero Backoff.
const (
	DefaultTimeout = 30 * time.Second
	DefaultRetries = 3
	DefaultBackoff = 500 * time.Millisecond

	// maxResponse bounds how much of a response body is read.
	maxResponse = 64 << 20
)

// Client talks to one GoLoad server.
type Client struct {
	// BaseURL has no trailing slash, e.g. https://goload.example.com.
	BaseURL string
	// Token is sent as "Authorization: Bearer <token>" when not empty.
	Token string
	// HTTP is the underlying client; nil means a client with DefaultTimeout.
	HTTP *http.Client
	// Retries is how often a GET is retried on a transient error (0 = never).
	Retries int
	// Backoff is the base pause between retries; it grows linearly.
	// Zero means DefaultBackoff.
	Backoff time.Duration
}

// New returns a Client with the default timeout and retry policy.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), Token: token,
		HTTP:    &http.Client{Timeout: DefaultTimeout},
		Retries: DefaultRetries, Backoff: DefaultBackoff,
	}
}

// APIError is a non-2xx answer (or a transport failure, Status 0).
type APIError struct {
	Status  int
	Message string
	Details []string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// Transient reports whether retrying the same GET may help.
func (e *APIError) Transient() bool {
	switch e.Status {
	case 0, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return true
	}
	return false
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultTimeout}
}

// Do performs one JSON request: in (if not nil) is the JSON body, a 2xx
// body is decoded into out (if not nil). GETs are retried on transient
// errors. Errors are *APIError.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return &APIError{Message: err.Error()}
		}
	}
	return c.do(ctx, method, path, "application/json", body, out)
}

// DoRaw is Do with a raw request body (e.g. a CSV upload) of the given
// content type.
func (c *Client) DoRaw(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	return c.do(ctx, method, path, contentType, body, out)
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	attempts := 1
	if method == http.MethodGet {
		attempts += c.Retries
	}
	backoff := c.Backoff
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(time.Duration(i) * backoff):
			}
		}
		err = c.once(ctx, method, path, contentType, body, out)
		var ae *APIError
		if err == nil || ctx.Err() != nil || !errors.As(err, &ae) || !ae.Transient() {
			return err
		}
	}
	return err
}

func (c *Client) once(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return &APIError{Message: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		// *url.Error carries method and URL only, never headers.
		return &APIError{Message: err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return &APIError{Message: "reading response: " + err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := &APIError{Status: resp.StatusCode}
		var apiErr model.APIError
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			ae.Message, ae.Details = apiErr.Error, apiErr.Details
		} else {
			ae.Message = http.StatusText(resp.StatusCode)
			if s := strings.TrimSpace(string(data)); s != "" && len(s) < 300 {
				ae.Message += ": " + s
			}
		}
		return ae
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return &APIError{Message: "invalid JSON from server: " + err.Error()}
		}
	}
	return nil
}

// Ref is a test or suite as seen by name resolution.
type Ref struct{ ID, Name string }

// ResolveRef finds ref among items: an exact id wins, then an exact name.
// kind ("test", "suite") is only used in messages. When nothing matches,
// the error suggests similar names; when a name is ambiguous it lists the
// ids.
func ResolveRef(kind, ref string, items []Ref) (Ref, error) {
	for _, it := range items {
		if it.ID == ref {
			return it, nil
		}
	}
	var hits []Ref
	for _, it := range items {
		if it.Name == ref {
			hits = append(hits, it)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var near, all []string
		lref := strings.ToLower(ref)
		for _, it := range items {
			all = append(all, it.Name)
			ln := strings.ToLower(it.Name)
			if strings.Contains(ln, lref) || strings.Contains(lref, ln) {
				near = append(near, it.Name)
			}
		}
		msg := fmt.Sprintf("no %s named or with id %q", kind, ref)
		if len(near) == 0 {
			near = all
		}
		if len(near) > 0 {
			if len(near) > 8 {
				near = near[:8]
			}
			msg += "; did you mean: " + strings.Join(quoteAll(near), ", ")
		}
		return Ref{}, errors.New(msg)
	default:
		var ids []string
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		return Ref{}, fmt.Errorf("%d %ss are named %q; use an id instead: %s", len(hits), kind, ref, strings.Join(ids, ", "))
	}
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/draft"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type draftLoginIn struct {
	URL           string `json:"url" jsonschema:"login URL (absolute on the same host, or a path such as /api/login)"`
	Method        string `json:"method,omitempty" jsonschema:"POST (default), PUT or PATCH"`
	Format        string `json:"format,omitempty" jsonschema:"json (default) or form"`
	UsernameField string `json:"username_field,omitempty" jsonschema:"body field for the user name, default username"`
	PasswordField string `json:"password_field,omitempty" jsonschema:"body field for the password, default password"`
	TokenPath     string `json:"token_path,omitempty" jsonschema:"JSON path of the token in the login response, e.g. data.access_token; empty = the site uses a session cookie"`
}

type draftIn struct {
	URL        string        `json:"url" jsonschema:"absolute http(s) URL of the first page or API endpoint"`
	Goal       string        `json:"goal,omitempty" jsonschema:"smoke (quick check, default), load (normal day traffic), stress (find the limit) or spike (sudden burst)"`
	Users      int           `json:"users,omitempty" jsonschema:"people using the site at the same time; 0 = the goal's default"`
	Duration   string        `json:"duration,omitempty" jsonschema:"total length such as 30s, 5m; empty = the goal's default"`
	Name       string        `json:"name,omitempty" jsonschema:"test name; default derived from the URL"`
	ExtraPaths []string      `json:"extra_paths,omitempty" jsonschema:"more pages on the same host to visit after the first, e.g. /products"`
	Login      *draftLoginIn `json:"login,omitempty" jsonschema:"set when the site needs a login"`
}

type draftOut struct {
	Name     string   `json:"name"`
	YAML     string   `json:"yaml"`
	DataFile string   `json:"data_file,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Next     string   `json:"next"`
}

type linksIn struct {
	URL string `json:"url" jsonschema:"absolute http(s) URL of the page to look at"`
	Max int    `json:"max,omitempty" jsonschema:"maximum links to return (default 15, max 50)"`
}

type linksOut struct {
	URL      string   `json:"url"`
	FinalURL string   `json:"final_url,omitempty"`
	Status   int      `json:"status"`
	Links    []string `json:"links"`
	Note     string   `json:"note,omitempty"`
}

// registerDraftTools adds draft_test (pure: turns a few simple answers
// into a valid scenario YAML, saves nothing) and discover_links (sends
// one GET through the server's test-request endpoint and lists same-host
// links of the page).
func registerDraftTools(s *mcp.Server, h *handlers) {
	addTool(s, "draft_test", "Draft a load test",
		"Build a valid scenario YAML from simple answers (URL, goal, users, duration, optional login). Saves nothing: "+
			"follow with try_test, then create_test. With a login the YAML reads credentials from data_file (a CSV with header "+
			"username,password) that create_test uploads; never put passwords in the YAML.",
		kindRead, false,
		func(_ context.Context, in draftIn) (draftOut, string, error) {
			a := draft.Answers{URL: strings.TrimSpace(in.URL), Goal: draft.Goal(strings.ToLower(strings.TrimSpace(in.Goal))),
				Users: in.Users, Name: in.Name, ExtraPaths: in.ExtraPaths}
			if d := strings.TrimSpace(in.Duration); d != "" {
				dur, err := time.ParseDuration(d)
				if err != nil {
					return draftOut{}, "", fmt.Errorf("duration %q: use a value like 30s, 5m or 1h", d)
				}
				a.Duration = dur
			}
			if l := in.Login; l != nil {
				a.Login = &draft.Login{URL: l.URL, Method: l.Method, Format: l.Format,
					UsernameField: l.UsernameField, PasswordField: l.PasswordField, TokenPath: l.TokenPath}
			}
			r, err := draft.Build(a)
			if err != nil {
				return draftOut{}, "", err
			}
			next := "Run try_test with this yaml, fix what it reports, then create_test."
			if r.DataFile != "" {
				next = "Ask the user for one login (user name and password), then create_test with data_file " + r.DataFile +
					" (header username,password). Run try_test with test=<the new test> afterwards so the login is tried with that data."
			}
			return draftOut{Name: r.Name, YAML: r.YAML, DataFile: r.DataFile, Notes: r.Notes, Next: next},
				fmt.Sprintf("Drafted %q (%d notes).", r.Name, len(r.Notes)), nil
		})

	addTool(s, "discover_links", "Find pages on a site",
		"Open one page (a single GET through GoLoad's test request, subject to the server's target allowlist) and list "+
			"same-host links found in the first 8 KiB of its HTML. Use it to offer the user 2-3 important pages for extra_paths.",
		kindWrite, true,
		func(ctx context.Context, in linksIn) (linksOut, string, error) {
			u := strings.TrimSpace(in.URL)
			if u == "" {
				return linksOut{}, "", errors.New("url is required")
			}
			max := in.Max
			if max <= 0 {
				max = 15
			}
			if max > 50 {
				max = 50
			}
			r, err := draft.Build(draft.Answers{URL: u, Goal: draft.GoalSmoke, Users: 1, Duration: 10 * time.Second})
			if err != nil {
				return linksOut{}, "", err
			}
			var res model.TryResult
			if err := h.client.Do(ctx, http.MethodPost, "/api/tests/try", model.TryRequest{YAML: r.YAML}, &res); err != nil {
				return linksOut{}, "", h.apiErr("opening the page", err)
			}
			out := linksOut{URL: u, Links: []string{}}
			if len(res.Steps) == 0 || res.Steps[0].State == model.TryNotRun {
				out.Note = "the page was not requested"
				for _, hn := range res.Hints {
					out.Note += "; " + hn.Message
				}
				return out, "Page not opened: " + out.Note, nil
			}
			st := res.Steps[0]
			out.Status, out.FinalURL = st.Status, st.FinalURL
			base := u
			if st.FinalURL != "" {
				base = st.FinalURL
			}
			if st.BodyBinary || st.Body == "" {
				out.Note = "the page returned no HTML to read"
			} else {
				out.Links = draft.Links(base, st.Body, max)
				if st.BodyTruncated {
					out.Note = "only the first 8 KiB of the page were read"
				}
			}
			if out.Links == nil {
				out.Links = []string{}
			}
			return out, fmt.Sprintf("Found %d links on %s (HTTP %d).", len(out.Links), u, out.Status), nil
		})
}

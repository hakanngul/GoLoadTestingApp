package scenario

import (
	"fmt"
	"strings"
)

// Auth defaults applied by ApplyDefaults (see the Auth doc comment).
const (
	// DefaultAuthHeader is the header injected when Auth.Header is empty.
	DefaultAuthHeader = "Authorization"
	// DefaultAuthValue is the header value template for login and bearer
	// auth. For bearer, {{token}} stands for Auth.Token (see
	// Auth.ValueTemplate); for login it is the extracted variable "token".
	DefaultAuthValue = "Bearer {{token}}"
	// DefaultLoginMethod is the method of Auth.Login when it sets none.
	// Unlike ordinary steps (GET), logins almost always POST credentials.
	DefaultLoginMethod = "POST"
	// AuthLoginStep is the step name under which login requests are
	// reported. Scenario steps must not use it while auth is configured.
	AuthLoginStep = "auth:login"
)

// InjectEnabled reports whether the auth header is added to every step
// (Inject nil or true).
func (a *Auth) InjectEnabled() bool { return a.Inject == nil || *a.Inject }

// ValueTemplate returns the header value template that is rendered with
// the iteration variables. For bearer auth every {{token}} in Value is
// first replaced by Auth.Token (which may itself contain {{var}}s, e.g. a
// CSV column). For basic auth it returns "" because the value is computed
// ("Basic " + base64(user:pass)).
func (a *Auth) ValueTemplate() string {
	switch a.Type {
	case AuthBasic:
		return ""
	case AuthBearer:
		v := a.Value
		if v == "" {
			v = DefaultAuthValue
		}
		return Render(v, map[string]string{"token": a.Token})
	}
	if a.Value == "" {
		return DefaultAuthValue
	}
	return a.Value
}

// applyAuthDefaults fills Auth defaults. Fields that do not apply to the
// type are left alone so Validate can report them.
func (a *Auth) applyDefaults() {
	a.Type = AuthType(strings.ToLower(strings.TrimSpace(string(a.Type))))
	if a.Header == "" {
		a.Header = DefaultAuthHeader
	}
	switch a.Type {
	case AuthLogin:
		if a.Scope == "" {
			a.Scope = AuthPerUser
		}
		if a.Value == "" {
			a.Value = DefaultAuthValue
		}
		if l := a.Login; l != nil {
			if l.Type != "" {
				l.Type = StepType(strings.ToLower(strings.TrimSpace(string(l.Type))))
			}
			l.Method = strings.ToUpper(strings.TrimSpace(l.Method))
			if l.Method == "" && !l.IsWS() {
				l.Method = DefaultLoginMethod
			}
			if l.Name == "" {
				l.Name = AuthLoginStep
			}
			for j := range l.Assert {
				if l.Assert[j].Op == "" {
					l.Assert[j].Op = DefaultAssertOp
				}
			}
		}
	case AuthBearer:
		if a.Value == "" {
			a.Value = DefaultAuthValue
		}
	}
}

// validateAuth reports problems in the auth block as *FieldErrors with
// paths under "auth".
func (s *Scenario) validateAuth() []error {
	a := s.Auth
	var errs []error
	add := func(path, format string, args ...any) {
		errs = append(errs, &FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
	}
	notFor := func(field string, set bool) {
		if set {
			add("auth."+field, "not allowed with type %s", a.Type)
		}
	}

	switch a.Type {
	case AuthLogin, AuthBearer, AuthBasic:
	default:
		add("auth.type", "must be one of login, bearer, basic; got %q", a.Type)
		return errs
	}

	if h := a.Header; strings.TrimSpace(h) == "" {
		add("auth.header", "must not be empty")
	} else if strings.ContainsAny(h, " \t\r\n:") {
		add("auth.header", "invalid header name %q", h)
	} else if strings.EqualFold(h, "Host") {
		add("auth.header", "cannot be Host")
	}

	if a.Type != AuthLogin {
		notFor("scope", a.Scope != "")
		notFor("refresh", a.Refresh.Duration != 0)
		notFor("login", a.Login != nil)
	}
	if a.Type != AuthBearer {
		notFor("token", a.Token != "")
	}
	if a.Type != AuthBasic {
		notFor("username", a.Username != "")
		notFor("password", a.Password != "")
	}

	switch a.Type {
	case AuthLogin:
		switch a.Scope {
		case AuthPerUser, AuthShared:
		default:
			add("auth.scope", "must be per_user or shared; got %q", a.Scope)
		}
		if a.Refresh.Duration < 0 {
			add("auth.refresh", "must not be negative")
		}
		l := a.Login
		if l == nil {
			add("auth.login", "required for type login (an HTTP step that extracts the token)")
			break
		}
		if l.Type != "" && l.Type != StepHTTP {
			add("auth.login.type", "must be http; got %q", l.Type)
			break
		}
		if l.ThinkTime.Duration != 0 {
			add("auth.login.think_time", "not supported on the login step")
		}
		errs = append(errs, validateStep("auth.login", l)...)
		if len(l.Extract) == 0 {
			add("auth.login.extract", "at least one extract is required (e.g. {var: token, from: json, expr: access_token})")
		} else if a.Value == DefaultAuthValue && a.InjectEnabled() {
			found := false
			for _, ex := range l.Extract {
				if strings.TrimSpace(ex.Var) == "token" {
					found = true
					break
				}
			}
			if !found {
				add("auth.login.extract", "the default auth.value %q needs an extract with var: token (or set auth.value to use another variable)", DefaultAuthValue)
			}
		}
	case AuthBearer:
		if strings.TrimSpace(a.Token) == "" {
			add("auth.token", "required for type bearer")
		}
	case AuthBasic:
		if a.Username == "" {
			add("auth.username", "required for type basic")
		}
		if a.Password == "" {
			add("auth.password", "required for type basic")
		}
		if a.Value != "" {
			add("auth.value", "not allowed with type basic (the value is \"Basic \" + base64(username:password))")
		}
	}

	for i := range s.Steps {
		if s.Steps[i].Name == AuthLoginStep {
			add(fmt.Sprintf("steps[%d].name", i), "%q is reserved for login requests when auth is configured", AuthLoginStep)
		}
	}
	return errs
}

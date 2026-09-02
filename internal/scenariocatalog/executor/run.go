package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/contracts/api/v2/scenarios"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// Result is the outcome of one scenario.
type Result struct {
	Catalog  string
	Row      string
	Scenario string
	Skipped  string
	Failures []string
}

// Passed reports a scenario that ran and met every assertion.
func (r Result) Passed() bool { return r.Skipped == "" && len(r.Failures) == 0 }

// RunAll executes every scenario in every catalog as subtests, returning the
// results for reporting. Database-gated scenarios skip when no database is
// configured.
func RunAll(t *testing.T, catalogs []*scenariocatalog.Catalog) []Result {
	t.Helper()
	env := New(t)
	var results []Result
	for _, c := range catalogs {
		c := c
		t.Run(strings.TrimSuffix(c.File, ".json"), func(t *testing.T) {
			for _, row := range c.Rows {
				row := row
				t.Run(row.Method+" "+row.Path+" #"+strconv.Itoa(row.RegistrationIndex), func(t *testing.T) {
					// Rows start from the same synthetic state so a mutation in
					// one row cannot change what another row observes.
					if env.HasDatabase() && env.rowNeedsDatabase(row) {
						env.Reseed()
					}
					for _, s := range row.Scenarios {
						s := s
						t.Run(s.ID, func(t *testing.T) {
							env.Run(t, c, row, s, func(r Result) { results = append(results, r) })
						})
					}
				})
			}
		})
	}
	return results
}

// Run executes one scenario. It skips (via t.Skip) when the scenario needs a
// database and none is configured, and fails t on assertion failures. record
// receives the result before any Skip/Fatal unwinds the subtest.
func (e *Env) Run(t *testing.T, c *scenariocatalog.Catalog, row scenariocatalog.Row, s scenariocatalog.Scenario, record func(Result)) {
	t.Helper()
	res := Result{Catalog: c.File, Row: row.Key().String(), Scenario: s.ID}
	defer func() { record(res) }()
	needsDB := s.NeedsDatabase()
	dbUnavailable := s.HasRequirement("database_unavailable")
	// A scenario on the rate-limited registration variant (#1 rows, which
	// the router only registers with a rate-limit middleware) runs on the
	// limited router whatever it asserts, so the row's real middleware
	// chain is the one exercised. requires: rate_limiter forces the same.
	rateLimited := s.HasRequirement("rate_limiter") || e.rowRateLimited(row)
	if rateLimited {
		needsDB = true
	}
	if !dbUnavailable && !e.OfflineHas(row.Method, row.Path) {
		// The row is registered only with a user store / auth middleware
		// present, so even its public cases need the live router.
		needsDB = true
	}
	if needsDB && !e.HasDatabase() {
		res.Skipped = DatabaseEnv + " is not set"
		t.Skip(res.Skipped)
		return
	}
	server, err := e.server(needsDB, dbUnavailable, rateLimited)
	if err != nil {
		res.Skipped = err.Error()
		t.Skip(res.Skipped)
		return
	}
	// A scenario that mutates the household runs on a fresh fixture set and
	// leaves a fresh one behind, so neither its predecessor nor its successor
	// observes the mutation.
	if s.FreshState && e.HasDatabase() {
		e.Reseed()
		defer e.Reseed()
	}
	if len(s.Settings) > 0 {
		resolved := make(map[string]string, len(s.Settings))
		for k, v := range s.Settings {
			sv, err := e.substitute(v)
			if err != nil {
				res.Failures = []string{"settings: " + err.Error()}
				t.Fatal(res.Failures[0])
				return
			}
			resolved[k] = sv
		}
		restore := e.applySettings(resolved)
		defer restore()
	}
	if rateLimited {
		e.resetRateLimits()
	}

	failures, fatal := e.exchange(server.URL, s.Method(), s.Request, s.Principal, s.Expect)
	if fatal != nil {
		res.Failures = []string{fatal.Error()}
		t.Fatal(res.Failures[0])
		return
	}
	res.Failures = failures
	// Follow-up exchanges observe the primary request's effect in the same
	// state. They run even when the primary failed so the report shows the
	// whole picture, but stop at the first step that cannot be built.
	for i, step := range s.Then {
		principal := s.Principal
		if step.Principal != nil {
			principal = *step.Principal
		}
		stepFailures, fatal := e.exchange(server.URL, step.Method, step.Request, principal, step.Expect)
		if fatal != nil {
			res.Failures = append(res.Failures, fmt.Sprintf("then[%d]: %v", i, fatal))
			t.Fatal(res.Failures[len(res.Failures)-1])
			return
		}
		for _, f := range stepFailures {
			label := fmt.Sprintf("then[%d]", i)
			if step.Description != "" {
				label += " (" + step.Description + ")"
			}
			res.Failures = append(res.Failures, label+": "+f)
		}
	}
	if len(res.Failures) > 0 {
		t.Errorf("%s\n  %s", s.Description, strings.Join(res.Failures, "\n  "))
	}
}

// exchange sends one request (repeated when the request says so), checks
// the final response, and returns the assertion failures with the response
// summary attached. A non-nil error means the exchange could not be built
// or sent at all.
func (e *Env) exchange(base, method string, request scenariocatalog.Request, principal scenariocatalog.Principal, expect scenariocatalog.Expect) ([]string, error) {
	repeat := request.Repeat
	if repeat < 1 {
		repeat = 1
	}
	var resp response
	for i := 0; i < repeat; i++ {
		req, err := e.buildRequest(base, method, request, principal)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		resp, err = send(req)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}
	}
	exp, err := e.substituteExpect(expect)
	if err != nil {
		return nil, fmt.Errorf("expected values: %w", err)
	}
	failures := check(exp, resp)
	if len(failures) > 0 {
		body := string(resp.Raw)
		if len(body) > 600 {
			body = body[:600] + "..."
		}
		failures = append(failures, fmt.Sprintf("response: %d %s", resp.Status, body))
	}
	return failures, nil
}

// rowRateLimited reports whether the ledger registers the row only behind
// the rate-limit middleware, in which case its scenarios must run on the
// limited router or they never reach that registration at all.
func (e *Env) rowRateLimited(row scenariocatalog.Row) bool {
	entry, ok := e.ledger[row.Key()]
	return ok && entry.RateLimited()
}

// applySettings writes server_settings rows for a scenario and returns the
// function that puts the previous values back (or deletes rows that did not
// exist), so one scenario's settings never leak into the next.
func (e *Env) applySettings(settings map[string]string) func() {
	prior := map[string]*string{}
	for k, v := range settings {
		var existing string
		err := e.pool.QueryRow(e.ctx, `SELECT value FROM server_settings WHERE key = $1`, k).Scan(&existing)
		if err == nil {
			prior[k] = &existing
		} else {
			prior[k] = nil
		}
		e.mustSetting(k, v)
	}
	return func() {
		for k, v := range prior {
			if v == nil {
				e.mustExec(`DELETE FROM server_settings WHERE key = $1`, k)
				continue
			}
			e.mustExec(`UPDATE server_settings SET value = $2 WHERE key = $1`, k, *v)
		}
		if e.limiter != nil {
			_ = e.limiter.Reload(e.ctx)
		}
	}
}

// substituteExpect resolves ${...} placeholders inside expected values so a
// catalog can compare against minted fixture identities.
func (e *Env) substituteExpect(exp scenariocatalog.Expect) (scenariocatalog.Expect, error) {
	out := exp
	out.Body = make([]scenariocatalog.BodyAssertion, len(exp.Body))
	for i, a := range exp.Body {
		out.Body[i] = a
		if len(a.Value) > 0 {
			sub, err := e.substitute(string(a.Value))
			if err != nil {
				return out, err
			}
			out.Body[i].Value = json.RawMessage(sub)
		}
	}
	out.Headers = make([]scenariocatalog.HeaderAssertion, len(exp.Headers))
	for i, h := range exp.Headers {
		sub, err := e.substitute(h.Value)
		if err != nil {
			return out, err
		}
		out.Headers[i] = h
		out.Headers[i].Value = sub
	}
	return out, nil
}

func (e *Env) rowNeedsDatabase(row scenariocatalog.Row) bool {
	if e.rowRateLimited(row) {
		return true
	}
	for _, s := range row.Scenarios {
		if s.NeedsDatabase() || s.HasRequirement("rate_limiter") {
			return true
		}
	}
	return false
}

func (e *Env) resetRateLimits() {
	if e.limiter == nil {
		return
	}
	if err := e.limiter.Reload(e.ctx); err != nil {
		e.t.Fatalf("scenario executor: reset rate limits: %v", err)
	}
}

var (
	placeholderRE = regexp.MustCompile(`\$\{([a-z_]+)\}`)
	// "${name:int}" (with the surrounding quotes) substitutes as a bare JSON
	// number so a catalog can compare integer ids against minted fixtures.
	intPlaceholderRE = regexp.MustCompile(`"\$\{([a-z_]+):int\}"`)
)

// placeholders are the ${...} values a catalog may reference. Every value is
// minted from the synthetic fixture set at run time; nothing is committed.
func (e *Env) placeholder(name string) (string, bool) {
	switch name {
	case "admin_token":
		return e.accessToken(fixtureAdmin), true
	case "member_token":
		return e.accessToken(fixtureMember), true
	case "grouped_token":
		return e.accessToken(fixtureGrouped), true
	case "disabled_token":
		return e.accessToken(fixtureDisabled), true
	case "member_refresh_token":
		return e.refreshToken(fixtureMember), true
	case "member_revoked_refresh_token":
		return e.refreshTokenFor(fixtureMember, e.fixtures["member_revoked_session_id"]), true
	case "device_code_approved":
		return deviceCodeApp, true
	case "browser_code_approved":
		return browserCodeAp, true
	case "device_code_remote_approved":
		return deviceCodeRA, true
	case "browser_code_remote_approved":
		return browserCodeRA, true
	case "user_code_remote":
		return userCodeRem, true
	case "member_access_as_refresh":
		return e.accessToken(fixtureMember), true
	case "impersonation_token":
		return e.impersonationToken(), true
	case "admin_api_key":
		return e.apiKeys[""], true
	case "admin_password":
		return adminPassword, true
	case "member_password":
		return memberPass, true
	case "disabled_password":
		return disabledPass, true
	case "member_username":
		return memberUser, true
	case "admin_username":
		return adminUsername, true
	case "disabled_username":
		return disabledUser, true
	case "member_email":
		return memberEmail, true
	case "invite_code":
		return inviteCode, true
	case "invite_code_disabled":
		return inviteCodeOff, true
	case "invite_code_exhausted":
		return inviteFull, true
	case "invitation_token":
		return inviteToken, true
	case "invitation_token_accepted":
		return inviteTokenUs, true
	case "device_code":
		return deviceCode, true
	case "browser_code":
		return browserCode, true
	case "user_code":
		return userCode, true
	case "device_code_remote":
		return deviceCodeRem, true
	case "browser_code_remote":
		return browserCodeRm, true
	case "device_code_denied":
		return deviceCodeDen, true
	case "browser_code_denied":
		return browserCodeDn, true
	case "device_code_expired":
		return deviceCodeExp, true
	case "browser_code_expired":
		return browserCodeEx, true
	case "locked_pin":
		return lockedPIN, true
	case "device_a":
		return deviceIDA, true
	case "device_b":
		return deviceIDB, true
	case "profile_admin_primary":
		return profileAdminPrimary, true
	case "profile_admin_secondary":
		return profileAdminSecondary, true
	case "profile_primary":
		return profilePrimary, true
	case "profile_secondary":
		return profileSecondary, true
	case "profile_child":
		return profileChild, true
	case "profile_locked":
		return profileLocked, true
	case "profile_grouped_primary":
		return profileGroupedPrimary, true
	case "profile_missing":
		return profileMissing, true
	case "server_name":
		return serverName, true
	case "public_url":
		return publicURL, true
	}
	v, ok := e.fixtures[name]
	return v, ok
}

func (e *Env) substitute(s string) (string, error) {
	var missing []string
	s = intPlaceholderRE.ReplaceAllStringFunc(s, func(m string) string {
		name := intPlaceholderRE.FindStringSubmatch(m)[1]
		v, ok := e.placeholder(name)
		if !ok {
			missing = append(missing, name)
			return m
		}
		if _, err := strconv.Atoi(v); err != nil {
			missing = append(missing, name+" (not an integer)")
			return m
		}
		return v
	})
	out := placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		v, ok := e.placeholder(name)
		if !ok {
			missing = append(missing, name)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unknown placeholder(s) %v", missing)
	}
	return out, nil
}

// principalHeaders returns the credential headers for the scenario's
// principal class. Catalogs may override or remove any of them.
func (e *Env) principalHeaders(p scenariocatalog.Principal) (http.Header, error) {
	h := http.Header{}
	h.Set("User-Agent", "silo-scenario-executor/1")
	user := p.User
	profile := p.Profile
	defaults, ok := principalDefaults[p.Class]
	defaultUser, defaultProfile := defaults[0], defaults[1]
	switch {
	case p.Class == "public":
		return h, nil
	case p.Class == "api_key":
		key := e.apiKeys[strings.Join(p.Scopes, ",")]
		if key == "" {
			return nil, fmt.Errorf("no fixture api key for scopes %v", p.Scopes)
		}
		h.Set("Authorization", "Bearer "+key)
		return h, nil
	case !ok:
		return nil, fmt.Errorf("unknown principal class %q", p.Class)
	}
	if user == "" {
		user = defaultUser
	}
	if profile == "" {
		profile = defaultProfile
	}
	// The disabled account's seeded session validates like any other; the
	// middleware does not consult users.enabled for JWTs. Handlers decide.
	h.Set("Authorization", "Bearer "+e.accessToken(user))
	if profile != "" && profile != profileNone {
		id, ok := e.placeholder("profile_" + profile)
		if !ok {
			return nil, fmt.Errorf("unknown fixture profile %q", profile)
		}
		h.Set("X-Profile-Id", id)
		if p.Verified {
			h.Set("X-Profile-Token", e.fixtures["locked_profile_token"])
		}
	}
	return h, nil
}

func (e *Env) buildRequest(base, method string, r scenariocatalog.Request, principal scenariocatalog.Principal) (*http.Request, error) {
	path, err := e.substitute(r.Path)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, err
	}
	if len(r.Query) > 0 {
		q := u.Query()
		for k, v := range r.Query {
			sv, err := e.substitute(v)
			if err != nil {
				return nil, err
			}
			q.Set(k, sv)
		}
		u.RawQuery = q.Encode()
	}

	var body io.Reader
	contentType := ""
	switch {
	case r.Multipart != nil:
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		for k, v := range r.Multipart.Fields {
			if err := mw.WriteField(k, v); err != nil {
				return nil, err
			}
		}
		for _, f := range r.Multipart.Files {
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, f.Field, f.Filename))
			header.Set("Content-Type", f.ContentType)
			part, err := mw.CreatePart(header)
			if err != nil {
				return nil, err
			}
			data, err := syntheticFile(f.Content)
			if err != nil {
				return nil, err
			}
			if _, err := part.Write(data); err != nil {
				return nil, err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
		body = buf
		contentType = mw.FormDataContentType()
	case r.RawBody != nil:
		raw, err := e.substitute(*r.RawBody)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(raw)
		contentType = contentTypeJSON
	case r.BodyRef != "":
		raw, err := scenarios.FS.ReadFile("fixtures/" + r.BodyRef)
		if err != nil {
			return nil, fmt.Errorf("body_ref %s: %w", r.BodyRef, err)
		}
		sub, err := e.substitute(string(raw))
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(sub)
		contentType = contentTypeJSON
	case r.Body != nil:
		sub, err := e.substitute(string(r.Body))
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(sub)
		contentType = contentTypeJSON
	}
	if r.ContentType != "" {
		contentType = r.ContentType
	}

	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}
	headers, err := e.principalHeaders(principal)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range r.Headers {
		if v == nil {
			req.Header.Del(k)
			continue
		}
		sv, err := e.substitute(*v)
		if err != nil {
			return nil, err
		}
		req.Header.Set(k, sv)
	}
	return req, nil
}

// principalDefaults maps a principal class to the fixture account and
// declared profile it uses unless the scenario overrides them. The demo class
// is an ordinary member so the demo guard applies; catalogs pair it with the
// demo.enabled setting.
var principalDefaults = map[string][2]string{
	"authenticated":   {fixtureMember, ""},
	"profile":         {fixtureMember, "secondary"},
	"child_profile":   {fixtureMember, "child"},
	"primary_profile": {fixtureMember, "primary"},
	"admin":           {fixtureAdmin, ""},
	"acting_admin":    {fixtureAdmin, "admin_primary"},
	"access_group":    {fixtureGrouped, "grouped_primary"},
	"demo":            {fixtureMember, ""},
}

const contentTypeJSON = "application/json"

// profileNone is the catalog spelling for "send no X-Profile-Id".
const profileNone = "none"

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func send(req *http.Request) (response, error) {
	httpResp, err := noRedirect.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return response{}, err
	}
	resp := response{Status: httpResp.StatusCode, Headers: httpResp.Header, Raw: raw}
	if len(raw) > 0 && json.Valid(raw) {
		var doc any
		if err := json.Unmarshal(raw, &doc); err == nil {
			resp.Doc = doc
			resp.IsJSON = true
		}
	}
	if !resp.IsJSON {
		resp.Doc = string(raw)
	}
	return resp, nil
}

// syntheticFile renders a synthetic file part: png:WxH, text:..., bytes:N.
func syntheticFile(spec string) ([]byte, error) {
	switch {
	case strings.HasPrefix(spec, "png:"):
		dims := strings.SplitN(strings.TrimPrefix(spec, "png:"), "x", 2)
		w, _ := strconv.Atoi(dims[0])
		h, _ := strconv.Atoi(dims[1])
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{R: 64, G: 128, B: 192, A: 255})
			}
		}
		buf := &bytes.Buffer{}
		if err := png.Encode(buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case strings.HasPrefix(spec, "text:"):
		return []byte(strings.TrimPrefix(spec, "text:")), nil
	case strings.HasPrefix(spec, "bytes:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(spec, "bytes:"))
		return make([]byte, n), nil
	}
	return nil, fmt.Errorf("unknown synthetic file spec %q", spec)
}

// WriteReport renders results as JSON for CI artifacts when
// SILO_SCENARIO_REPORT names a file.
func WriteReport(results []Result) error {
	path := os.Getenv("SILO_SCENARIO_REPORT")
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

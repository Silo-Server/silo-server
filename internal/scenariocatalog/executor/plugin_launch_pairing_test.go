package executor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestRequiredPluginLaunchAcceptance runs the eight frozen plugin launch
// cases on both transports: four issuances and four refusals. Every case
// asserts the launch response itself, so no plugin process is served. The
// runner additionally proves the v2 cookie carries the same plugin access
// claims as the v1 cookie for the same principal and differs only in path.
func TestRequiredPluginLaunchAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-plugin-launch for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.PluginLaunchAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, effects, cookies := 0, 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				var v1Cookie *http.Cookie
				for _, transport := range []string{"v1", "v2"} {
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						e.Reseed()
						defer e.Reseed()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							var raw []byte
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s),'sessions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM auth_sessions a),'device_requests',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM device_login_requests d),'invitations',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitations i),'invite_codes',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM invite_codes c))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							if len(rows) != 8 {
								t.Fatal("snapshot must contain eight full tables")
							}
							return rows
						}
						dbNow := func() time.Time {
							t.Helper()
							var now time.Time
							if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
								t.Fatal(err)
							}
							return now
						}
						before := snapshot()
						dbLower := dbNow()
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
						}
						requests++
						response, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						cookie := pluginAccessCookie(response.Headers)
						if expect.Status != http.StatusOK {
							if cookie != nil {
								t.Error("a refusal must not issue a plugin access cookie")
							}
						} else {
							cookies++
							if transport == "v1" {
								v1Cookie = cookie
							} else {
								e.checkPluginLaunchCookies(t, v1Cookie, cookie)
							}
						}
						dbUpper := dbNow()
						after := snapshot()
						for id, want := range before {
							if id == "keys" && principal.Class == "api_key" {
								// Both transports admit the key at the auth gate before the
								// handler refuses it, and admission records last-used
								// asynchronously. Only that column of the used key may differ,
								// and only inside the database request window.
								checkKeysAllowingLastUsed(t, want, after[id], dbLower, dbUpper)
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Errorf("stored %s rows changed during plugin launch", id)
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredPluginLaunchScenarios); err != nil {
		t.Error(err)
	}
	if requests != 16 || effects != 32 || cookies != 8 {
		t.Errorf("paired plugin launch evidence %dHTTP/%dPG/%d cookies, want 16/32/8", requests, effects, cookies)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func pluginAccessCookie(h http.Header) *http.Cookie {
	rec := http.Response{Header: h}
	for _, c := range rec.Cookies() {
		if c.Name == auth.PluginAccessCookieName {
			return c
		}
	}
	return nil
}

// checkPluginLaunchCookies proves the v2 cookie is the v1 credential on the
// narrow v2 plugin-content path: same name, lifetime, HttpOnly and SameSite,
// same token type, user, session, role and profile in the signed claims, and
// a path that is exactly the plugin-content parent, never / or /api/v1.
func (e *Env) checkPluginLaunchCookies(t *testing.T, v1, v2 *http.Cookie) {
	t.Helper()
	if v1 == nil || v2 == nil {
		t.Fatal("both transports must issue the plugin access cookie")
	}
	if v1.Path != "/api/v1" || v2.Path != plugins.ContentPrefix || v2.Path == "/" {
		t.Errorf("cookie paths v1=%q v2=%q", v1.Path, v2.Path)
	}
	if v1.MaxAge != v2.MaxAge || v1.HttpOnly != v2.HttpOnly || v1.SameSite != v2.SameSite || v1.Secure != v2.Secure {
		t.Errorf("cookie attributes differ: v1=%#v v2=%#v", v1, v2)
	}
	a, err := e.jwt.ValidateToken(v1.Value)
	if err != nil {
		t.Fatalf("v1 cookie token: %v", err)
	}
	b, err := e.jwt.ValidateToken(v2.Value)
	if err != nil {
		t.Fatalf("v2 cookie token: %v", err)
	}
	if a.TokenType != auth.TokenTypePluginAccess || b.TokenType != a.TokenType || a.UserID != b.UserID || a.Role != b.Role || a.ProfileID != b.ProfileID || a.SessionID == "" || b.SessionID == "" {
		t.Errorf("plugin access claims differ: v1=%#v v2=%#v", a, b)
	}
}

// checkKeysAllowingLastUsed compares the api_keys table before and after a
// key-admitted exchange: every column of every row stays byte-identical
// except last_used_at on at most one row, whose new stamp must fall inside
// the database request window. An unchanged table (the asynchronous commit
// landed outside the snapshot window) is accepted as well.
func checkKeysAllowingLastUsed(t *testing.T, before, after json.RawMessage, lower, upper time.Time) {
	t.Helper()
	var old, cur []map[string]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &cur) != nil || len(old) != len(cur) {
		t.Fatal("api_keys snapshot shape changed")
	}
	touched := 0
	for i := range old {
		if len(old[i]) != len(cur[i]) {
			t.Fatal("api_keys column count changed")
		}
		for k, v := range old[i] {
			if bytes.Equal(v, cur[i][k]) {
				continue
			}
			if k != "last_used_at" {
				t.Errorf("unexpected api_keys column change: %s", k)
				continue
			}
			touched++
			var stamp time.Time
			if err := json.Unmarshal(cur[i][k], &stamp); err != nil {
				t.Fatal(err)
			}
			if stamp.Before(lower) || stamp.After(upper) {
				t.Error("last_used_at outside the database request window")
			}
		}
	}
	if touched > 1 {
		t.Errorf("%d keys recorded usage, want at most the one presented", touched)
	}
}

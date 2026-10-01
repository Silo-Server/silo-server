package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestRequiredOAuthRefusalAcceptance runs the OAuth handshake refusals and the
// disabled-account read through the real router on both transports. No
// authentication plugin is installed or launched: every case is decided by
// install_id parsing, state verification, body validation, or completion-code
// lookup, and each proves it wrote no oauth_sessions, oauth_completions, or
// login-session row.
func TestRequiredOAuthRefusalAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run oauth refusal acceptance explicitly")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.OAuthRefusalAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	// The migrations seed one built-in metadata installation (silo.builtin);
	// no real plugin row, no auth-provider capability, and no installation 42
	// may exist, so every handshake resolves to "auth plugin unavailable".
	var pluginRows, authCapabilities, install42 int
	if err := e.pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM plugin_installations WHERE kind <> 'builtin'),(SELECT count(*) FROM plugin_capabilities WHERE capability_type LIKE 'auth%'),(SELECT count(*) FROM plugin_installations WHERE id = 42)`).Scan(&pluginRows, &authCapabilities, &install42); err != nil {
		t.Fatal(err)
	}
	if pluginRows != 0 || authCapabilities != 0 || install42 != 0 {
		t.Fatalf("oauth refusal acceptance requires no installed auth plugin: plugin rows %d, auth capabilities %d, installation 42 rows %d", pluginRows, authCapabilities, install42)
	}
	var results []Result
	requests, effects := 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s),'sessions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM auth_sessions a),'oauth_sessions',(SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY o.state),'[]'::jsonb) FROM oauth_sessions o),'oauth_completions',(SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY o.code_hash),'[]'::jsonb) FROM oauth_completions o),'plugin_installations',(SELECT COALESCE(jsonb_agg(to_jsonb(i) ORDER BY i.id),'[]'::jsonb) FROM plugin_installations i))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						before := snapshot()
						if len(before) != 8 {
							t.Fatal("snapshot must contain eight full tables")
						}
						for _, table := range []string{"oauth_sessions", "oauth_completions"} {
							if string(before[table]) != "[]" {
								t.Fatalf("%s must be empty before the refusal: %s", table, before[table])
							}
						}
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
						}
						requests += max(request.Repeat, 1)
						resp, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						// A refusal never issues a credential on either transport.
						if s.ID != "me.disabled_account" && bytes.Contains(resp.Raw, []byte("access_token")) {
							t.Error("refusal body carries a credential")
						}
						after := snapshot()
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Errorf("%s rows changed during an oauth refusal or disabled-account read", id)
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredOAuthRefusalScenarios); err != nil {
		t.Error(err)
	}
	if requests != 42 || effects != 84 {
		t.Errorf("paired oauth refusal evidence %dHTTP/%dPG, want42/84", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

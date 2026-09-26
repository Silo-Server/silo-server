package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredLogoutSuccessAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-logout-success for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.LogoutSuccessAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				for _, transport := range []string{"v1", "v2"} {
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						// This focused runner supplies fresh state before AND after each transport,
						// including FreshState originals, and observes effects before teardown.
						e.Reseed()
						defer e.Reseed()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						if len(s.Settings) > 0 {
							settings := map[string]string{}
							for k, v := range s.Settings {
								resolved, err := e.substitute(v)
								if err != nil {
									t.Fatal(err)
								}
								settings[k] = resolved
							}
							defer e.applySettings(settings)()
						}
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
						lower := dbNow()
						if len(before) != 8 {
							t.Fatal("snapshot must contain eight full tables")
						}
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
							if pair.Principal != nil {
								principal = *pair.Principal
							}
						}
						requests += max(request.Repeat, 1)
						_, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						upper := dbNow()
						after := snapshot()
						var prior, current []map[string]json.RawMessage
						if err := json.Unmarshal(before["sessions"], &prior); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(after["sessions"], &current); err != nil {
							t.Fatal(err)
						}
						if len(prior) != len(current) {
							t.Fatal("session row count changed")
						}
						target, matched := e.sessions[fixtureMember], 0
						for i, row := range prior {
							var id string
							if err := json.Unmarshal(row["id"], &id); err != nil {
								t.Fatal(err)
							}
							if id != target {
								continue
							}
							matched++
							if string(row["revoked_at"]) != "null" {
								t.Fatal("target session already revoked")
							}
							if !bytes.Equal(row["id"], current[i]["id"]) {
								t.Fatal("session identity changed")
							}
							var revoked time.Time
							if err := json.Unmarshal(current[i]["revoked_at"], &revoked); err != nil {
								t.Fatal(err)
							}
							if revoked.Before(lower) || revoked.After(upper) {
								t.Fatal("revocation outside database request bounds")
							}
							row["revoked_at"] = current[i]["revoked_at"]
						}
						if matched != 1 {
							t.Fatal("expected exactly one target login session")
						}
						before["sessions"], err = json.Marshal(prior)
						if err != nil {
							t.Fatal(err)
						}
						after["sessions"], err = json.Marshal(current)
						if err != nil {
							t.Fatal(err)
						}
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/login-session/device-request/invitation/invite-code rows changed during logout success")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredLogoutSuccessScenarios); err != nil {
		t.Error(err)
	}
	if requests != 4 || effects != 8 {
		t.Errorf("paired logout success evidence %dHTTP/%dPG, want4/8", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

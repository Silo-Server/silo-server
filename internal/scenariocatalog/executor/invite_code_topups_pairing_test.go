package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredInviteCodeTopUpsAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-invite-code-topups for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.InviteCodeTopUpsAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect administrator invite-code refusal scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("administrator invite-code refusal pre-constructor guards passed")
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(t) ORDER BY key) FROM server_settings t),'sessions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM auth_sessions t),'device_requests',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM device_login_requests t),'invitations',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invitations t),'invite_codes',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invite_codes t))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						var started time.Time
						if err := e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&started); err != nil {
							t.Fatal(err)
						}
						before := snapshot()
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
						after := snapshot()
						var finished time.Time
						if err := e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&finished); err != nil {
							t.Fatal(err)
						}
						assertInviteCodeTopUp(t, s.ID, e.fixtures["invite_code_id"], before["invite_codes"], after["invite_codes"], started, finished)
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if id == "invite_codes" {
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/session/device-request/invitation/code rows changed during invite-code top-up refusal")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredInviteCodeTopUpsScenarios); err != nil {
		t.Error(err)
	}
	if requests != 6 || effects != 12 {
		t.Errorf("paired administrator invite-code refusal evidence %dHTTP/%dPG, want6/12", requests, effects)
	}
	t.Logf("administrator invite-code refusals: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, effects, effects*8)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Compare every code column, allowing only the reserved count and a bounded update time.
func assertInviteCodeTopUp(t *testing.T, scenario, target string, before, after json.RawMessage, started, finished time.Time) {
	t.Helper()
	var previous, current []map[string]json.RawMessage
	if err := json.Unmarshal(before, &previous); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &current); err != nil {
		t.Fatal(err)
	}
	if len(previous) != len(current) {
		t.Fatal("invite-code row count changed")
	}
	matched := 0
	for i, want := range previous {
		got := current[i]
		if string(want["id"]) == target {
			matched++
			var oldTime, newTime time.Time
			if err := json.Unmarshal(want["updated_at"], &oldTime); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got["updated_at"], &newTime); err != nil {
				t.Fatal(err)
			}
			if newTime.Before(started) || newTime.After(finished) || newTime.Before(oldTime) {
				t.Error("target update time outside database request bounds")
			}
			amount := 2
			switch scenario {
			case "codes_topup.ok", "codes_topup.meaning":
			case "codes_topup.shape":
				amount = 1
			default:
				t.Fatal("unsupported top-up effect")
			}
			var priorMax int
			if err := json.Unmarshal(want["max_uses"], &priorMax); err != nil {
				t.Fatal(err)
			}
			expectedMax, err := json.Marshal(priorMax + amount)
			if err != nil {
				t.Fatal(err)
			}
			want["max_uses"] = expectedMax
			want["updated_at"] = got["updated_at"]
		}
		if len(want) != len(got) {
			t.Error("invite-code column count changed")
		}
		for key, value := range want {
			if !bytes.Equal(value, got[key]) {
				t.Errorf("unexpected invite-code field effect: %s", key)
			}
		}
	}
	if matched != 1 {
		t.Errorf("matched %d target code rows, want 1", matched)
	}
}

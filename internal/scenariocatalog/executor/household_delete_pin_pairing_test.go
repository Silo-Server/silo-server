package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredHouseholdDeletePINAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-household-delete-pin for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.HouseholdDeletePINAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect administrator household refusal scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("administrator household refusal pre-constructor guards passed")
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
							rows := map[string]json.RawMessage{}
							for _, table := range householdEffectTables {
								var raw []byte
								if err := e.pool.QueryRow(e.ctx, fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t", table)).Scan(&raw); err != nil {
									t.Fatal(err)
								}
								rows[table] = raw
							}
							return rows
						}
						before := snapshot()
						if len(before) != 19 {
							t.Fatal("snapshot must contain nineteen full tables")
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
						if s.ID == "profiles_delete.shape" {
							assertHouseholdProfileDeletion(t, profileChild, before["user_profiles"], after["user_profiles"])
						}
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if id == "user_profiles" && s.ID == "profiles_delete.shape" {
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/session/device-request/invitation/profile rows changed during household deletion refusal")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredHouseholdDeletePINScenarios); err != nil {
		t.Error(err)
	}
	if requests != 28 || effects != 56 {
		t.Errorf("paired administrator household refusal evidence %dHTTP/%dPG, want28/56", requests, effects)
	}
	t.Logf("administrator household refusals: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, effects, effects*19)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Require precisely the selected row to disappear, preserving all surviving columns.
func assertHouseholdProfileDeletion(t *testing.T, target string, before, after json.RawMessage) {
	t.Helper()
	var previous, current []map[string]json.RawMessage
	if err := json.Unmarshal(before, &previous); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &current); err != nil {
		t.Fatal(err)
	}
	matched, remaining := 0, 0
	for _, want := range previous {
		var id string
		if err := json.Unmarshal(want["id"], &id); err != nil {
			t.Fatal(err)
		}
		if id == target {
			matched++
			continue
		}
		if remaining >= len(current) {
			t.Fatal("unexpected missing surviving profile")
		}
		got := current[remaining]
		remaining++
		if len(want) != len(got) {
			t.Error("surviving profile column count changed")
		}
		for key, value := range want {
			if !bytes.Equal(value, got[key]) {
				t.Errorf("unexpected surviving profile field change: %s", key)
			}
		}
	}
	if matched != 1 || remaining != len(current) {
		t.Errorf("deletion effect: matched %d target rows and %d/%d survivors", matched, remaining, len(current))
	}
}

var householdEffectTables = []string{"users", "user_profiles", "api_keys", "server_settings", "auth_sessions", "device_login_requests", "invitations", "invite_codes", "user_devices", "user_device_settings", "user_favorites", "user_watchlist", "user_watch_progress", "user_collection_sort_preferences", "user_personal_collections", "user_personal_collection_items", "user_series_playback_preferences", "user_library_playback_preferences", "user_setting_values"}

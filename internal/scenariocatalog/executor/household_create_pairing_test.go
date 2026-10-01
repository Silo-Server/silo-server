package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredHouseholdCreateAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-household-create for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.HouseholdCreateAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect household creation scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("household creation pre-constructor guards passed")
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
							for _, table := range append(append([]string{}, householdEffectTables...), "user_setting_mutations", "user_setting_migration_rejects", "user_profile_allowed_libraries", "user_settings") {
								var raw []byte
								if err := e.pool.QueryRow(e.ctx, fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t", table)).Scan(&raw); err != nil {
									t.Fatal(err)
								}
								rows[table] = raw
							}
							return rows
						}
						before := snapshot()
						sequence := func() int64 {
							var value int64
							var called bool
							if err := e.pool.QueryRow(e.ctx, "SELECT last_value, is_called FROM user_setting_values_id_seq").Scan(&value, &called); err != nil {
								t.Fatal(err)
							}
							if !called {
								return value - 1
							}
							return value
						}
						seqBefore := sequence()
						var dbStart time.Time
						if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&dbStart); err != nil {
							t.Fatal(err)
						}
						appStart := time.Now().UTC().Truncate(time.Second)
						if len(before) != 23 {
							t.Fatal("snapshot must contain twenty-three full tables")
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
						var middle map[string]json.RawMessage
						var created response
						if request.Repeat == 2 {
							single := request
							single.Repeat = 1
							first, failures, err := e.exchange(e.live.URL, method, single, principal, scenariocatalog.Expect{Status: 201}, nil, nil, nil)
							if err != nil || len(failures) > 0 {
								t.Fatal("first creation failed", err, failures)
							}
							created = first
							middle = snapshot()
							request.Repeat = 1
						}
						resp, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						appEnd := time.Now().UTC()
						var dbEnd time.Time
						if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&dbEnd); err != nil {
							t.Fatal(err)
						}
						after := snapshot()
						success := householdCreates(s.ID)
						advance := int64(0)
						if success {
							advance = 5
						}
						if sequence() != seqBefore+advance {
							t.Error("unexpected canonical sequence allocation")
						}
						for table, rows := range middle {
							if !bytes.Equal(rows, after[table]) {
								t.Errorf("second create changed %s", table)
							}
						}
						if created.Status == 0 {
							created = resp
						}
						if success {
							assertHouseholdCreation(t, e, s.ID, created, seqBefore, appStart, appEnd, dbStart, dbEnd, before, after)
						}
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if success && (id == "user_profiles" || id == "user_setting_values" || (id == "users" && s.ID == "profiles_create.meaning")) {
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/session/device-request/invitation/profile rows changed during household creation")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredHouseholdCreateScenarios); err != nil {
		t.Error(err)
	}
	if requests != 34 || effects != 66 {
		t.Errorf("paired household creation evidence %dHTTP/%dPG, want34/66", requests, effects)
	}
	t.Logf("household creations: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, effects, effects*23)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

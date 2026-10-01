package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequiredOnboardingProgressAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-onboarding-progress for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.OnboardingProgressAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect onboarding scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("onboarding progress pre-constructor guards passed")
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, snapshots := 0, 0
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
								result.Failures = append(result.Failures, "scenario assertion failed; see log")
							}
							results = append(results, result)
						}()
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							snapshots++
							out := map[string]json.RawMessage{}
							for _, table := range append(append([]string{}, householdEffectTables...), "user_setting_mutations", "user_setting_migration_rejects", "user_profile_allowed_libraries", "user_settings", "user_profile_onboarding", "request_settings") {
								var raw []byte
								if err := e.pool.QueryRow(e.ctx, fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t", table)).Scan(&raw); err != nil {
									t.Fatal(err)
								}
								out[table] = raw
							}
							if len(out) != 25 {
								t.Fatal("expected twenty-five full tables")
							}
							return out
						}
						var previous *response
						read := func(method string, request scenariocatalog.Request, principal scenariocatalog.Principal, expect scenariocatalog.Expect, bindings []scenariocatalog.ResponseBinding) {
							t.Helper()
							before := snapshot()
							started := time.Now().UTC()
							requests += max(request.Repeat, 1)
							resp, failures, err := e.exchange(e.live.URL, method, request, principal, expect, previous, bindings, nil)
							finished := time.Now().UTC()
							previous = &resp
							if err != nil {
								failures = append(failures, err.Error())
							}
							result.Failures = append(result.Failures, failures...)
							if len(failures) > 0 {
								t.Errorf("frozen onboarding exchange: %v", failures)
							}
							after := snapshot()
							write := (method == "POST" || method == "PUT") && expect.Status < 300
							if write {
								assertOnboardingProgressEffect(t, e, method, request, started, finished, before["user_profile_onboarding"], after["user_profile_onboarding"])
							}
							for table, want := range before {
								if write && table == "user_profile_onboarding" {
									continue
								}
								if !bytes.Equal(want, after[table]) {
									t.Errorf("onboarding progress changed full table %s", table)
								}
							}
						}
						if transport == "v1" {
							read(row.Method, s.Request, s.Principal, s.Expect, nil)
							for _, step := range s.Then {
								principal := s.Principal
								if step.Principal != nil {
									principal = *step.Principal
								}
								read(step.Method, step.Request, principal, step.Expect, nil)
							}
						} else {
							pair := s.V2Expectation
							result.OperationID = pair.OperationID
							read(pair.Method, pair.Request, s.Principal, pair.Expect, nil)
							for _, step := range pair.Then {
								principal := s.Principal
								if step.Principal != nil {
									principal = *step.Principal
								}
								read(step.Method, step.Request, principal, step.Expect, step.FromPrevious)
							}
						}

					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredOnboardingProgressScenarios); err != nil {
		t.Error(err)
	}
	if requests != 28 || snapshots != 56 {
		t.Errorf("onboarding counters %d HTTP/%d snapshots, want28/56", requests, snapshots)
	}
	t.Logf("onboarding progress: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, snapshots, snapshots*25)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

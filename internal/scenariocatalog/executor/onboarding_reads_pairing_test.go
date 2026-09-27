package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequiredOnboardingReadsAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-onboarding-reads for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.OnboardingReadsAcceptance(catalogs)
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
	t.Log("onboarding read pre-constructor guards passed")
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
						read := func(method string, request scenariocatalog.Request, principal scenariocatalog.Principal, expect scenariocatalog.Expect) {
							t.Helper()
							before := snapshot()
							if !bytes.Equal(before["user_profile_onboarding"], []byte("[]")) {
								t.Fatal("expected fresh onboarding fixture")
							}
							requests += max(request.Repeat, 1)
							_, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
							if err != nil {
								failures = append(failures, err.Error())
							}
							result.Failures = append(result.Failures, failures...)
							if len(failures) > 0 {
								t.Errorf("frozen onboarding exchange: %v", failures)
							}
							after := snapshot()
							for table, want := range before {
								if !bytes.Equal(want, after[table]) {
									t.Errorf("onboarding read changed full table %s", table)
								}
							}
						}
						request, expect, principal, method, steps := s.Request, s.Expect, s.Principal, row.Method, s.Then
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							steps = nil
							for _, step := range pair.Then {
								steps = append(steps, step.Step)
							}
							result.OperationID = pair.OperationID
						}
						read(method, request, principal, expect)
						for _, step := range steps {
							stepPrincipal := principal
							if step.Principal != nil {
								stepPrincipal = *step.Principal
							}
							read(step.Method, step.Request, stepPrincipal, step.Expect)
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredOnboardingReadsScenarios); err != nil {
		t.Error(err)
	}
	if requests != 36 || snapshots != 72 {
		t.Errorf("onboarding counters %d HTTP/%d snapshots, want36/72", requests, snapshots)
	}
	t.Logf("onboarding reads: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, snapshots, snapshots*25)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Router startup recovery can change stale jobs. Refuse existing jobs before New.
func guardNewTranslationFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect translation fixture database")
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('metadata_translation_jobs') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM metadata_translation_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("NEW translation fixture requires empty jobs before router recovery or reseeding")
	}
}

func TestRequiredNewTranslationJobs(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-translation-jobs for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewTranslationFixture(t)
	e := New(t)
	const base int64 = 900000100
	pendingID := strconv.FormatInt(base+52, 10)
	var fixture catalogMediaFixture
	var jobIDs []int64
	cleanup := func() {
		e.mustExec(`DELETE FROM metadata_translation_jobs WHERE id=ANY($1)`, jobIDs)
		jobIDs = nil
		fixture.cleanup(t, e)
	}
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase(); guardNewTranslationFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"newest_50", "empty_list", "foreign_job", "pending_cancel", "terminal_cancel", "acting_admin_gate"} {
		cleanup()
		e.Reseed()
		fixture.seed(t, e)
		// Seed completed history and one fresh pending row, never enqueue or dispatch.
		for i := 1; i <= 52; i++ {
			content, status := catalogAlpha, "completed"
			if i == 52 {
				content, status = catalogBeta, "pending"
			}
			jobID := base + int64(i)
			e.mustExec(`INSERT INTO metadata_translation_jobs(id,target_kind,content_id,target_language,status,idempotency_key,requested_by,created_at,heartbeat_at) VALUES($1,'item',$2,'fr',$3,$4,$5,'2026-01-02T03:04:05Z'::timestamptz+$6*interval '1 second',now())`, jobID, content, status, fmt.Sprintf("synthetic-translation-%d", i), e.users[fixtureAdmin].ID, i)
			jobIDs = append(jobIDs, jobID)
		}
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_translation_jobs." + id + "/v2", Scenario: "new_translation_jobs." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() map[string]map[string]any {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_object_agg(id::text,to_jsonb(j)) FROM metadata_translation_jobs j`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows map[string]map[string]any
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 52 {
					t.Fatalf("job count%d,want52", len(rows))
				}
				for _, jobID := range jobIDs {
					if rows[strconv.FormatInt(jobID, 10)] == nil {
						t.Fatalf("missing fixture job%d", jobID)
					}
				}
				return rows
			}
			want := snapshot()
			exchange := func(method, target, suffix string, principal scenariocatalog.Principal, expect scenariocatalog.Expect) {
				t.Helper()
				requests++
				_, failures, err := e.exchange(e.live.URL, method, scenariocatalog.Request{Path: "/api/v2/admin/items/" + target + "/metadata-translation/jobs" + suffix}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("translation request: %v", failures)
				}
			}
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			noContent := scenariocatalog.Expect{Status: 204, BodyKind: "empty"}
			switch id {
			case "newest_50":
				checks := []scenariocatalog.BodyAssertion{catalogAssertion("/jobs", "length", 50)}
				for i := range 50 {
					path := fmt.Sprintf("/jobs/%d", i)
					checks = append(checks, catalogAssertion(path+"/id", "equals", strconv.FormatInt(base+51-int64(i), 10)), catalogAssertion(path+"/content_id", "equals", catalogAlpha), catalogAssertion(path+"/status", "equals", "completed"), catalogAssertion(path+"/requested_by", "absent", nil), catalogAssertion(path+"/idempotency_key", "absent", nil))
				}
				exchange(http.MethodGet, catalogAlpha, "", admin, catalogOK(checks...))
			case "empty_list":
				exchange(http.MethodGet, catalogHidden, "", admin, catalogOK(catalogAssertion("/jobs", "equals", []any{})))
			case "foreign_job":
				exchange(http.MethodPost, catalogAlpha, "/"+pendingID+"/cancel", admin, catalogProblem(404, "not_found"))
			case "pending_cancel":
				exchange(http.MethodPost, catalogBeta, "/"+pendingID+"/cancel", admin, noContent)
				want[pendingID]["status"] = "cancelled"        //nolint:misspell // Exact persisted status spelling.
				want[pendingID]["error_message"] = "cancelled" //nolint:misspell // Exact persisted service message.
			case "terminal_cancel":
				exchange(http.MethodPost, catalogAlpha, "/"+strconv.FormatInt(base+51, 10)+"/cancel", admin, noContent)
			case "acting_admin_gate":
				for _, principal := range []scenariocatalog.Principal{{Class: "primary_profile"}, {Class: "acting_admin", Profile: "admin_secondary"}} {
					exchange(http.MethodPost, catalogBeta, "/"+pendingID+"/cancel", principal, catalogProblem(403, "permission_denied"))
				}
			default:
				t.Fatal("unimplemented case")
			}
			got := snapshot()
			if id == "pending_cancel" {
				delete(want[pendingID], "updated_at")
				delete(got[pendingID], "updated_at")
				delete(want[pendingID], "heartbeat_at")
				delete(got[pendingID], "heartbeat_at")
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatal("translation full-table snapshot differs beyond expected cancellation")
			}
		})
	}
	if len(results) != 6 || requests != 7 || effects != 12 {
		t.Errorf("NEW translation evidence%d/%d/%d,want6/7/12", len(results), requests, effects)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.ID] || !r.Passed() {
			t.Errorf("duplicate or failed result%s: %v", r.ID, r.Failures)
		}
		seen[r.ID] = true
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                                       string
			NewScenarios, PhysicalRequests, EffectReads int
			Results                                     []Result
		}{"NEW translation jobs (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

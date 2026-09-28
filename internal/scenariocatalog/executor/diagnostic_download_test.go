package executor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reports reference users: refuse their data before New can cascade a reseed.
func guardNewDiagnosticFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect diagnostic fixture database")
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('client_diagnostic_reports') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM client_diagnostic_reports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("NEW diagnostic fixture requires empty reports before reseeding")
	}
}

func TestRequiredNewDiagnosticDownloadFailures(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-diagnostic-download for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewDiagnosticFixture(t)
	e := New(t)
	const reportID = "00000000-0000-4000-8000-000000000081"
	cleanup := func() { e.mustExec(`DELETE FROM client_diagnostic_reports WHERE id=$1::uuid`, reportID) }
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase(); guardNewDiagnosticFixture(t) }()
	requests, effects := 0, 0
	var results []Result
	for _, id := range []string{"receiving", "failed", "ready_without_storage", "missing_identity", "acting_admin_gate"} {
		cleanup()
		e.Reseed()
		state := "ready"
		if id == "receiving" || id == "failed" {
			state = id
		}
		e.mustExec(`INSERT INTO client_diagnostic_reports(id,short_id,user_id,profile_id,state,captured_at,report_type,platform,app_version,manifest,blob_bucket,blob_key,blob_bytes) VALUES($1::uuid,'SILO-000000000081',$2,$3,$4,'2026-01-02T03:04:05Z','manual','android','synthetic','{"synthetic":true}','synthetic-private-bucket','synthetic-private-key',17)`, reportID, e.users[fixtureMember].ID, profileSecondary, state)
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_diagnostic_download." + id + "/v2", Scenario: "new_diagnostic_download." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() []byte {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM client_diagnostic_reports r`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			before := snapshot()
			get := func(target string, principal scenariocatalog.Principal, status int, code string) {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(e.live.URL, http.MethodGet, scenariocatalog.Request{Path: "/api/v2/admin/diagnostics/reports/" + target + "/download", Headers: map[string]*string{"Range": new("bytes=0-3")}}, principal, catalogProblem(status, code), nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("download %s: %v", target, failures)
				}
				for _, header := range []string{"Content-Disposition", "Content-Range", "Location"} {
					if resp.Headers.Get(header) != "" {
						t.Fatalf("pre-stream error set %s", header)
					}
				}
				for _, private := range []string{"synthetic-private-bucket", "synthetic-private-key", "SILO-000000000081", "\"synthetic\""} {
					if strings.Contains(string(resp.Raw), private) {
						t.Fatalf("pre-stream error exposed report metadata %q", private)
					}
				}
			}
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			switch id {
			case "receiving", "failed":
				get(reportID, admin, 409, "conflict")
			case "ready_without_storage":
				get(reportID, admin, 503, "dependency_unavailable")
			case "missing_identity":
				get("00000000-0000-4000-8000-000000000082", admin, 404, "not_found")
			case "acting_admin_gate":
				get(reportID, scenariocatalog.Principal{Class: "profile"}, 403, "permission_denied")
				get(reportID, scenariocatalog.Principal{Class: "acting_admin", Profile: "admin_secondary"}, 403, "permission_denied")
				get(reportID, scenariocatalog.Principal{Class: "public"}, 401, "authentication_required")
			default:
				t.Fatal("unimplemented case")
			}
			if !bytes.Equal(before, snapshot()) {
				t.Fatal("download failure changed persisted report")
			}
		})
	}
	if len(results) != 5 || requests != 7 || effects != 10 {
		t.Errorf("NEW diagnostic evidence %d/%d/%d, want5/7/10", len(results), requests, effects)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.ID] || !r.Passed() {
			t.Errorf("duplicate or failed result %s: %v", r.ID, r.Failures)
		}
		seen[r.ID] = true
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                                       string
			NewScenarios, PhysicalRequests, EffectReads int
			Results                                     []Result
		}{"NEW diagnostic download failures (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

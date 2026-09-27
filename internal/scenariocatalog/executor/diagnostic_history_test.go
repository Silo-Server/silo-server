package executor

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredNewDiagnosticHistory(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-diagnostic-history for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewDiagnosticFixture(t)
	e := New(t)
	ids := []string{"00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000102", "00000000-0000-4000-8000-000000000103"}
	cleanup := func() { e.mustExec(`DELETE FROM client_diagnostic_reports WHERE id=ANY($1::uuid[])`, ids) }
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase(); guardNewDiagnosticFixture(t) }()
	requests, effects := 0, 0
	var results []Result
	for _, id := range []string{"timestamp_ties", "cursor_binding", "detail_manifest", "delete_receipt", "denied_delete"} {
		cleanup()
		e.Reseed()
		for i, report := range ids {
			e.mustExec(`INSERT INTO client_diagnostic_reports(id,short_id,user_id,profile_id,state,captured_at,received_at,report_type,platform,app_version,manifest,blob_bucket,blob_key) VALUES($1::uuid,$2,$3,$4,'ready','2026-01-02T03:04:05.123456Z','2026-01-02T03:04:05.123456Z','manual','android','synthetic','{"schema_version":1,"extension":{"synthetic":true}}','synthetic-private-bucket','synthetic-private-key')`, report, "SILO-000000000"+strconv.Itoa(101+i), e.users[fixtureMember].ID, profileSecondary)
		}
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_diagnostic_history." + id + "/v2", Scenario: "new_diagnostic_history." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() map[string]any {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT COALESCE(jsonb_object_agg(id,to_jsonb(r)),'{}') FROM client_diagnostic_reports r`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows map[string]any
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			want := snapshot()
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			exchange := func(method, path string, query map[string]string, principal scenariocatalog.Principal, expect scenariocatalog.Expect) response {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(e.live.URL, method, scenariocatalog.Request{Path: "/api/v2/admin/diagnostics/reports" + path, Query: query}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("%s %s: %v", method, path, failures)
				}
				if strings.Contains(string(resp.Raw), "synthetic-private-") {
					t.Fatal("storage location leaked")
				}
				return resp
			}
			first := func() response {
				return exchange(http.MethodGet, "", map[string]string{"limit": "2"}, admin, catalogOK(catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/id", "equals", ids[2]), catalogAssertion("/items/1/id", "equals", ids[1]), catalogAssertion("/items/0/manifest", "absent", nil), catalogAssertion("/items/0/user_id", "equals", strconv.Itoa(e.users[fixtureMember].ID)), catalogAssertion("/items/0/received_at", "equals", "2026-01-02T03:04:05.123Z"), catalogAssertion("/page/has_more", "equals", true)))
			}
			cursor := func(resp response) string {
				t.Helper()
				var body struct {
					Page struct {
						Next string `json:"next_cursor"`
					} `json:"page"`
				}
				if err := json.Unmarshal(resp.Raw, &body); err != nil {
					t.Fatal(err)
				}
				if body.Page.Next == "" {
					t.Fatal("missing continuation")
				}
				return body.Page.Next
			}
			switch id {
			case "timestamp_ties":
				next := cursor(first())
				exchange(http.MethodGet, "", map[string]string{"limit": "2", "cursor": next}, admin, catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", ids[0]), catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/page/next_cursor", "absent", nil)))
			case "cursor_binding":
				next := cursor(first())
				exchange(http.MethodGet, "", map[string]string{"limit": "1", "cursor": next}, admin, catalogProblem(400, "invalid_cursor"))
				exchange(http.MethodGet, "", map[string]string{"limit": "2", "platform": "ios", "cursor": next}, admin, catalogProblem(400, "invalid_cursor"))
			case "detail_manifest":
				exchange(http.MethodGet, "/"+ids[1], nil, admin, catalogOK(catalogAssertion("/id", "equals", ids[1]), catalogAssertion("/profile_id", "equals", profileSecondary), catalogAssertion("/manifest/schema_version", "equals", 1), catalogAssertion("/manifest/extension/synthetic", "equals", true), catalogAssertion("/playback_session_ids", "length", 0)))
			case "delete_receipt":
				for range 2 {
					exchange(http.MethodDelete, "/"+ids[1], nil, admin, scenariocatalog.Expect{Status: 204, BodyKind: "empty"})
				}
				exchange(http.MethodGet, "/"+ids[1], nil, admin, catalogProblem(404, "not_found"))
				exchange(http.MethodGet, "", nil, admin, catalogOK(catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/id", "equals", ids[2]), catalogAssertion("/items/1/id", "equals", ids[0]), catalogAssertion("/page/has_more", "equals", false)))
				delete(want, ids[1])
			case "denied_delete":
				for _, principal := range []scenariocatalog.Principal{{Class: "profile"}, {Class: "acting_admin", Profile: "admin_secondary"}} {
					exchange(http.MethodDelete, "/"+ids[1], nil, principal, catalogProblem(403, "permission_denied"))
				}
			default:
				t.Fatal("unimplemented case")
			}
			if got := snapshot(); !reflect.DeepEqual(want, got) {
				t.Fatal("persisted reports changed outside expected exact deletion")
			}
		})
	}
	if len(results) != 5 || requests != 12 || effects != 10 {
		t.Errorf("NEW diagnostic history evidence%d/%d/%d, want5/12/10", len(results), requests, effects)
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
		}{"NEW diagnostic history (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

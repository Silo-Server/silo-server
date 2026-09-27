package executor

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This guard runs before New: reseeding users cascades to match decisions.
func guardNewLiteraryFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect literary fixture database")
	}
	defer pool.Close()
	for _, table := range []string{"literary_works", "literary_work_items", "literary_work_match_decisions"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			continue
		}
		var count int
		// Identifiers come exclusively from the fixed table list above.
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("NEW literary fixture requires empty %s before reseeding", table)
		}
	}
}

func TestRequiredNewLiteraryAdmin(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-literary-admin for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewLiteraryFixture(t)
	e := New(t)
	const book = "acceptance-literary-ebook"
	const audio = "acceptance-literary-audio"
	const work = "acceptance-literary-work"
	const base = "/api/v2/admin/literary-works"
	cleanup := func() {
		e.mustExec(`DELETE FROM media_items WHERE content_id=ANY($1)`, []string{book, audio})
		e.mustExec(`DELETE FROM literary_works WHERE work_id=$1`, work)
	}
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase(); guardNewLiteraryFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"link_unlink_identity", "confirm_account", "ignore_reverse", "acting_admin_gate", "invalid_no_effect", "missing_no_effect"} {
		cleanup()
		e.Reseed()
		for _, item := range []struct{ id, kind string }{{book, "ebook"}, {audio, "audiobook"}} {
			e.mustExec(`INSERT INTO media_items(content_id,type,title,sort_title,status,genres) VALUES($1,$2,'Synthetic literary acceptance','Synthetic literary acceptance','matched','{}')`, item.id, item.kind)
			e.mustExec(`INSERT INTO media_item_provider_ids(content_id,item_type,provider,provider_id) VALUES($1,$2,'isbn','synthetic-literary-pair')`, item.id, item.kind)
		}
		e.mustExec(`INSERT INTO literary_works(work_id,canonical_title,normalized_title) VALUES($1,'Synthetic literary acceptance','synthetic literary acceptance')`, work)
		e.mustExec(`INSERT INTO literary_work_items(work_id,content_id,format_type,link_source,confidence,confirmed_at) VALUES($1,$2,'ebook','manual',1,now())`, work, book)
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_literary_admin." + id + "/v2", Scenario: "new_literary_admin." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			exchange := func(method, path string, body any, principal scenariocatalog.Principal, expect scenariocatalog.Expect) {
				t.Helper()
				requests++
				var raw json.RawMessage
				if body != nil {
					var err error
					raw, err = json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
				}
				_, failures, err := e.exchange(e.live.URL, method, scenariocatalog.Request{Path: base + path, Body: raw}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("%s %s: %v", method, path, failures)
				}
			}
			// Check the entire owned relation, including item identity and attribution.
			state := func(wantAudio bool, wantDecision string) {
				t.Helper()
				effects++
				var books, audios, links, works, decisions, attributed int
				err := e.pool.QueryRow(e.ctx, `SELECT
     (SELECT count(*) FROM literary_work_items WHERE work_id=$1 AND content_id=$2 AND format_type='ebook' AND link_source='manual' AND confidence=1 AND confirmed_at IS NOT NULL),
     (SELECT count(*) FROM literary_work_items WHERE work_id=$1 AND content_id=$3 AND format_type='audiobook' AND link_source='manual' AND confidence=1 AND confirmed_at IS NOT NULL),
     (SELECT count(*) FROM literary_work_items), (SELECT count(*) FROM literary_works),
     (SELECT count(*) FROM literary_work_match_decisions),
     (SELECT count(*) FROM literary_work_match_decisions WHERE source_content_id=$2 AND target_content_id=$3 AND decision=$4 AND created_by=$5)`, work, book, audio, wantDecision, e.users[fixtureAdmin].ID).Scan(&books, &audios, &links, &works, &decisions, &attributed)
				if err != nil {
					t.Fatal(err)
				}
				wantLinks, wantAudios, wantDecisions := 1, 0, 0
				if wantAudio {
					wantLinks = 2
					wantAudios = 1
				}
				if wantDecision != "" {
					wantDecisions = 1
				}
				if books != 1 || audios != wantAudios || links != wantLinks || works != 1 || decisions != wantDecisions || attributed != wantDecisions {
					t.Fatalf("persisted books/audio/links/works/decisions/attributed=%d/%d/%d/%d/%d/%d", books, audios, links, works, decisions, attributed)
				}
			}
			pair := map[string]string{"source_content_id": book, "target_content_id": audio}
			link := map[string]any{"content_ids": []string{book, audio}}
			empty := scenariocatalog.Expect{Status: 204, BodyKind: "empty"}
			switch id {
			case "link_unlink_identity":
				exchange(http.MethodPost, "/link", link, admin, catalogOK(catalogAssertion("/work_id", "equals", work)))
				state(true, "")
				exchange(http.MethodDelete, "/different-work/items/"+audio, nil, admin, empty)
				state(true, "")
				exchange(http.MethodDelete, "/"+work+"/items/"+audio, nil, admin, empty)
				state(false, "")
			case "confirm_account":
				exchange(http.MethodPost, "/matches/confirm", pair, admin, catalogOK(catalogAssertion("/status", "equals", "ok"), catalogAssertion("/work_id", "equals", work)))
				state(true, "confirmed")
			case "ignore_reverse":
				exchange(http.MethodGet, "/items/"+book+"/candidates", nil, admin, catalogOK(catalogAssertion("/candidates", "length", 1), catalogAssertion("/candidates/0/source_content_id", "equals", book), catalogAssertion("/candidates/0/target_content_id", "equals", audio), catalogAssertion("/candidates/0/score", "equals", 0.98), catalogAssertion("/candidates/0/evidence/external_id", "equals", "isbn:synthetic-literary-pair")))
				exchange(http.MethodPost, "/matches/ignore", pair, admin, catalogOK(catalogAssertion("/status", "equals", "ok"), catalogAssertion("/work_id", "absent", nil)))
				state(false, "ignored")
				exchange(http.MethodGet, "/items/"+audio+"/candidates", nil, admin, catalogOK(catalogAssertion("/candidates", "length", 0)))
			case "acting_admin_gate":
				exchange(http.MethodPost, "/matches/confirm", pair, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(403, "permission_denied"))
				exchange(http.MethodPost, "/matches/ignore", pair, scenariocatalog.Principal{Class: "acting_admin", Profile: "admin_secondary"}, catalogProblem(403, "permission_denied"))
				state(false, "")
			case "invalid_no_effect":
				exchange(http.MethodPost, "/matches/ignore", map[string]string{"source_content_id": book, "target_content_id": book}, admin, catalogProblem(422, "validation_failed"))
				exchange(http.MethodPost, "/link", map[string]any{"content_ids": []string{audio, " "}}, admin, catalogProblem(422, "validation_failed"))
				state(false, "")
			case "missing_no_effect":
				exchange(http.MethodPost, "/link", map[string]any{"content_ids": []string{audio, "missing-literary-item"}}, admin, catalogProblem(404, "not_found"))
				state(false, "")
			default:
				t.Fatal("unimplemented case")
			}
		})
	}
	if len(results) != 6 || requests != 12 || effects != 8 {
		t.Errorf("NEW literary evidence %d/%d/%d, want6/12/8", len(results), requests, effects)
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
		}{"NEW literary administration (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

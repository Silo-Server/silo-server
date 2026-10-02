package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func guardFrozenAPIKeyFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect frozen API-key fixture")
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('api_keys') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM api_keys k LEFT JOIN users u ON u.id=k.user_id WHERE NOT (COALESCE(u.email,'')=$1 AND k.label IN ('fixture-unscoped','fixture-scoped') OR COALESCE(u.email,'')=$2 AND k.label='fixture-member-key')`, adminEmail, memberEmail).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("frozen API-key fixture refuses existing non-fixture keys before reseeding")
	}
}

func TestRequiredAPIKeyDeleteAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-api-key-deletions for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.APIKeyDeleteAcceptance(catalogs)
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
							if err := e.pool.QueryRow(e.ctx, `SELECT COALESCE(jsonb_object_agg(id::text,to_jsonb(k)),'{}') FROM api_keys k`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						before := snapshot()
						if len(before) != 3 {
							t.Fatal("API-key fixture must contain exactly three keys")
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
						switch s.ID {
						case "keys_delete.ok", "keys_delete.gone", "keys_delete.shape":
							delete(before, e.fixtures["member_api_key_id"])
						}
						after := snapshot()
						if len(before) != len(after) {
							t.Error("unexpected API-key row count after deletion")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Error("API-key rows changed outside the exact intended deletion")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredAPIKeyDeleteScenarios); err != nil {
		t.Error(err)
	}
	if requests != 16 || effects != 28 {
		t.Errorf("paired deletion evidence%dHTTP/%dPG,want16/28", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

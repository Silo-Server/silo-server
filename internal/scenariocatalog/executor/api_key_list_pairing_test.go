package executor

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredAPIKeyListAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-api-key-lists for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.APIKeyListAcceptance(catalogs)
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
						after := snapshot()
						if len(before) != len(after) {
							t.Error("unexpected API-key row count after list")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Error("API-key rows changed during list")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredAPIKeyListScenarios); err != nil {
		t.Error(err)
	}
	if requests != 12 || effects != 24 {
		t.Errorf("paired list evidence %dHTTP/%dPG, want12/24", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Exercise the existing assertion engine without a database so a response leaking
// a secret cannot satisfy either overlay, while the frozen v1 oracle still needs it.
func TestAPIKeyListSecretAbsenceExpectations(t *testing.T) {
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.APIKeyListAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				if s.ID != "keys_list.meaning" && s.ID != "keys_list.shape" {
					continue
				}
				t.Run(s.ID, func(t *testing.T) {
					resolve := func(exp scenariocatalog.Expect) scenariocatalog.Expect {
						t.Helper()
						raw, err := json.Marshal(exp)
						if err != nil {
							t.Fatal(err)
						}
						raw = bytes.ReplaceAll(raw, []byte(`"${admin_user_id:int}"`), []byte(`1`))
						raw = bytes.ReplaceAll(raw, []byte(`${admin_user_id}`), []byte(`1`))
						var result scenariocatalog.Expect
						if err := json.Unmarshal(raw, &result); err != nil {
							t.Fatal(err)
						}
						return result
					}
					v1 := []any{
						map[string]any{"id": float64(1), "user_id": float64(1), "label": "first", "key": "sa_" + strings.Repeat("a", 64), "rate_tier": "standard", "scopes": []any{"admin:users"}, "created_at": "2026-01-02T00:00:00Z"},
						map[string]any{"id": float64(2), "user_id": float64(1), "label": "second", "key": "sa_" + strings.Repeat("b", 64), "rate_tier": "standard", "scopes": []any{}, "created_at": "2026-01-01T00:00:00Z"},
					}
					responseFor := func(doc any) response {
						return response{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, IsJSON: true, Doc: doc}
					}
					if failures := check(resolve(s.Expect), responseFor(v1)); len(failures) > 0 {
						t.Fatalf("full-secret v1 oracle failed: %v", failures)
					}
					delete(v1[0].(map[string]any), "key")
					if failures := check(resolve(s.Expect), responseFor(v1)); len(failures) == 0 {
						t.Fatal("v1 oracle accepted missing secret")
					}
					items := []any{}
					for i, original := range v1 {
						item := maps.Clone(original.(map[string]any))
						delete(item, "key")
						item["id"] = strconv.Itoa(i + 1)
						item["user_id"] = "1"
						item["key_prefix"] = "sa_example"
						items = append(items, item)
					}
					doc := map[string]any{"items": items}
					exp := resolve(s.V2Expectation.Expect)
					if failures := check(exp, responseFor(doc)); len(failures) > 0 {
						t.Fatalf("absence failed: %v", failures)
					}
					for i, item := range items {
						for _, secret := range []any{nil, "", "sa_" + strings.Repeat("c", 64)} {
							item.(map[string]any)["key"] = secret
							failures := check(exp, responseFor(doc))
							if len(failures) == 0 {
								t.Fatalf("v2 accepted present key on item %d", i)
							}
							// Require the explicit absence predicate to fail, not just keys_equal.
							absentFailed := false
							for _, assertion := range exp.Body {
								if bytes.Contains(assertion.Value, []byte(`"absent"`)) && checkBody(assertion, doc, true) != "" {
									absentFailed = true
								}
							}
							if !absentFailed {
								t.Fatal("secret presence did not fail absence predicate")
							}
							delete(item.(map[string]any), "key")
						}
					}
				})
			}
		}
	}
}

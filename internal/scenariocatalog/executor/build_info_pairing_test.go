package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredBuildInfoAcceptance(t *testing.T) {
	runSystemReadAcceptance(t, scenariocatalog.BuildInfoAcceptance, scenariocatalog.RequiredBuildInfoScenarios, 8)
}

func TestRequiredBuildAuthorityAcceptance(t *testing.T) {
	runSystemReadAcceptance(t, scenariocatalog.BuildAuthorityAcceptance, scenariocatalog.RequiredBuildAuthorityScenarios, 6)
}

func TestRequiredResourceRefusalAcceptance(t *testing.T) {
	runSystemReadAcceptance(t, scenariocatalog.ResourceRefusalAcceptance, scenariocatalog.RequiredResourceRefusalScenarios, 6)
}

func TestRequiredHardwareRefusalAcceptance(t *testing.T) {
	runSystemReadAcceptance(t, scenariocatalog.HardwareRefusalAcceptance, scenariocatalog.RequiredHardwareRefusalScenarios, 6)
}

func runSystemReadAcceptance(t *testing.T, selectCases func([]*scenariocatalog.Catalog) ([]*scenariocatalog.Catalog, error), ids []string, wantRequests int) {
	t.Helper()
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run the selected build-info, build-authority or resource-refusals scenario target for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectCases(catalogs)
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
				var original []byte
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k))`).Scan(&raw); err != nil {
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
							t.Fatal("snapshot must contain three full tables")
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
						response, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						if expect.Status == 200 {
							if transport == "v1" {
								original = response.Raw
							} else {
								checkBuildProjection(t, original, response.Raw)
							}
						}
						after := snapshot()
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key rows changed during system read")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, ids); err != nil {
		t.Error(err)
	}
	if requests != wantRequests || effects != 2*wantRequests {
		t.Errorf("paired system read evidence %dHTTP/%dPG, want%d/%d", requests, effects, wantRequests, 2*wantRequests)
	}
	t.Logf("verified %d HTTP requests, %d combined snapshots, %d full-table observations", requests, effects, effects*3)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Compare dynamic build metadata from the same binary, changing only the two
// timestamp projections explicitly permitted by this v2 pairing.
func checkBuildProjection(t *testing.T, original, current []byte) {
	t.Helper()
	var want, got map[string]json.RawMessage
	if json.Unmarshal(original, &want) != nil || json.Unmarshal(current, &got) != nil {
		t.Fatal("invalid build response")
	}
	for _, field := range []string{"vcs_time", "built_at"} {
		var value string
		if err := json.Unmarshal(want[field], &value); err != nil {
			t.Fatal("invalid original build timestamp")
		}
		if value == "" {
			delete(want, field)
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal("invalid original timestamp format")
		}
		want[field], _ = json.Marshal(stamp.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	if len(want) != len(got) {
		t.Error("build projection field set changed")
	}
	for key, value := range want {
		if !bytes.Equal(value, got[key]) {
			t.Errorf("build projection differs at %s", key)
		}
	}
}

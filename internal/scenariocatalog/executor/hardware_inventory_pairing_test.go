package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestRequiredHardwareInventoryAcceptance runs the three frozen local hardware
// inventory reads and the non-admin refusal shape on both transports. The
// executor wires no transcode pool, so every success is the local probe of
// this host's ffmpeg; only presence and types are asserted, never values.
func TestRequiredHardwareInventoryAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-hardware-inventory for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.HardwareInventoryAcceptance(catalogs)
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
						e.Reseed()
						defer e.Reseed()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							var raw []byte
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s),'sessions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM auth_sessions a),'device_requests',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM device_login_requests d),'invitations',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitations i),'invite_codes',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM invite_codes c))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							if len(rows) != 8 {
								t.Fatal("snapshot must contain eight full tables")
							}
							return rows
						}
						before := snapshot()
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
						}
						requests++
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
								checkHardwareProjection(t, original, response.Raw)
							}
						}
						after := snapshot()
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Errorf("stored %s rows changed during hardware inventory read", id)
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredHardwareInventoryScenarios); err != nil {
		t.Error(err)
	}
	if requests != 8 || effects != 16 {
		t.Errorf("paired hardware inventory evidence %dHTTP/%dPG, want8/16", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// checkHardwareProjection proves both transports report the same local probe:
// every v1 field is present in v2 with an equal value, except that v2 renders
// the two device lists as explicit arrays where v1 may emit null. Probe values
// themselves are host dependent and are never pinned.
func checkHardwareProjection(t *testing.T, original, current []byte) {
	t.Helper()
	var want, got map[string]json.RawMessage
	if json.Unmarshal(original, &want) != nil || json.Unmarshal(current, &got) != nil {
		t.Fatal("invalid hardware inventory response")
	}
	for _, key := range []string{"render_devices", "render_device_details"} {
		if bytes.Equal(bytes.TrimSpace(want[key]), []byte("null")) {
			want[key] = json.RawMessage("[]")
		}
		var list []any
		if err := json.Unmarshal(got[key], &list); err != nil {
			t.Errorf("v2 %s is not an array", key)
		}
	}
	if _, ok := want["nodes"]; ok {
		t.Fatal("local probe must not report nodes")
	}
	for key, value := range want {
		var a, b any
		if json.Unmarshal(value, &a) != nil || json.Unmarshal(got[key], &b) != nil || !jsonEqual(a, b) {
			t.Errorf("hardware projection differs at %s", key)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("v2 added hardware field %s absent from v1", key)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

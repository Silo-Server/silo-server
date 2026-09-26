package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestRequiredRetainedProbeAcceptance runs the seven frozen liveness and
// readiness originals against the retained v1 probes. There is no v2
// transport: the rows are ratified as retained unversioned probes. The four
// database_unavailable cases run on the executor's offline router (pool at a
// closed loopback port, proven by a failing ping before each exchange);
// health.identity/health.shape and ready.ok run on the live router over the
// owned database. Around every exchange the live database's account, profile,
// API-key and settings tables are snapshotted and must be unchanged: a probe
// reads, it never writes.
func TestRequiredRetainedProbeAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-probes for required acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.RetainedProbeAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, effects, outages, live := 0, 0, 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				t.Run(s.ID+"/v1", func(t *testing.T) {
					e.Reseed()
					defer e.Reseed()
					result := Result{ID: s.ID + "/v1", Scenario: s.ID, Transport: "v1", Catalog: c.File, Row: row.Key().String()}
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
						if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s))`).Scan(&raw); err != nil {
							t.Fatal(err)
						}
						var rows map[string]json.RawMessage
						if err := json.Unmarshal(raw, &rows); err != nil {
							t.Fatal(err)
						}
						return rows
					}
					base := e.live.URL
					if scenariocatalog.RetainedProbeNeedsOutage(s.ID) {
						var netErr net.Error
						if err := e.offlinePing(); err == nil || !errors.As(err, &netErr) {
							t.Fatalf("offline pool reached a database (%v); the outage state is not reproduced", err)
						}
						outages++
						base = e.offline.URL
					} else {
						if err := e.pool.Ping(e.ctx); err != nil {
							t.Fatalf("live pool unreachable: %v", err)
						}
						live++
					}
					before := snapshot()
					if len(before) != 4 {
						t.Fatal("snapshot must contain four full tables")
					}
					requests += max(s.Request.Repeat, 1)
					_, failures, err := e.exchange(base, row.Method, s.Request, s.Principal, s.Expect, nil, nil, nil)
					if err != nil {
						failures = append(failures, err.Error())
					}
					result.Failures = append(result.Failures, failures...)
					if len(failures) > 0 {
						t.Errorf("frozen exchange: %v", failures)
					}
					after := snapshot()
					for id, want := range before {
						if !bytes.Equal(want, after[id]) {
							t.Error("account/profile/API-key/settings rows changed during a probe read")
						}
					}
				})
			}
		}
	}
	if err := requiredProbeResults(results); err != nil {
		t.Error(err)
	}
	if requests != 7 || effects != 14 || outages != 4 || live != 3 {
		t.Errorf("retained probe evidence %dHTTP/%dPG/%d outage/%d live, want 7/14/4/3", requests, effects, outages, live)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// requiredProbeResults is requiredPairedResults for a v1-only family: every
// required scenario exactly once, passed, on the v1 transport.
func requiredProbeResults(results []Result) error {
	want := make(map[string]bool)
	for _, id := range scenariocatalog.RequiredRetainedProbeScenarios {
		want[id+"/v1"] = false
	}
	for _, result := range results {
		key := result.Scenario + "/" + result.Transport
		seen, ok := want[key]
		if !ok || seen {
			return errors.New("unexpected or duplicate required exchange " + key)
		}
		if !result.Passed() {
			return errors.New("required " + key + " did not pass: " + result.Skipped + " " + joinFailures(result.Failures))
		}
		want[key] = true
	}
	for key, seen := range want {
		if !seen {
			return errors.New("required exchange " + key + " missing")
		}
	}
	return nil
}

func joinFailures(failures []string) string {
	out := ""
	for _, f := range failures {
		out += f + "; "
	}
	return out
}

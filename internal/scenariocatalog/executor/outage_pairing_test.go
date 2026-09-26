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

// TestRequiredOutageAcceptance pairs the three frozen public reads whose
// original oracle is recorded under an unreachable database. The outage is
// the executor's existing offline router: its pool targets 127.0.0.1:1, a
// port nothing listens on, so every query fails with a refused connection
// while the routes stay registered. Nothing is stopped, paused or killed, and
// no shared resource is involved; the state is reproduced identically on
// every run. Before each exchange the runner proves the outage (a pool ping
// fails with a network error) and snapshots the owned live database; after
// it the snapshot must be unchanged, showing the outage router reaches no
// stored row. Each transport reseeds independently, as the sibling runners
// do, even though the outage router cannot touch the seeded household.
func TestRequiredOutageAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-outage for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.OutageAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, effects, outages := 0, 0, 0
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
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							return accountSnapshot(t, e)
						}
						// The outage must be real before the exchange: the offline
						// pool cannot reach its target.
						var netErr net.Error
						if err := e.offlinePing(); err == nil || !errors.As(err, &netErr) {
							t.Fatalf("offline pool reached a database (%v); the outage state is not reproduced", err)
						}
						outages++
						before := snapshot()
						if len(before) != 6 {
							t.Fatal("snapshot must contain six full tables")
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
						_, failures, err := e.exchange(e.offline.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						after := snapshot()
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/login-session/device-request rows changed during an outage read")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredOutageScenarios); err != nil {
		t.Error(err)
	}
	if requests != 6 || effects != 12 || outages != 6 {
		t.Errorf("paired outage evidence %dHTTP/%dPG/%d outage proofs, want6/12/6", requests, effects, outages)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

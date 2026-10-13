package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredDeviceLookupAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-device-lookup for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.DeviceLookupAcceptance(catalogs)
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
							return accountSnapshot(t, e)
						}
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
							t.Error("unexpected snapshot table count")
						}
						// The frozen v1 lookup is a pure read. A v2 lookup of the
						// fixture's pending request records opened_at and may
						// extend expires_at on that one row; nothing else changes.
						for id, want := range before {
							if id == "device_requests" && transport == "v2" {
								assertDeviceRequestsOpened(t, want, after[id], 1)
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/login-session/device-request rows changed during device lookup read")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceLookupScenarios); err != nil {
		t.Error(err)
	}
	if requests != 8 || effects != 16 {
		t.Errorf("paired device lookup evidence %dHTTP/%dPG, want8/16", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// assertDeviceRequestsOpened compares two device_login_requests snapshots
// taken around a v2 lookup. Exactly wantOpened rows may differ, and each must
// be a pending, unopened request that the lookup marked opened.
func assertDeviceRequestsOpened(t *testing.T, before, after json.RawMessage, wantOpened int) {
	t.Helper()
	var old, rows []map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &rows); err != nil {
		t.Fatal(err)
	}
	if len(old) != len(rows) {
		t.Fatal("device requests added or removed during lookup")
	}
	known := map[any]map[string]any{}
	for _, r := range old {
		known[r["id"]] = r
	}
	opened := 0
	for _, r := range rows {
		prev, ok := known[r["id"]]
		if !ok {
			t.Fatal("device request replaced during lookup")
		}
		if reflect.DeepEqual(prev, r) {
			continue
		}
		opened++
		assertDeviceLookupOpened(t, prev, r)
	}
	if opened != wantOpened {
		t.Errorf("lookup opened %d device requests, want %d", opened, wantOpened)
	}
}

// assertDeviceLookupOpened checks the only write a v2 lookup makes: a pending,
// unopened request gains opened_at (with updated_at from the same statement)
// and an expiry held at least five minutes past the opening. Every other
// column, including status and every code hash, is unchanged.
func assertDeviceLookupOpened(t *testing.T, before, after map[string]any) {
	t.Helper()
	instant := func(row map[string]any, key string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339Nano, fmt.Sprint(row[key]))
		if err != nil {
			t.Fatalf("device request %s: %v", key, err)
		}
		return v
	}
	if before["status"] != "pending" || before["opened_at"] != nil {
		t.Error("lookup changed a request that was not pending and unopened")
	}
	openedAt := instant(after, "opened_at")
	if !instant(after, "updated_at").Equal(openedAt) {
		t.Error("opened_at and updated_at differ")
	}
	expiry := instant(after, "expires_at")
	if expiry.Before(instant(before, "expires_at")) || expiry.Before(openedAt.Add(5*time.Minute)) {
		t.Error("lookup shortened the pending request's expiry")
	}
	if len(before) != len(after) {
		t.Error("device request columns changed")
	}
	for key, want := range before {
		switch key {
		case "opened_at", "updated_at", "expires_at":
			continue
		}
		if !reflect.DeepEqual(want, after[key]) {
			t.Errorf("lookup changed device request %s", key)
		}
	}
}

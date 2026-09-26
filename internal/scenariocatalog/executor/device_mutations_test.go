package executor

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Only the mutation gate installs this overlay. Each transport starts with
// independent populated device settings; default/list fixtures remain unchanged.
func deviceMutationOverlay(t *testing.T, e *Env) {
	t.Helper()
	store, err := e.stores.ForUser(e.ctx, e.users[fixtureMember].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ profile, device string }{{profilePrimary, deviceIDA}, {profilePrimary, deviceIDB}, {profileSecondary, "fixture-device-c"}} {
		if _, err := store.UpsertSettingValue(e.ctx, userstore.SettingIdentity{Key: "theme", Scope: settingscontract.ScopeProfileDevice, ProfileID: target.profile, DeviceID: target.device}, json.RawMessage(`"dark"`)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequiredDeviceMutationAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-device-mutations for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pilot, err := scenariocatalog.DeviceMutationAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	e := New(t)
	e.afterReseed = func() { deviceMutationOverlay(t, e) }
	defer func() {
		e.afterReseed = nil
		e.Reseed()
		var count int
		if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM user_setting_values WHERE scope='profile_device'`).Scan(&count); err != nil {
			t.Error(err)
		} else if count != 0 {
			t.Errorf("mutation overlay leaked %d settings", count)
		}
	}()
	results := runAll(t, pilot, e)
	if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceMutationScenarios); err != nil {
		t.Error(err)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredDeviceMutationResultsRejectIncompleteEvidence(t *testing.T) {
	for _, kind := range []string{"missing", "skipped", "failed assertion"} {
		t.Run(kind, func(t *testing.T) {
			var results []Result
			for _, id := range scenariocatalog.RequiredDeviceMutationScenarios {
				for _, transport := range []string{"v1", "v2"} {
					results = append(results, Result{Scenario: id, Transport: transport})
				}
			}
			if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceMutationScenarios); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				results = results[1:]
			case "skipped":
				results[0].Skipped = "database unavailable"
			case "failed assertion":
				results[0].Failures = check(scenariocatalog.Expect{Status: 418}, response{Status: 204})
			}
			if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceMutationScenarios); err == nil {
				t.Fatal("incomplete mutation evidence passed")
			}
		})
	}
}

package executor

import (
	"fmt"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredProfileListAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-profile-pairing for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pilot, err := scenariocatalog.ProfileListAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	results := RunAll(t, pilot)
	for _, result := range results {
		if !result.Passed() {
			t.Errorf("required %s/%s did not pass: skipped=%q failures=%v", result.Scenario, result.Transport, result.Skipped, result.Failures)
		}
	}
	if len(results) != 2*len(scenariocatalog.RequiredProfileListScenarios) {
		t.Fatalf("required paired exchanges = %d, want %d", len(results), 2*len(scenariocatalog.RequiredProfileListScenarios))
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredDeviceListAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-device-pairing for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pilot, err := scenariocatalog.DeviceListAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	results := RunAll(t, pilot)
	if err := requiredDeviceResults(results); err != nil {
		t.Error(err)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func requiredDeviceResults(results []Result) error {
	return requiredPairedResults(results, scenariocatalog.RequiredDeviceListScenarios)
}

func requiredPairedResults(results []Result, required []string) error {
	want := make(map[string]bool)
	for _, id := range required {
		for _, transport := range []string{"v1", "v2"} {
			want[id+"/"+transport] = false
		}
	}
	for _, result := range results {
		key := result.Scenario + "/" + result.Transport
		seen, ok := want[key]
		if !ok || seen {
			return fmt.Errorf("unexpected or duplicate required exchange %s", key)
		}
		if !result.Passed() {
			return fmt.Errorf("required %s did not pass: skipped=%q failures=%v", key, result.Skipped, result.Failures)
		}
		want[key] = true
	}
	for key, seen := range want {
		if !seen {
			return fmt.Errorf("required exchange %s missing", key)
		}
	}
	return nil
}

func TestRequiredDeviceResultsRejectIncompleteEvidence(t *testing.T) {
	complete := func() []Result {
		var results []Result
		for _, id := range scenariocatalog.RequiredDeviceListScenarios {
			for _, transport := range []string{"v1", "v2"} {
				results = append(results, Result{Scenario: id, Transport: transport})
			}
		}
		return results
	}
	if err := requiredDeviceResults(complete()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "duplicate", "unknown", "skipped", "failed assertion"} {
		t.Run(kind, func(t *testing.T) {
			results := complete()
			switch kind {
			case "missing":
				results = results[1:]
			case "duplicate":
				results[0] = results[1]
			case "unknown":
				results[0].Scenario = "unknown"
			case "skipped":
				results[0].Skipped = "database unavailable"
			case "failed assertion":
				results[0].Failures = check(scenariocatalog.Expect{Status: 418}, response{Status: 200})
			}
			if err := requiredDeviceResults(results); err == nil {
				t.Fatal("incomplete evidence passed")
			}
		})
	}
}

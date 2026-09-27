package executor

import (
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredSectionReadAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-section-reads for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.SectionReadAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	results := RunAll(t, selected)
	if err := requiredPairedResults(results, scenariocatalog.RequiredSectionReadScenarios); err != nil {
		t.Error(err)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

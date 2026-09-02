package executor

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestScenarioCatalogs runs every scenario against the real router. Public
// and database-unavailable scenarios run in plain CI; the rest run when
// SILO_TEST_DATABASE_URL points at a scratch database and skip otherwise.
func TestScenarioCatalogs(t *testing.T) {
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	results := RunAll(t, catalogs)
	var passed, skipped, failed int
	for _, r := range results {
		switch {
		case r.Skipped != "":
			skipped++
		case len(r.Failures) > 0:
			failed++
		default:
			passed++
		}
	}
	t.Logf("scenarios: passed=%d failed=%d skipped=%d", passed, failed, skipped)
	if err := WriteReport(results); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

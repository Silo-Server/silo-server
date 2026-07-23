package catalog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEffectiveLastAirDateExpr_ReadsDenormColumn(t *testing.T) {
	expr := effectiveLastAirDateExpr("mi")
	if strings.Contains(expr, "SELECT MAX(") {
		t.Fatalf("effectiveLastAirDateExpr must NOT contain a correlated subquery; got %s", expr)
	}
	if !strings.Contains(expr, "mi.last_air_date_at") {
		t.Fatalf("effectiveLastAirDateExpr must read mi.last_air_date_at; got %s", expr)
	}
}

func TestEffectiveLastAirDateExpr_NonSeriesFallsBackToFirstAirDate(t *testing.T) {
	expr := effectiveLastAirDateExpr("mi")
	// For non-series rows, the ELSE branch returns first_air_date (trimmed).
	// This is a deliberate change from the pre-migration-103 behavior, where
	// it returned the stored mi.last_air_date text column. Documented here so
	// a future "fix" doesn't silently restore the old fallback.
	if !strings.Contains(expr, "ELSE NULLIF(BTRIM(mi.first_air_date), '') END") {
		t.Fatalf("non-series fallback must be NULLIF(BTRIM(mi.first_air_date), ''); got %s", expr)
	}
	if strings.Contains(expr, "ELSE NULLIF(BTRIM(mi.last_air_date), '') END") {
		t.Fatalf("non-series fallback should no longer read mi.last_air_date; got %s", expr)
	}
}

// TestNoEpisodeDeletePath_ProtectsLastAirDateDenorm pins the invariant that
// no code path deletes from `episodes`. The denormalized
// media_items.last_air_date_at column is currently maintained only on
// Upsert/BulkUpsert (see updateSeriesLastAirDateSQL / batchUpdateSeriesLastAirDateSQL).
// If a future PR adds an episode-delete code path, last_air_date_at will
// drift unless that path also recomputes the parent series' value.
//
// When this test fires, fix it the right way:
//  1. Add the maintenance call (an UPDATE recomputing MAX(air_date)) on the
//     new delete path, OR a DB trigger that fires on episodes DELETE.
//  2. Then update knownEpisodeDeleteSites below to allow the new site.
//
// Do NOT silence the test by adding the new site to the allowlist without
// also fixing the maintenance.
func TestNoEpisodeDeletePath_ProtectsLastAirDateDenorm(t *testing.T) {
	// Sites that may legitimately mention "DELETE FROM episodes" without
	// breaking the invariant — e.g., schema migrations that drop and
	// recreate the table, or tests asserting the absence itself.
	knownEpisodeDeleteSites := map[string]bool{
		"item_repo.go":                       true,
		"virtual_media_integration_test.go": true,
	}

	// Match `DELETE FROM episodes` (with optional schema qualifier and
	// case-insensitive). Word-boundary on the table name prevents false
	// matches on episodes_libraries / episode_libraries / episode_targets.
	deletePattern := regexp.MustCompile(`(?i)DELETE\s+FROM\s+(?:public\.)?episodes\b`)

	roots := []string{
		"../..", // walk repo root from internal/catalog
	}
	var hits []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable
			}
			if info.IsDir() {
				// Skip vendor, node_modules, build artifacts, and the
				// frontend tree — none should host server-side SQL.
				name := info.Name()
				if name == "vendor" || name == "node_modules" || name == ".git" ||
					name == "web" || name == "dist" || name == "build" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".go" && ext != ".sql" {
				return nil
			}
			// Don't match this file itself (it contains the regex literal).
			if strings.HasSuffix(path, "last_air_date_denorm_test.go") {
				return nil
			}
			rel := filepath.ToSlash(path)
			if knownEpisodeDeleteSites[filepath.Base(rel)] {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			if deletePattern.Match(data) {
				hits = append(hits, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("found DELETE FROM episodes in %d file(s): %v\n"+
			"Episode deletes can drift media_items.last_air_date_at. "+
			"See test comment for the correct fix.", len(hits), hits)
	}
}

func TestLastAirDateSortPlan_NoDerivedTableScan(t *testing.T) {
	plan, err := NewQueryBuilder("mi").BuildSortPlan(QuerySort{
		Field: "last_air_date",
		Order: "desc",
	})
	if err != nil {
		t.Fatalf("BuildSortPlan error: %v", err)
	}
	if len(plan.Joins) != 0 {
		t.Fatalf("last_air_date sort must not use derived-table JOIN; got joins=%v", plan.Joins)
	}
	if !strings.Contains(plan.OrderBy, "mi.last_air_date_at") {
		t.Fatalf("expected ORDER BY mi.last_air_date_at; got %s", plan.OrderBy)
	}
	if !strings.Contains(plan.OrderBy, "NULLS LAST") {
		t.Fatalf("expected NULLS LAST; got %s", plan.OrderBy)
	}
}

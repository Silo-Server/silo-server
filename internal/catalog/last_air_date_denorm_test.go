package catalog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoEpisodeDeletePath_ProtectsLastAirDateDenorm pins the invariant that
// no production code path deletes from `episodes`. The denormalized
// media_items.last_air_date_at column is maintained on Upsert/BulkUpsert and
// by the due-series sweep (see refreshSeriesAirDatesSQL).
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
	// recreate the table. Go test files are excluded below.
	knownEpisodeDeleteSites := map[string]bool{
		// Migration 001 owns the initial schema; subsequent migrations may
		// rebuild the table. Add specific migration filenames here.
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
				return nil //nolint:nilerr // skip unreadable paths in the checkout instead of aborting the source scan
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
			// Go tests may delete their isolated fixtures. Keep every production
			// Go file and SQL file in scope, including scenario executor code.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel := filepath.ToSlash(path)
			if knownEpisodeDeleteSites[filepath.Base(rel)] {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil //nolint:nilerr // skip unreadable files, as the walk does for unreadable paths
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

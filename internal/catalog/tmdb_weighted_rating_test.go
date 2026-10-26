package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The weighted-rating index only serves discovery rows while its expression is
// the one the queries order by.
func TestTMDBWeightedRatingMatchesItsIndex(t *testing.T) {
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "sql", "20261008200123_index_tmdb_weighted_rating.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	expr := strings.ReplaceAll(TMDBWeightedRatingSQL("x"), "x.", "")
	if !strings.Contains(string(migration), "("+expr+" DESC NULLS LAST, content_id)") {
		t.Fatalf("migration does not index %s DESC NULLS LAST, content_id", expr)
	}
	if !strings.Contains(DiscoveryRatingOrder, TMDBWeightedRatingSQL("mi")+" DESC NULLS LAST, mi.content_id ASC") {
		t.Fatalf("DiscoveryRatingOrder = %q does not match the index order", DiscoveryRatingOrder)
	}
}

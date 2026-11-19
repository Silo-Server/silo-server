package recommendations

import (
	"slices"
	"testing"
)

// The profile editor writes bare ages for ceilings outside the US ladder, so a
// peer carrying "12" must be a neighbor of a PG-13 viewer, and a peer carrying
// "16" must not be.
func TestCompatiblePeerContentRatingsComparesAgeCeilings(t *testing.T) {
	ratings := compatiblePeerContentRatings("PG-13")
	if !slices.Contains(ratings, "12") {
		t.Fatalf("expected age ceiling 12 in compatible ratings: %#v", ratings)
	}
	if slices.Contains(ratings, "16") {
		t.Fatalf("did not expect age ceiling 16 in compatible ratings: %#v", ratings)
	}

	ratings = compatiblePeerContentRatings("15")
	if !slices.Contains(ratings, "PG-13") || slices.Contains(ratings, "R") {
		t.Fatalf("a 15 ceiling must admit PG-13 peers and exclude R peers: %#v", ratings)
	}
}

func clusterTestItem(id string, embedding []float32, weight float64, genre string) clusterItem {
	return clusterItem{
		itemID:    id,
		embedding: embedding,
		weight:    weight,
		genres:    []string{genre},
	}
}

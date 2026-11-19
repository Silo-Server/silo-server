package recommendations

import (
	"slices"
	"testing"
)

// Two households of near-identical profiles at the top of the similarity
// order cannot fill the peers: each account keeps at most
// similarUsersPeersPerAccount, so a third account below them still makes the
// floor. The order and the limit hold.
func TestPeersAcrossAccountsSpreadsHouseholds(t *testing.T) {
	var candidates []UserSimilarity
	for _, account := range []int{2, 3} {
		for _, p := range []string{"a", "b", "c", "d", "e"} {
			candidates = append(candidates, UserSimilarity{UserID: account, ProfileID: p})
		}
	}
	candidates = append(candidates, UserSimilarity{UserID: 4, ProfileID: "a"}, UserSimilarity{UserID: 5, ProfileID: "a"})

	peers := peersAcrossAccounts(candidates, 5, 2)
	want := []UserSimilarity{{UserID: 2, ProfileID: "a"}, {UserID: 2, ProfileID: "b"}, {UserID: 3, ProfileID: "a"}, {UserID: 3, ProfileID: "b"}, {UserID: 4, ProfileID: "a"}}
	if !slices.Equal(peers, want) {
		t.Fatalf("peers = %v, want %v", peers, want)
	}
	if got := distinctAccounts(peers); got < minSimilarUsersPeerAccounts {
		t.Fatalf("peers span %d accounts, under the floor of %d", got, minSimilarUsersPeerAccounts)
	}
	// The first ten candidates, the old read, span only two accounts.
	if got := distinctAccounts(candidates[:10]); got >= minSimilarUsersPeerAccounts {
		t.Fatalf("fixture: the first ten candidates span %d accounts", got)
	}
}

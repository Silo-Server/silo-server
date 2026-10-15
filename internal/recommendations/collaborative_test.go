package recommendations

import (
	"maps"
	"slices"
	"testing"
)

// Peers are profiles; the floor counts the accounts behind them, so one
// household with many profiles cannot carry the row.
func TestSimilarUsersPeerFloorCountsAccounts(t *testing.T) {
	household := []UserSimilarity{{UserID: 2, ProfileID: "a"}, {UserID: 2, ProfileID: "b"}, {UserID: 2, ProfileID: "c"}, {UserID: 3, ProfileID: "a"}}
	if got := distinctAccounts(household); got != 2 || got >= minSimilarUsersPeerAccounts {
		t.Fatalf("distinctAccounts = %d, want 2, below the floor of %d", got, minSimilarUsersPeerAccounts)
	}
	three := append(household, UserSimilarity{UserID: 4, ProfileID: "a"})
	if got := distinctAccounts(three); got != minSimilarUsersPeerAccounts {
		t.Fatalf("distinctAccounts = %d, want %d", got, minSimilarUsersPeerAccounts)
	}
}

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

// A title one account liked, however many of its profiles liked it, never
// reaches the row; one liked on two accounts does.
func TestCollaborativeCandidatesNeedTwoAccounts(t *testing.T) {
	candidates := collaborativeCandidates([]peerLikes{
		{userID: 2, similarity: 0.9, weights: map[string]float64{"one-household": 1, "shared": 0.7}},
		{userID: 2, similarity: 0.8, weights: map[string]float64{"one-household": 1}},
		{userID: 3, similarity: 0.7, weights: map[string]float64{"shared": 1, "single": 1}},
		{userID: 4, similarity: 0.6, weights: map[string]float64{"other-single": 1}},
	})

	got := slices.Sorted(maps.Keys(candidates))
	if !slices.Equal(got, []string{"shared"}) {
		t.Fatalf("candidates = %v, want only the title two accounts liked", got)
	}
}

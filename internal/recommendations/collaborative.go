package recommendations

import (
	"context"
	"fmt"
	"sort"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// The Similar Users row is built from other accounts' ratings and favorites,
// so it is a privacy boundary as much as a ranking: with too few accounts
// behind it, the row would show what one other household liked. Like the
// co-watch minimum (itemWatchersQuery), both floors count distinct login
// accounts, never profiles, so one household's profiles cannot meet them.
const (
	// minSimilarUsersPeerAccounts is how many other accounts the similar
	// profiles must span before the row is built at all.
	minSimilarUsersPeerAccounts = 3
	// minSimilarUsersItemAccounts is how many of those accounts must have
	// liked a title before the row may recommend it.
	minSimilarUsersItemAccounts = 2
)

type collaborativeCandidate struct {
	score float64
	// accounts holds the accounts whose profiles liked the title.
	accounts map[int]struct{}
}

// peerLikes is one similar profile's liked titles and their signal weights.
type peerLikes struct {
	userID     int
	similarity float64
	weights    map[string]float64
}

// distinctAccounts counts the accounts the similar profiles belong to.
func distinctAccounts(peers []UserSimilarity) int {
	accounts := make(map[int]struct{}, len(peers))
	for _, p := range peers {
		accounts[p.UserID] = struct{}{}
	}
	return len(accounts)
}

// collaborativeCandidates sums each liked title's similarity-weighted score
// over the peers and keeps the titles liked on at least
// minSimilarUsersItemAccounts accounts.
func collaborativeCandidates(peers []peerLikes) map[string]collaborativeCandidate {
	candidates := make(map[string]collaborativeCandidate)
	for _, peer := range peers {
		for itemID, weight := range peer.weights {
			candidate := candidates[itemID]
			if candidate.accounts == nil {
				candidate.accounts = make(map[int]struct{}, 1)
			}
			candidate.score += peer.similarity * weight
			candidate.accounts[peer.userID] = struct{}{}
			candidates[itemID] = candidate
		}
	}
	for itemID, candidate := range candidates {
		if len(candidate.accounts) < minSimilarUsersItemAccounts {
			delete(candidates, itemID)
		}
	}
	return candidates
}

// SimilarUsersLiked returns items highly rated or favorited by users with
// similar taste profiles. Scores are weighted by the similarity of each peer
// user to the requesting user. Items the target profile already rated, those
// in its recommendation exclusion set, and those filter does not admit are
// filtered out before ranking. Applies MMR re-ranking for diversity. Below
// the account floors it returns an empty list.
func (e *Engine) SimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	excluded, err := e.recommendationExclusionSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get recommendation exclusions for user %d profile %s: %w", userID, profileID, err)
	}
	return e.similarUsersLiked(ctx, userID, profileID, limit, excluded, filter)
}

// similarUsersLiked is SimilarUsersLiked with the exclusion set given. It
// answers an empty, non-nil list when the row has nothing to show, so a
// cached row records that it was built.
func (e *Engine) similarUsersLiked(ctx context.Context, userID int, profileID string, limit int, excluded map[string]struct{}, filter catalog.AccessFilter) ([]ScoredItem, error) {
	meta, err := e.repo.GetTasteProfileMeta(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste profile meta for user %d profile %s: %w", userID, profileID, err)
	}
	maxContentRating := ""
	if meta != nil {
		maxContentRating = meta.MaxContentRating
	}

	similarUsers, err := e.repo.FindSimilarUsers(ctx, userID, profileID, maxContentRating, 10)
	if err != nil {
		return nil, fmt.Errorf("find similar users for user %d profile %s: %w", userID, profileID, err)
	}
	if distinctAccounts(similarUsers) < minSimilarUsersPeerAccounts {
		return []ScoredItem{}, nil
	}

	peers := make([]peerLikes, 0, len(similarUsers))
	for _, su := range similarUsers {
		peerWeights := make(map[string]float64)

		// Collect highly-rated items (4–5 stars) from this similar user.
		ratings, err := e.ratingsRepo.List(ctx, su.UserID, su.ProfileID, 100, 0)
		if err != nil {
			return nil, fmt.Errorf("list ratings for similar user %d profile %s: %w", su.UserID, su.ProfileID, err)
		}

		for _, r := range ratings {
			var weight float64
			switch {
			case r.Rating == 5:
				weight = WeightRated5
			case r.Rating == 4:
				weight = WeightRated4
			default:
				continue
			}

			if existing, ok := peerWeights[r.MediaItemID]; !ok || weight > existing {
				peerWeights[r.MediaItemID] = weight
			}
		}

		// Collect favorited items from this similar user.
		store, err := e.storeProvider.ForUser(ctx, su.UserID)
		if err != nil {
			return nil, fmt.Errorf("get store for similar user %d: %w", su.UserID, err)
		}

		favorites, err := store.ListFavorites(ctx, su.ProfileID, 100, 0)
		if err != nil {
			return nil, fmt.Errorf("list favorites for similar user %d profile %s: %w", su.UserID, su.ProfileID, err)
		}

		for _, f := range favorites {
			if existing, ok := peerWeights[f.MediaItemID]; !ok || WeightFavorited > existing {
				peerWeights[f.MediaItemID] = WeightFavorited
			}
		}

		peers = append(peers, peerLikes{userID: su.UserID, similarity: su.Score, weights: peerWeights})
	}

	candidates := collaborativeCandidates(peers)
	if len(candidates) == 0 {
		return []ScoredItem{}, nil
	}

	// Build list of candidate item IDs for filtering.
	candidateIDs := make([]string, 0, len(candidates))
	for id := range candidates {
		candidateIDs = append(candidateIDs, id)
	}

	// Filter out items the target user has already rated.
	ratedMap, err := e.ratingsRepo.ListForItems(ctx, userID, profileID, candidateIDs)
	if err != nil {
		return nil, fmt.Errorf("list rated items for filtering: %w", err)
	}

	// Leave out what the viewer cannot see before ranking, so those titles
	// do not take the row's places.
	accessible, err := e.repo.FilterAccessibleItemIDs(ctx, candidateIDs, filter)
	if err != nil {
		return nil, fmt.Errorf("filter accessible similar-users candidates: %w", err)
	}

	// Build scored result list, excluding already-rated, excluded and
	// inaccessible items.
	results := make([]ScoredItem, 0, len(candidates))
	supportCounts := make(map[string]int, len(candidates))
	for id, candidate := range candidates {
		if _, rated := ratedMap[id]; rated {
			continue
		}
		if _, ok := accessible[id]; !ok {
			continue
		}
		if _, skip := excluded[id]; skip {
			continue
		}
		supportCounts[id] = len(candidate.accounts)
		results = append(results, ScoredItem{
			MediaItemID: id,
			Score:       candidate.score,
			Reason:      "similar_users_liked",
		})
	}
	if len(results) == 0 {
		return []ScoredItem{}, nil
	}

	// Sort by score descending.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if supportCounts[results[i].MediaItemID] != supportCounts[results[j].MediaItemID] {
			return supportCounts[results[i].MediaItemID] > supportCounts[results[j].MediaItemID]
		}
		return results[i].MediaItemID < results[j].MediaItemID
	})

	// Apply MMR re-ranking for diversity.
	if len(results) > limit*3 {
		results = results[:limit*3]
	}

	resultIDs := make([]string, len(results))
	for i, item := range results {
		resultIDs[i] = item.MediaItemID
	}
	embMap, _ := e.repo.GetBatchEmbeddings(ctx, resultIDs)
	results = applyMMR(results, embMap, e.mmrLambda(), limit)

	// Keep any one genre from taking over the served window.
	genres, _ := e.repo.GetItemAllGenres(ctx, resultIDs)
	return applyGenreCap(results, genres), nil
}

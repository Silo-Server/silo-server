package recommendations

import (
	"context"
	"fmt"
	"sort"
)

type collaborativeCandidate struct {
	score   float64
	support int
}

func addCollaborativeSupport(candidates map[string]collaborativeCandidate, itemID string, score float64) {
	candidate := candidates[itemID]
	candidate.score += score
	candidate.support++
	candidates[itemID] = candidate
}

// SimilarUsersLiked returns items highly rated or favorited by users with
// similar taste profiles. Scores are weighted by the similarity of each peer
// user to the requesting user. Items already rated or watched by the target
// user are filtered out. Applies MMR re-ranking for diversity.
func (e *Engine) SimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int) ([]ScoredItem, error) {
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
	if len(similarUsers) == 0 {
		return nil, nil
	}

	candidates := make(map[string]collaborativeCandidate)

	for _, su := range similarUsers {
		similarity := su.Score
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

		for itemID, weight := range peerWeights {
			addCollaborativeSupport(candidates, itemID, similarity*weight)
		}
	}

	if len(candidates) == 0 {
		return nil, nil
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

	// Build scored result list, excluding already-rated or already-watched items.
	results := make([]ScoredItem, 0, len(candidates))
	supportCounts := make(map[string]int, len(candidates))
	for id, candidate := range candidates {
		if _, rated := ratedMap[id]; rated {
			continue
		}
		supportCounts[id] = candidate.support
		results = append(results, ScoredItem{
			MediaItemID: id,
			Score:       candidate.score,
			Reason:      "similar_users_liked",
		})
	}

	watchedSet, err := e.watchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get watched items for user %d profile %s: %w", userID, profileID, err)
	}
	results = excludeScoredItems(results, watchedSet)

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

	// Apply genre cap to "Similar Users Liked" for cross-genre diversity.
	genres, _ := e.repo.GetItemAllGenres(ctx, resultIDs)
	results = applyGenreCap(results, genres, GenreCapPercent)

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

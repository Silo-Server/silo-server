package recommendations

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
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

	// similarUsersPeerProfiles is how many similar profiles the row reads
	// likes from, and similarUsersPeersPerAccount how many of them one
	// account may hold. similarUsersCandidateProfiles nearest profiles are
	// read so that a household of near-identical profiles cannot fill the
	// peers and leave the row under its account floor.
	similarUsersPeerProfiles      = 10
	similarUsersPeersPerAccount   = 2
	similarUsersCandidateProfiles = 40

	// peerLikesPerKind caps how many of one similar profile's liked ratings,
	// and separately its favorites, the row reads: the most recent, counted
	// after leaving out what the row cannot use, so newer low ratings and
	// episodes do not crowd out older likes.
	peerLikesPerKind = 100
	// peerLikedMinScore is the lowest score that rounds to 4 stars (see
	// catalog.UserRating.Stars), the least a peer's rating must be to count
	// as a like.
	peerLikedMinScore = 7
)

// peerCeiling is the content-rating ceiling a profile's peers are matched
// under (see Repo.FindSimilarUsers): the stricter of its parental controls,
// its content-rating ceiling and its advisory-age limit as a bare age. A
// profile with neither may match any peer, whatever it watched itself. The
// row's titles are filtered by the profile's own access either way; this only
// keeps a restricted profile's neighbors to those whose taste it can share.
func peerCeiling(limits access.MaturityLimits) string {
	ceiling := limits.MaxContentRating
	if limits.MaxAdvisoryAge <= 0 {
		return ceiling
	}
	if access.HasCeiling(ceiling) {
		// An unusable ceiling stays: it matches no rated peer, as it shows
		// the profile no rated title.
		if age, ok := access.AgeForCeiling(ceiling); !ok || *age <= limits.MaxAdvisoryAge {
			return ceiling
		}
	}
	return strconv.Itoa(limits.MaxAdvisoryAge)
}

// peersAcrossAccounts keeps, in similarity order, up to limit profiles with
// at most perAccount from any one account.
func peersAcrossAccounts(candidates []UserSimilarity, limit, perAccount int) []UserSimilarity {
	taken := make(map[int]int, len(candidates))
	peers := make([]UserSimilarity, 0, min(limit, len(candidates)))
	for _, c := range candidates {
		if len(peers) == limit {
			break
		}
		if taken[c.UserID] >= perAccount {
			continue
		}
		taken[c.UserID]++
		peers = append(peers, c)
	}
	return peers
}

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

// peerLikedTitles returns the recommendable titles a similar profile liked and
// their weights: up to peerLikesPerKind of its most recent 4- and 5-star
// ratings and as many of its favorites, a favorite weighing most.
func (e *Engine) peerLikedTitles(ctx context.Context, userID int, profileID string) (map[string]float64, error) {
	weights := make(map[string]float64)
	ratings, err := e.ratingsRepo.ListRatedAtLeast(ctx, userID, profileID, peerLikedMinScore, recommendableMediaTypes, peerLikesPerKind)
	if err != nil {
		return nil, fmt.Errorf("list ratings for similar user %d profile %s: %w", userID, profileID, err)
	}
	for _, r := range ratings {
		weight := WeightRated4
		if r.Stars() == 5 {
			weight = WeightRated5
		}
		weights[r.MediaItemID] = weight
	}

	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get store for similar user %d: %w", userID, err)
	}
	favorites, err := e.recommendableFavorites(ctx, store, profileID, peerLikesPerKind)
	if err != nil {
		return nil, fmt.Errorf("list favorites for similar user %d profile %s: %w", userID, profileID, err)
	}
	for _, id := range favorites {
		weights[id] = max(weights[id], WeightFavorited)
	}
	return weights, nil
}

// recommendableFavorites returns up to limit of the profile's favorites of
// recommendableMediaTypes, newest first. Favorites of other types, such as
// episodes, are left out before the limit rather than taking its places.
func (e *Engine) recommendableFavorites(ctx context.Context, store userstore.UserStore, profileID string, limit int) ([]string, error) {
	var ids []string
	var after *userstore.ListKey
	for {
		page, err := store.ListFavoritesPage(ctx, profileID, after, signalPageSize)
		if err != nil {
			return nil, err
		}
		pageIDs := make([]string, len(page))
		for i, f := range page {
			pageIDs[i] = f.MediaItemID
		}
		recommendable, err := e.repo.FilterRecommendableItemIDs(ctx, pageIDs, catalog.AccessFilter{})
		if err != nil {
			return nil, err
		}
		for _, f := range page {
			if _, ok := recommendable[f.MediaItemID]; !ok {
				continue
			}
			if ids = append(ids, f.MediaItemID); len(ids) == limit {
				return ids, nil
			}
		}
		if len(page) < signalPageSize {
			return ids, nil
		}
		last := page[len(page)-1]
		after = &userstore.ListKey{AddedAt: last.AddedAt, MediaItemID: last.MediaItemID}
	}
}

// SimilarUsersLiked returns items highly rated or favorited by users with
// similar taste profiles. Scores are weighted by the similarity of each peer
// user to the requesting user. Items the target profile already rated, those
// in its recommendation exclusion set, those filter does not admit and those
// not of recommendableMediaTypes are filtered out before ranking, which also
// ranks against the profile's turned-away titles. Applies MMR re-ranking for
// diversity. Below the account floors it returns an empty list.
func (e *Engine) SimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	excluded, err := e.recommendationExclusionSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get recommendation exclusions for user %d profile %s: %w", userID, profileID, err)
	}
	builder := e.rowBuilder()
	if builder.aversion, err = e.loadAversion(ctx, userID, profileID, builder.now); err != nil {
		return nil, fmt.Errorf("get turned-away titles for user %d profile %s: %w", userID, profileID, err)
	}
	return e.similarUsersLiked(ctx, builder, userID, profileID, limit, excluded, filter)
}

// similarUsersLiked is SimilarUsersLiked with the exclusion set given, ranked
// by builder, whose aversion it applies around the profile's taste vector. It
// answers an empty, non-nil list when the row has nothing to show, so a
// cached row records that it was built.
func (e *Engine) similarUsersLiked(ctx context.Context, builder rowBuilder, userID int, profileID string, limit int, excluded map[string]struct{}, filter catalog.AccessFilter) ([]ScoredItem, error) {
	nearest, err := e.repo.FindSimilarUsers(ctx, userID, profileID, peerCeiling(filter.MaturityLimits), similarUsersCandidateProfiles)
	if err != nil {
		return nil, fmt.Errorf("find similar users for user %d profile %s: %w", userID, profileID, err)
	}
	similarUsers := peersAcrossAccounts(nearest, similarUsersPeerProfiles, similarUsersPeersPerAccount)
	if distinctAccounts(similarUsers) < minSimilarUsersPeerAccounts {
		return []ScoredItem{}, nil
	}

	peers := make([]peerLikes, 0, len(similarUsers))
	for _, su := range similarUsers {
		weights, err := e.peerLikedTitles(ctx, su.UserID, su.ProfileID)
		if err != nil {
			return nil, err
		}
		peers = append(peers, peerLikes{userID: su.UserID, similarity: su.Score, weights: weights})
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

	// Leave out what the viewer cannot see, and titles not of
	// recommendableMediaTypes, before ranking, so those titles do not take
	// the row's places.
	accessible, err := e.repo.FilterRecommendableItemIDs(ctx, candidateIDs, filter)
	if err != nil {
		return nil, fmt.Errorf("filter accessible similar-users candidates: %w", err)
	}

	// Build scored result list, excluding already-rated, excluded,
	// inaccessible and unrecommendable items.
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
	embMap, err := e.repo.GetBatchEmbeddings(ctx, resultIDs)
	if err != nil {
		if !builder.aversion.empty() {
			// Without embeddings the row could not rank against the
			// profile's turned-away titles.
			return nil, fmt.Errorf("similar users embeddings: %w", err)
		}
		embMap = nil
	}
	if !builder.aversion.empty() {
		taste, err := e.repo.GetTasteProfile(ctx, userID, profileID)
		if err != nil {
			return nil, fmt.Errorf("get taste profile for user %d profile %s: %w", userID, profileID, err)
		}
		results, _ = builder.withAversion(results, embMap, taste)
	}
	results = applyMMR(results, embMap, builder.lambda, limit)

	// Keep any one genre from taking over the served window.
	genres, _ := e.repo.GetItemAllGenres(ctx, resultIDs)
	return applyGenreCap(results, genres), nil
}

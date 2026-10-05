package recommendations

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const signalPageSize = 1000

type signalRepo interface {
	GetWatchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error)
	GetFavoriteItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	GetWatchProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error)
	GetEbookReaderProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error)
	GetRecentCompletedItemIDs(ctx context.Context, userID int, profileID string, limit int) ([]string, error)
	GetRewatchCounts(ctx context.Context, userID int, profileID string) ([]RewatchCount, error)
	ResolveCanonicalItemIDs(ctx context.Context, contentIDs []string) (map[string]string, error)
	ResolveCanonicalItemIDSet(ctx context.Context, contentIDs []string) (map[string]struct{}, error)
	RecommendableItemIDs(ctx context.Context, itemIDs []string) (map[string]struct{}, error)
	HasSignalRows(ctx context.Context, userID int, profileID string, includeStoreTables bool) (bool, error)
}

// SignalReader centralizes profile-scoped recommendation signals. userstore is
// the source of truth when configured; repo SQL is retained for deployments
// without a store provider.
type SignalReader struct {
	repo          signalRepo
	storeProvider userstore.UserStoreProvider
	// storeOutsidePostgres marks a user store that keeps watch progress,
	// favorites and watchlist outside the Postgres tables (the SQLite
	// backend), so checks that would query those tables ask the store.
	storeOutsidePostgres bool
}

func NewSignalReader(repo signalRepo, storeProvider userstore.UserStoreProvider) *SignalReader {
	return &SignalReader{
		repo:          repo,
		storeProvider: storeProvider,
	}
}

func (s *SignalReader) storeForUser(ctx context.Context, userID int) (userstore.UserStore, bool, error) {
	if s == nil || s.storeProvider == nil {
		return nil, false, nil
	}

	store, err := s.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, false, fmt.Errorf("open user store for user %d: %w", userID, err)
	}
	if store == nil {
		return nil, false, nil
	}
	return store, true, nil
}

// storeIsSeparate reports whether the profile signals the user store holds
// live outside the Postgres tables.
func (s *SignalReader) storeIsSeparate() bool {
	return s != nil && s.storeProvider != nil && s.storeOutsidePostgres
}

// HasSignals reports whether the profile has anything a taste profile is
// built from: a rating, favorite, watchlist entry, or watch or reading
// progress. It checks existence only, so it stays cheap on every read.
func (s *SignalReader) HasSignals(ctx context.Context, userID int, profileID string) (bool, error) {
	separate := s.storeIsSeparate()
	found, err := s.repo.HasSignalRows(ctx, userID, profileID, !separate)
	if err != nil || found || !separate {
		return found, err
	}
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil || !ok {
		return false, err
	}
	favorites, err := store.ListFavorites(ctx, profileID, 1, 0)
	if err != nil {
		return false, fmt.Errorf("list favorites from store: %w", err)
	}
	if len(favorites) > 0 {
		return true, nil
	}
	watchlist, err := store.ListWatchlist(ctx, profileID, 1, 0)
	if err != nil {
		return false, fmt.Errorf("list watchlist from store: %w", err)
	}
	if len(watchlist) > 0 {
		return true, nil
	}
	progress, err := store.ListProgress(ctx, profileID, "all", 1, 0)
	if err != nil {
		return false, fmt.Errorf("list progress from store: %w", err)
	}
	return len(progress) > 0, nil
}

// watchedSetMemoKey keys the context value WithWatchedSetMemo installs.
type watchedSetMemoKey struct{}

// watchedSetMemo holds the watched sets read under one context, by account
// and profile.
type watchedSetMemo struct {
	mu   sync.Mutex
	sets map[watchedSetMemoProfile]map[string]struct{}
}

type watchedSetMemoProfile struct {
	userID    int
	profileID string
}

// WithWatchedSetMemo returns ctx carrying a memo for WatchedItemIDSet: under
// it, a profile's watched set is read once however many SignalReaders ask,
// so one request that filters rows and then blends airings walks the
// profile's history once. A set read through the memo is shared and must not
// be modified.
func WithWatchedSetMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, watchedSetMemoKey{}, &watchedSetMemo{sets: map[watchedSetMemoProfile]map[string]struct{}{}})
}

// WatchedItemIDSet returns the canonical IDs of the titles the profile has
// watched: progress completed or at least half way, episodes counting for
// their series, plus finished ebooks. Under WithWatchedSetMemo it reuses the
// set read earlier for the profile.
func (s *SignalReader) WatchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	memo, _ := ctx.Value(watchedSetMemoKey{}).(*watchedSetMemo)
	if memo == nil {
		return s.readWatchedItemIDSet(ctx, userID, profileID)
	}
	key := watchedSetMemoProfile{userID, profileID}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if set, ok := memo.sets[key]; ok {
		return set, nil
	}
	set, err := s.readWatchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	memo.sets[key] = set
	return set, nil
}

func (s *SignalReader) readWatchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetWatchedItemIDSet(ctx, userID, profileID)
	}

	rawIDs := make([]string, 0, signalPageSize)
	if err := pageProgress(ctx, store, profileID, "all", func(progress []userstore.WatchProgress) error {
		for _, wp := range progress {
			if wp.Completed || watchedProgressThresholdMet(wp.PositionSeconds, wp.DurationSeconds) {
				rawIDs = append(rawIDs, wp.MediaItemID)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	for _, wp := range ebookProgress {
		if wp.Completed || watchedProgressThresholdMet(wp.PositionSeconds, wp.DurationSeconds) {
			rawIDs = append(rawIDs, wp.MediaItemID)
		}
	}

	return s.repo.ResolveCanonicalItemIDSet(ctx, rawIDs)
}

// RecommendationExclusionSet returns the canonical IDs the profile's
// recommendations leave out: the watched set and the profile's favorites,
// which include its taste-seed picks. A favorited episode excludes its series,
// as a watched one does. Watchlist titles stay recommendable.
func (s *SignalReader) RecommendationExclusionSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	excluded, err := s.WatchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	favoriteIDs, err := s.favoriteItemIDs(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	favorites, err := s.repo.ResolveCanonicalItemIDSet(ctx, favoriteIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve favorite item IDs: %w", err)
	}
	merged := make(map[string]struct{}, len(excluded)+len(favorites))
	for id := range excluded {
		merged[id] = struct{}{}
	}
	for id := range favorites {
		merged[id] = struct{}{}
	}
	return merged, nil
}

// favoriteItemIDs lists every content ID the profile has favorited.
func (s *SignalReader) favoriteItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetFavoriteItemIDs(ctx, userID, profileID)
	}

	var ids []string
	var after *userstore.ListKey
	for {
		page, err := store.ListFavoritesPage(ctx, profileID, after, signalPageSize)
		if err != nil {
			return nil, fmt.Errorf("list favorites from store: %w", err)
		}
		for _, f := range page {
			ids = append(ids, f.MediaItemID)
		}
		if len(page) < signalPageSize {
			return ids, nil
		}
		last := page[len(page)-1]
		after = &userstore.ListKey{AddedAt: last.AddedAt, MediaItemID: last.MediaItemID}
	}
}

func (s *SignalReader) WatchProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetWatchProgressForUser(ctx, userID, profileID)
	}

	rows := make([]WatchProgressRow, 0, signalPageSize)
	if err := pageProgress(ctx, store, profileID, "all", func(progress []userstore.WatchProgress) error {
		for _, wp := range progress {
			rows = append(rows, WatchProgressRow{
				MediaItemID:     wp.MediaItemID,
				PositionSeconds: wp.PositionSeconds,
				DurationSeconds: wp.DurationSeconds,
				Completed:       wp.Completed,
				UpdatedAt:       parseSignalTime(wp.UpdatedAt, time.Time{}),
			})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	rows = append(rows, ebookProgress...)

	return rows, nil
}

// RecentCompletedItemIDs returns the canonical IDs of the profile's most
// recently completed titles that are still in the catalog and of
// recommendableMediaTypes, newest first. Completions of deleted items and of
// other types, such as a finished audiobook, are skipped, so they never
// become anchors.
func (s *SignalReader) RecentCompletedItemIDs(ctx context.Context, userID int, profileID string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}

	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetRecentCompletedItemIDs(ctx, userID, profileID, limit)
	}

	candidates := make([]WatchProgressRow, 0, limit)
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	for _, wp := range ebookProgress {
		if wp.Completed {
			candidates = append(candidates, wp)
		}
	}
	candidates, err = liveCompletedRows(ctx, s.repo, candidates)
	if err != nil {
		return nil, fmt.Errorf("resolve recent completed item IDs: %w", err)
	}
	candidates = recentDistinctCompletedRows(candidates, limit)

	var after *userstore.ProgressKey
	for {
		progress, err := store.ListProgressPage(ctx, profileID, "completed", after, signalPageSize)
		if err != nil {
			return nil, fmt.Errorf("list completed progress from store: %w", err)
		}

		page := make([]WatchProgressRow, 0, len(progress))
		oldestPageTime := time.Time{}
		for _, wp := range progress {
			if !wp.Completed {
				continue
			}
			updatedAt := parseSignalTime(wp.UpdatedAt, time.Time{})
			page = append(page, WatchProgressRow{
				MediaItemID: wp.MediaItemID,
				Completed:   true,
				UpdatedAt:   updatedAt,
			})
			if oldestPageTime.IsZero() || updatedAt.Before(oldestPageTime) {
				oldestPageTime = updatedAt
			}
		}
		page, err = liveCompletedRows(ctx, s.repo, page)
		if err != nil {
			return nil, fmt.Errorf("resolve recent completed item IDs: %w", err)
		}
		candidates = recentDistinctCompletedRows(append(candidates, page...), limit)

		if len(progress) < signalPageSize {
			break
		}
		last := progress[len(progress)-1]
		after = &userstore.ProgressKey{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID}
		// Read every page tied at the cutoff: a later leaf can resolve to a
		// canonical ID that sorts ahead of the current last anchor.
		if len(candidates) == limit && !oldestPageTime.IsZero() &&
			oldestPageTime.Before(candidates[len(candidates)-1].UpdatedAt) {
			break
		}
	}

	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.MediaItemID)
	}
	return ids, nil
}

// Because You Watched anchors on the profile's latest completions.
const (
	// BecauseYouWatchedAnchors is how many anchors a profile's Because You
	// Watched rows are built and read for.
	BecauseYouWatchedAnchors = 3
	// anchorCandidateLimit is how many of the latest completions the anchors
	// are chosen from, so disliked ones can be passed over.
	anchorCandidateLimit = 10
)

// itemRatingReader reads a profile's star ratings of the given items, keyed
// by item. *catalog.RatingsRepo implements it.
type itemRatingReader interface {
	ListForItems(ctx context.Context, userID int, profileID string, itemIDs []string) (map[string]int, error)
}

// anchorItemIDs returns up to n Because You Watched anchors for the profile:
// its most recently completed titles of recommendableMediaTypes still in the
// catalog, newest first, of the latest anchorCandidateLimit, leaving out
// those it rated DislikedRatingMax or lower. A row headed "Because You
// Watched" a title the profile disliked contradicts its taste. With no
// ratings reader nothing is left out. The worker, the Reader and Watch
// Tonight all choose anchors here, since a read can only use an anchor whose
// row the worker cached.
func anchorItemIDs(ctx context.Context, signals *SignalReader, ratings itemRatingReader, userID int, profileID string, n int) ([]string, error) {
	if n <= 0 {
		return []string{}, nil
	}
	recent, err := signals.RecentCompletedItemIDs(ctx, userID, profileID, max(n, anchorCandidateLimit))
	if err != nil {
		return nil, err
	}
	if ratings != nil && len(recent) > 0 {
		rated, err := ratings.ListForItems(ctx, userID, profileID, recent)
		if err != nil {
			return nil, fmt.Errorf("read ratings of recent completions: %w", err)
		}
		liked := make([]string, 0, len(recent))
		for _, id := range recent {
			if rating, ok := rated[id]; ok && rating <= DislikedRatingMax {
				continue
			}
			liked = append(liked, id)
		}
		recent = liked
	}
	return recent[:min(n, len(recent))], nil
}

func canonicalizeCompletedRows(ctx context.Context, repo signalRepo, rows []WatchProgressRow) error {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MediaItemID)
	}
	resolved, err := repo.ResolveCanonicalItemIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		if canonicalID := resolved[rows[i].MediaItemID]; canonicalID != "" {
			rows[i].MediaItemID = canonicalID
		}
	}
	return nil
}

// liveCompletedRows canonicalizes rows and drops those whose canonical ID is
// no longer in the catalog (a deleted movie, or an episode whose series was
// deleted and so no longer resolves to it) or is not of
// recommendableMediaTypes.
func liveCompletedRows(ctx context.Context, repo signalRepo, rows []WatchProgressRow) ([]WatchProgressRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	if err := canonicalizeCompletedRows(ctx, repo, rows); err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.MediaItemID
	}
	existing, err := repo.RecommendableItemIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	live := rows[:0]
	for _, row := range rows {
		if _, ok := existing[row.MediaItemID]; ok {
			live = append(live, row)
		}
	}
	return live, nil
}

func recentDistinctCompletedRows(rows []WatchProgressRow, limit int) []WatchProgressRow {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
		}
		return rows[i].MediaItemID < rows[j].MediaItemID
	})
	seen := make(map[string]struct{}, len(rows))
	result := make([]WatchProgressRow, 0, min(limit, len(rows)))
	for _, row := range rows {
		if _, ok := seen[row.MediaItemID]; ok {
			continue
		}
		seen[row.MediaItemID] = struct{}{}
		result = append(result, row)
		if len(result) == limit {
			break
		}
	}
	return result
}

func (s *SignalReader) RewatchCounts(ctx context.Context, userID int, profileID string) ([]RewatchCount, error) {
	store, ok, err := s.storeForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.repo.GetRewatchCounts(ctx, userID, profileID)
	}

	counts := make(map[string]*RewatchCount)
	offset := 0
	for {
		history, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{
			ProfileID: profileID,
			Limit:     signalPageSize,
			Offset:    offset,
		})
		if err != nil {
			return nil, fmt.Errorf("list completed history from store: %w", err)
		}
		for _, entry := range history {
			if !entry.Completed {
				continue
			}
			rc := counts[entry.MediaItemID]
			if rc == nil {
				rc = &RewatchCount{MediaItemID: entry.MediaItemID}
				counts[entry.MediaItemID] = rc
			}
			rc.Count++
			watchedAt := parseSignalTime(entry.WatchedAt, time.Time{})
			if watchedAt.After(rc.LastWatchedAt) {
				rc.LastWatchedAt = watchedAt
			}
		}
		if len(history) < signalPageSize {
			break
		}
		offset += len(history)
	}

	result := make([]RewatchCount, 0, len(counts))
	for _, rc := range counts {
		if rc.Count >= 2 {
			result = append(result, *rc)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MediaItemID < result[j].MediaItemID
	})
	return result, nil
}

// pageProgress visits the profile's progress rows with the given status a
// page at a time, newest first. It pages by keyset, so no row is read twice.
// A row updated while the walk runs moves ahead of the cursor and is not
// visited; the change that updated it queues a refresh of its own.
func pageProgress(ctx context.Context, store userstore.UserStore, profileID, status string, visit func([]userstore.WatchProgress) error) error {
	var after *userstore.ProgressKey
	for {
		progress, err := store.ListProgressPage(ctx, profileID, status, after, signalPageSize)
		if err != nil {
			return fmt.Errorf("list progress from store: %w", err)
		}
		if err := visit(progress); err != nil {
			return err
		}
		if len(progress) < signalPageSize {
			return nil
		}
		last := progress[len(progress)-1]
		after = &userstore.ProgressKey{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID}
	}
}

func watchedProgressThresholdMet(positionSeconds, durationSeconds float64) bool {
	return durationSeconds > 0 && positionSeconds/durationSeconds >= 0.5
}

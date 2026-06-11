package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScopeResolver computes the effective library visibility for a
// (user, profile) outside of a request context. Satisfied by
// *access.Resolver.
type ScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

const (
	interestFlushInterval = 2 * time.Second
	interestQueryChunk    = 500
	interestRecomputeTime = 30 * time.Second
	// interestMaxFlushAttempts bounds requeues of a failing mutation so a
	// poisoned item cannot retry every flush forever; the periodic rebuild
	// task repairs whatever gets dropped.
	interestMaxFlushAttempts = 5
)

type interestMutation struct {
	userID    int
	profileID string
	itemID    string
}

// InterestUpdater maintains profile_series_interest from user-state
// mutations. Mutations are queued (cheap, non-blocking) and coalesced by a
// background loop: bursts like history imports or progress-sync ticks
// collapse into one recompute per (profile, series) per flush window.
//
// Recompute reads source-of-truth state through the userstore interface —
// user data may live in per-user SQLite stores, so no SQL joins against
// catalog tables are possible.
type InterestUpdater struct {
	pool      *pgxpool.Pool
	interests *InterestRepository
	stores    userstore.UserStoreProvider
	scopes    ScopeResolver
	logger    *slog.Logger

	mu sync.Mutex
	// pending maps each queued mutation to how many flushes have already
	// failed it (transient failures requeue instead of dropping).
	pending map[interestMutation]int
}

// NewInterestUpdater creates an InterestUpdater.
func NewInterestUpdater(
	pool *pgxpool.Pool,
	interests *InterestRepository,
	stores userstore.UserStoreProvider,
	scopes ScopeResolver,
) *InterestUpdater {
	return &InterestUpdater{
		pool:      pool,
		interests: interests,
		stores:    stores,
		scopes:    scopes,
		logger:    slog.Default().With("component", "notifications.interest"),
		pending:   make(map[interestMutation]int),
	}
}

// QueueItemMutation records that a profile's relationship to a media item
// (favorite, watchlist, watch progress) changed. The item is resolved to its
// parent series asynchronously; movie targets are ignored. Safe to call from
// hot request paths.
func (u *InterestUpdater) QueueItemMutation(userID int, profileID, itemID string) {
	if u == nil || userID <= 0 || profileID == "" || itemID == "" {
		return
	}
	mutation := interestMutation{userID: userID, profileID: profileID, itemID: itemID}
	u.mu.Lock()
	if _, queued := u.pending[mutation]; !queued {
		u.pending[mutation] = 0
	}
	u.mu.Unlock()
}

// Run drains the mutation queue until ctx is canceled.
func (u *InterestUpdater) Run(ctx context.Context) {
	ticker := time.NewTicker(interestFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.flush(ctx)
		}
	}
}

func (u *InterestUpdater) flush(ctx context.Context) {
	u.mu.Lock()
	if len(u.pending) == 0 {
		u.mu.Unlock()
		return
	}
	batch := u.pending
	u.pending = make(map[interestMutation]int)
	u.mu.Unlock()

	// Transient failures requeue for the next flush instead of dropping the
	// mutation, which would leave profile_series_interest stale until some
	// later mutation or the rebuild task happened to touch the series.
	requeue := make(map[interestMutation]int)

	// Resolve items to series and dedupe to one recompute per
	// (user, profile, series).
	type recomputeKey struct {
		userID    int
		profileID string
		seriesID  string
	}
	seen := make(map[recomputeKey]struct{}, len(batch))
	for mutation, failures := range batch {
		if ctx.Err() != nil {
			return // shutting down; the rebuild task repairs anything dropped
		}
		seriesID, ok, err := u.resolveSeriesID(ctx, mutation.itemID)
		if err != nil {
			u.logger.Warn("interest series resolution failed",
				"item_id", mutation.itemID, "error", err)
			requeue[mutation] = failures + 1
			continue
		}
		if !ok {
			continue // movies and unknown items have no series interest
		}
		key := recomputeKey{mutation.userID, mutation.profileID, seriesID}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		recomputeCtx, cancel := context.WithTimeout(ctx, interestRecomputeTime)
		err = u.RecomputeSeries(recomputeCtx, mutation.userID, mutation.profileID, seriesID)
		cancel()
		if err != nil {
			u.logger.Warn("interest recompute failed",
				"user_id", mutation.userID, "profile_id", mutation.profileID,
				"series_id", seriesID, "error", err)
			requeue[mutation] = failures + 1
		}
	}

	if len(requeue) == 0 {
		return
	}
	u.mu.Lock()
	for mutation, failures := range requeue {
		if failures >= interestMaxFlushAttempts {
			u.logger.Warn("interest mutation dropped after repeated failures",
				"item_id", mutation.itemID, "profile_id", mutation.profileID)
			continue
		}
		// A fresh queue of the same mutation (failure count 0) wins; it will
		// be recomputed either way.
		if _, queued := u.pending[mutation]; !queued {
			u.pending[mutation] = failures
		}
	}
	u.mu.Unlock()
}

// resolveSeriesID maps a media item ID (series, season, or episode) to its
// series content ID. Returns ok=false for movies and unknown items.
func (u *InterestUpdater) resolveSeriesID(ctx context.Context, itemID string) (string, bool, error) {
	var seriesID string
	err := u.pool.QueryRow(ctx, `
		SELECT series_id FROM episodes WHERE content_id = $1
		UNION ALL
		SELECT series_id FROM seasons WHERE content_id = $1
		UNION ALL
		SELECT content_id FROM media_items WHERE content_id = $1 AND type = 'series'
		LIMIT 1`,
		itemID,
	).Scan(&seriesID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return seriesID, seriesID != "", nil
}

// RecomputeSeries rebuilds the (profile, series) interest rows from
// source-of-truth user state. It is the single shared path for live updates,
// backfill, and repair, so all three stay drift-free.
func (u *InterestUpdater) RecomputeSeries(ctx context.Context, userID int, profileID, seriesID string) error {
	store, err := u.stores.ForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}

	episodeKeys, seasonIDs, err := u.loadSeriesStructure(ctx, seriesID)
	if err != nil {
		return err
	}
	episodeIDs := make([]string, 0, len(episodeKeys))
	for id := range episodeKeys {
		episodeIDs = append(episodeIDs, id)
	}

	// Favorites/watchlist against episode or season items resolve to the
	// series, so the membership check spans the series and its children.
	interestTargets := append([]string{seriesID}, seasonIDs...)
	interestTargets = append(interestTargets, episodeIDs...)

	favorite, err := anyInBatches(interestTargets, func(chunk []string) (bool, error) {
		matches, err := store.ListFavoritesByMediaItems(ctx, profileID, chunk)
		return anyTrue(matches), err
	})
	if err != nil {
		return fmt.Errorf("load favorites: %w", err)
	}
	watchlist, err := anyInBatches(interestTargets, func(chunk []string) (bool, error) {
		matches, err := store.ListWatchlistByMediaItems(ctx, profileID, chunk)
		return anyTrue(matches), err
	})
	if err != nil {
		return fmt.Errorf("load watchlist: %w", err)
	}

	continueWatching := false
	hasProgression := false
	lastCompletedKey := 0
	hasCompleted := false
	markCompleted := func(episodeID string) {
		hasProgression = true
		if key, ok := episodeKeys[episodeID]; ok && (!hasCompleted || key > lastCompletedKey) {
			lastCompletedKey = key
			hasCompleted = true
		}
	}
	for start := 0; start < len(episodeIDs); start += interestQueryChunk {
		end := min(start+interestQueryChunk, len(episodeIDs))
		progress, err := store.ListProgressByMediaItems(ctx, profileID, episodeIDs[start:end])
		if err != nil {
			return fmt.Errorf("load progress: %w", err)
		}
		for episodeID, entry := range progress {
			hasProgression = true
			if !entry.Completed && entry.PositionSeconds > 0 {
				continueWatching = true
			}
			if entry.Completed {
				markCompleted(episodeID)
			}
		}
	}

	// Completed watch history counts too: history imports and watch-provider
	// syncs may record a watched-at fact without ever writing a progress row,
	// and those episodes must still advance the progression cursor.
	for start := 0; start < len(episodeIDs); start += interestQueryChunk {
		end := min(start+interestQueryChunk, len(episodeIDs))
		chunk := episodeIDs[start:end]
		for offset := 0; ; {
			entries, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{
				ProfileID:    profileID,
				MediaItemIDs: chunk,
				Limit:        interestQueryChunk,
				Offset:       offset,
			})
			if err != nil {
				return fmt.Errorf("load completed history: %w", err)
			}
			for _, entry := range entries {
				markCompleted(entry.MediaItemID)
			}
			if len(entries) < interestQueryChunk {
				break
			}
			offset += len(entries)
		}
	}

	// Conservative progression cursor: next_expected = last completed key + 1.
	// When the profile has gaps (completed E10 with E05 unwatched), a late-
	// arriving E05 will not match next_up (it may still match favorite /
	// watchlist / continue_watching). The plan explicitly prefers this
	// under-notify tradeoff over per-episode gap scans at recompute time.
	var lastCompleted, nextExpected *int
	if hasCompleted {
		completed := lastCompletedKey
		expected := lastCompletedKey + 1
		lastCompleted = &completed
		nextExpected = &expected
	}
	nextUpCandidate := hasProgression

	flags := SeriesInterest{
		UserID:                  userID,
		ProfileID:               profileID,
		SeriesID:                seriesID,
		Favorite:                favorite,
		Watchlist:               watchlist,
		ContinueWatching:        continueWatching,
		NextUpCandidate:         nextUpCandidate,
		LastCompletedEpisodeKey: lastCompleted,
		NextExpectedEpisodeKey:  nextExpected,
	}

	if !flags.HasAnyInterest() && lastCompleted == nil {
		// Nothing can ever notify: drop all rows for the pair.
		return u.interests.DeleteStaleForProfileSeries(ctx, profileID, seriesID, []int{})
	}

	libraryIDs, err := u.visibleSeriesLibraries(ctx, userID, profileID, seriesID)
	if err != nil {
		return err
	}
	if len(libraryIDs) == 0 {
		return u.interests.DeleteStaleForProfileSeries(ctx, profileID, seriesID, []int{})
	}

	rows := make([]SeriesInterest, 0, len(libraryIDs))
	for _, libraryID := range libraryIDs {
		row := flags
		row.LibraryID = libraryID
		rows = append(rows, row)
	}
	if err := u.interests.UpsertRows(ctx, rows); err != nil {
		return err
	}
	return u.interests.DeleteStaleForProfileSeries(ctx, profileID, seriesID, libraryIDs)
}

// loadSeriesStructure returns the series' episode keys (content_id ->
// episode_key) and season content IDs.
func (u *InterestUpdater) loadSeriesStructure(ctx context.Context, seriesID string) (map[string]int, []string, error) {
	rows, err := u.pool.Query(ctx,
		`SELECT content_id, season_number, episode_number FROM episodes WHERE series_id = $1`,
		seriesID)
	if err != nil {
		return nil, nil, fmt.Errorf("load series episodes: %w", err)
	}
	episodeKeys := make(map[string]int, 64)
	for rows.Next() {
		var contentID string
		var seasonNumber, episodeNumber int
		if err := rows.Scan(&contentID, &seasonNumber, &episodeNumber); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan series episode: %w", err)
		}
		if !ValidEpisodeOrdinals(seasonNumber, episodeNumber) {
			continue
		}
		episodeKeys[contentID] = EpisodeKey(seasonNumber, episodeNumber)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	seasonRows, err := u.pool.Query(ctx,
		`SELECT content_id FROM seasons WHERE series_id = $1`, seriesID)
	if err != nil {
		return nil, nil, fmt.Errorf("load series seasons: %w", err)
	}
	seasonIDs := make([]string, 0, 8)
	for seasonRows.Next() {
		var contentID string
		if err := seasonRows.Scan(&contentID); err != nil {
			seasonRows.Close()
			return nil, nil, fmt.Errorf("scan series season: %w", err)
		}
		seasonIDs = append(seasonIDs, contentID)
	}
	seasonRows.Close()
	return episodeKeys, seasonIDs, seasonRows.Err()
}

// visibleSeriesLibraries intersects the series' library memberships with the
// profile's effective library visibility. The interest index must never
// assume global library visibility.
func (u *InterestUpdater) visibleSeriesLibraries(ctx context.Context, userID int, profileID, seriesID string) ([]int, error) {
	rows, err := u.pool.Query(ctx,
		`SELECT media_folder_id FROM media_item_libraries WHERE content_id = $1`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("load series library memberships: %w", err)
	}
	memberships := make([]int, 0, 4)
	for rows.Next() {
		var libraryID int
		if err := rows.Scan(&libraryID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan series library membership: %w", err)
		}
		memberships = append(memberships, libraryID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(memberships) == 0 {
		return nil, nil
	}

	scope, err := u.scopes.Resolve(ctx, access.ResolveInput{
		UserID:              userID,
		ProfileID:           profileID,
		SkipPINVerification: true,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve profile scope: %w", err)
	}

	allowed := make(map[int]bool)
	if scope.AllowedLibraryIDs != nil {
		for _, id := range scope.AllowedLibraryIDs {
			allowed[id] = true
		}
	}
	disabled := make(map[int]bool, len(scope.DisabledLibraryIDs))
	for _, id := range scope.DisabledLibraryIDs {
		disabled[id] = true
	}

	visible := make([]int, 0, len(memberships))
	for _, libraryID := range memberships {
		if scope.AllowedLibraryIDs != nil && !allowed[libraryID] {
			continue
		}
		if disabled[libraryID] {
			continue
		}
		visible = append(visible, libraryID)
	}
	return visible, nil
}

func anyInBatches(ids []string, check func(chunk []string) (bool, error)) (bool, error) {
	for start := 0; start < len(ids); start += interestQueryChunk {
		end := min(start+interestQueryChunk, len(ids))
		found, err := check(ids[start:end])
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func anyTrue(values map[string]bool) bool {
	for _, v := range values {
		if v {
			return true
		}
	}
	return false
}

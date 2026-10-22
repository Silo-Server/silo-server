package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
	drops     droppedSeriesLister
	logger    *slog.Logger

	mu sync.Mutex
	// pending maps each queued mutation to how many flushes have already
	// failed it (transient failures requeue instead of dropping).
	pending map[interestMutation]int
	// deferred maps mutations to the time they join pending.
	deferred map[interestMutation]time.Time
	// homeChanged records each profile's latest Home removal or restore on
	// this node, kept for progressSessionGap.
	homeChanged map[profileKey]time.Time
}

// profileKey names one profile on one account. A profile id alone is not
// unique: every account from before profiles has a profile named "default".
type profileKey struct {
	userID    int
	profileID string
}

// String renders the key as account/profile for logs and errors.
func (k profileKey) String() string { return strconv.Itoa(k.userID) + "/" + k.profileID }

// LogValue logs the key as account/profile in every slog handler; the JSON
// handler would otherwise encode the unexported fields as {}.
func (k profileKey) LogValue() slog.Value { return slog.StringValue(k.String()) }

// profileKeyArrays splits keys into the parallel account and profile arrays a
// query matches with (user_id, profile_id) IN (SELECT * FROM unnest($1::int[], $2::text[])).
func profileKeyArrays(keys []profileKey) ([]int, []string) {
	userIDs := make([]int, len(keys))
	profileIDs := make([]string, len(keys))
	for i, key := range keys {
		userIDs[i], profileIDs[i] = key.userID, key.profileID
	}
	return userIDs, profileIDs
}

// NewInterestUpdater creates an InterestUpdater.
func NewInterestUpdater(
	pool *pgxpool.Pool,
	interests *InterestRepository,
	stores userstore.UserStoreProvider,
	scopes ScopeResolver,
) *InterestUpdater {
	updater := &InterestUpdater{
		pool:      pool,
		interests: interests,
		stores:    stores,
		scopes:    scopes,
		logger:    slog.Default().With("component", "notifications.interest"),
		pending:   make(map[interestMutation]int),
	}
	if pool != nil {
		updater.drops = catalog.NewDroppedSeriesRepo(pool)
	}
	return updater
}

// QueueItemMutation records that a profile's relationship to a media item
// (watch progress, watch history, Home removal) changed. The item is
// resolved to its parent series asynchronously; movie targets are ignored.
// Safe to call from hot request paths.
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

// QueueItemMutationAfter queues the mutation once delay has passed. Queuing
// the same mutation again moves its due time later, never earlier.
func (u *InterestUpdater) QueueItemMutationAfter(userID int, profileID, itemID string, delay time.Duration) {
	if u == nil || userID <= 0 || profileID == "" || itemID == "" {
		return
	}
	mutation := interestMutation{userID: userID, profileID: profileID, itemID: itemID}
	due := time.Now().Add(delay)
	u.mu.Lock()
	if u.deferred == nil {
		u.deferred = make(map[interestMutation]time.Time)
	}
	if due.After(u.deferred[mutation]) {
		u.deferred[mutation] = due
	}
	u.mu.Unlock()
}

// noteHomeChange records a Home removal or restore of the profile.
func (u *InterestUpdater) noteHomeChange(userID int, profileID string, at time.Time) {
	if u == nil {
		return
	}
	u.mu.Lock()
	if u.homeChanged == nil {
		u.homeChanged = make(map[profileKey]time.Time)
	}
	u.homeChanged[profileKey{userID, profileID}] = at
	u.mu.Unlock()
}

// changedHomeSince reports whether the profile changed Home on this node
// after the given time.
func (u *InterestUpdater) changedHomeSince(userID int, profileID string, since time.Time) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	at, ok := u.homeChanged[profileKey{userID, profileID}]
	return ok && at.After(since)
}

// promoteDeferred moves deferred mutations due by now into pending and
// forgets Home changes older than progressSessionGap. The caller holds u.mu.
func (u *InterestUpdater) promoteDeferred(now time.Time) {
	for key, at := range u.homeChanged {
		if now.Sub(at) > progressSessionGap {
			delete(u.homeChanged, key)
		}
	}
	for mutation, due := range u.deferred {
		if due.After(now) {
			continue
		}
		delete(u.deferred, mutation)
		if _, queued := u.pending[mutation]; !queued {
			u.pending[mutation] = 0
		}
	}
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
	u.promoteDeferred(time.Now())
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
	// Resolve every queued item to its series in one query. Marking or
	// unmarking a whole series queues one mutation per episode, and those
	// thousands of items collapse to a single series — resolving them
	// one-at-a-time cost thousands of round-trips before the dedupe below
	// could discard them.
	itemIDs := make([]string, 0, len(batch))
	for mutation := range batch {
		itemIDs = append(itemIDs, mutation.itemID)
	}
	seriesByItem, resolveErr := u.resolveSeriesIDs(ctx, itemIDs)
	if resolveErr != nil {
		u.logger.WarnContext(ctx, "interest series resolution failed",
			"item_count", len(itemIDs), "error", resolveErr)
	}

	seen := make(map[recomputeKey]struct{}, len(batch))
	for mutation, failures := range batch {
		if ctx.Err() != nil {
			return // shutting down; the rebuild task repairs anything dropped
		}
		if resolveErr != nil {
			// The lookup failed for the whole batch, so no item can be
			// classified as "not a series". Requeue rather than silently
			// dropping interest updates.
			requeue[mutation] = failures + 1
			continue
		}
		seriesID, ok := seriesByItem[mutation.itemID]
		if !ok {
			continue // movies and unknown items have no series interest
		}
		key := recomputeKey{mutation.userID, mutation.profileID, seriesID}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		recomputeCtx, cancel := context.WithTimeout(ctx, interestRecomputeTime)
		err := u.RecomputeSeries(recomputeCtx, mutation.userID, mutation.profileID, seriesID)
		cancel()
		if err != nil {
			u.logger.WarnContext(ctx, "interest recompute failed",
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
			u.logger.WarnContext(ctx, "interest mutation dropped after repeated failures",
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

// resolveSeriesIDs maps many media item IDs (series, season, or episode) to
// their series content IDs in one query. Items with no series — movies and
// unknown IDs — are absent from the result, which is what lets the caller keep
// treating "missing" as "no series interest". Semantics match resolveSeriesID
// per item; only the number of round-trips differs.
func (u *InterestUpdater) resolveSeriesIDs(ctx context.Context, itemIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(itemIDs))
	ids := make([]string, 0, len(itemIDs))
	seen := make(map[string]struct{}, len(itemIDs))
	for _, itemID := range itemIDs {
		if itemID == "" {
			continue
		}
		if _, dup := seen[itemID]; dup {
			continue
		}
		seen[itemID] = struct{}{}
		ids = append(ids, itemID)
	}
	if len(ids) == 0 {
		return result, nil
	}

	// One branch per item kind, unioned, mirroring the single-item lookup.
	rows, err := u.pool.Query(ctx, `
		SELECT content_id, series_id FROM episodes WHERE content_id = ANY($1::text[])
		UNION ALL
		SELECT content_id, series_id FROM seasons WHERE content_id = ANY($1::text[])
		UNION ALL
		SELECT content_id, content_id FROM media_items WHERE content_id = ANY($1::text[]) AND type = 'series'`,
		ids,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID, seriesID string
		if err := rows.Scan(&itemID, &seriesID); err != nil {
			return nil, err
		}
		if seriesID == "" {
			continue
		}
		// An ID matching more than one branch keeps the first row, matching
		// the single-item query's UNION ALL ... LIMIT 1 ordering.
		if _, exists := result[itemID]; !exists {
			result[itemID] = seriesID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// RecomputeSeries rebuilds the (profile, series) interest rows from
// source-of-truth user state. It is the single shared path for live updates,
// backfill, and repair, so all three stay drift-free.
//
// The row follows Home's Next Up: it anchors on the episode the profile
// completed most recently, and waits for the first episode after the anchor
// the profile has not started (nextExpectedEpisode). It notifies only while
// the series shows in Next Up and no episode of it is partly watched.
func (u *InterestUpdater) RecomputeSeries(ctx context.Context, userID int, profileID, seriesID string) error {
	store, err := u.stores.ForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}

	episodeKeys, err := u.loadSeriesEpisodes(ctx, seriesID)
	if err != nil {
		return err
	}
	episodeIDs := make([]string, 0, len(episodeKeys))
	for id := range episodeKeys {
		episodeIDs = append(episodeIDs, id)
	}

	var inProgress []userstore.WatchProgress
	started := make([]string, 0, 16)
	var completedEpisodes []completedEpisode
	// Home anchors Next Up on the most recently completed episode, ties going
	// to the later episode. A special anchor moves to the most recent regular
	// one; see below.
	anchorKey, hasAnchor := 0, false
	var anchorAt time.Time
	regularAnchorKey, hasRegularAnchor := 0, false
	var regularAnchorAt time.Time
	for start := 0; start < len(episodeIDs); start += interestQueryChunk {
		end := min(start+interestQueryChunk, len(episodeIDs))
		progress, err := store.ListProgressByMediaItems(ctx, profileID, episodeIDs[start:end])
		if err != nil {
			return fmt.Errorf("load progress: %w", err)
		}
		for episodeID, entry := range progress {
			if entry.Completed || entry.PositionSeconds > 0 {
				started = append(started, episodeID)
			}
			if !entry.Completed && entry.PositionSeconds > 0 {
				entry.MediaItemID = episodeID
				inProgress = append(inProgress, entry)
			}
			if !entry.Completed {
				continue
			}
			key, ok := episodeKeys[episodeID]
			if !ok {
				continue
			}
			at, _ := time.Parse(time.RFC3339, entry.UpdatedAt)
			completedEpisodes = append(completedEpisodes, completedEpisode{key: key, at: at})
			if !hasAnchor || completedLater(at, key, anchorAt, anchorKey) {
				anchorKey, anchorAt, hasAnchor = key, at, true
			}
			if key >= firstRegularEpisodeKey && (!hasRegularAnchor || completedLater(at, key, regularAnchorAt, regularAnchorKey)) {
				regularAnchorKey, regularAnchorAt, hasRegularAnchor = key, at, true
			}
		}
	}
	if !hasAnchor {
		// No completed episode: Home has no Next Up for the series, so
		// nothing can notify.
		return u.interests.DeleteStaleForProfileSeries(ctx, userID, profileID, seriesID, []int{})
	}
	// The anchor the row reports is the episode the profile finished most
	// recently. Specials carry no airing position, so Home doesn't let one
	// anchor a series that has regular seasons: it resumes after the most
	// recent regular episode, or at the first one (catalog's
	// nextUpAnchorsCTE).
	lastCompleted := anchorKey
	if anchorKey < firstRegularEpisodeKey && hasRegularEpisodes(episodeKeys) {
		anchorKey = firstRegularEpisodeKey - 1
		if hasRegularAnchor {
			anchorKey = regularAnchorKey
			lastCompleted = regularAnchorKey
		}
	}
	nextExpected, nextOpen := nextExpectedEpisode(anchorKey, episodeKeys, started)

	// Notifications follow Home: a series the profile removed from Next Up
	// stops notifying, and so does one with a partly watched episode that no
	// later, more recently finished episode supersedes (the next episode will
	// be waiting when the profile finishes it). The cursors are kept, so Next
	// Up resumes from the right episode once the removal lapses or the
	// episode is finished.
	nextUpCandidate := false
	inProgress = catalog.FilterSupersededProgress(inProgress,
		supersededInProgress(inProgress, completedEpisodes, episodeKeys))
	if len(inProgress) == 0 {
		nextUpCandidate, err = u.showsInNextUp(ctx, store, userID, profileID, seriesID, episodeIDs, anchorKey, started)
		if err != nil {
			return err
		}
	}

	libraryIDs, err := u.visibleSeriesLibraries(ctx, userID, profileID, seriesID)
	if err != nil {
		return err
	}
	if len(libraryIDs) == 0 {
		return u.interests.DeleteStaleForProfileSeries(ctx, userID, profileID, seriesID, []int{})
	}

	rows := make([]SeriesInterest, 0, len(libraryIDs))
	for _, libraryID := range libraryIDs {
		rows = append(rows, SeriesInterest{
			UserID:                  userID,
			ProfileID:               profileID,
			LibraryID:               libraryID,
			SeriesID:                seriesID,
			NextUpCandidate:         nextUpCandidate,
			LastCompletedEpisodeKey: &lastCompleted,
			NextExpectedEpisodeKey:  &nextExpected,
			NextExpectedOpen:        nextOpen,
		})
	}
	if err := u.interests.UpsertRows(ctx, rows); err != nil {
		return err
	}
	return u.interests.DeleteStaleForProfileSeries(ctx, userID, profileID, seriesID, libraryIDs)
}

// nextExpectedEpisode returns the episode key Next Up waits for after the
// anchor: the lowest catalog episode keyed after it that the profile has not
// started, whether or not its file is in the library yet. A known episode
// that has not arrived holds the cursor, so a later one landing first does not
// notify. open reports that the catalog knows no unstarted episode after the
// anchor (a season finale before the next season's metadata exists, or a
// rewatch of a series watched through); the cursor then sits past the highest
// known key and the first episode to arrive at or past it is the next.
// started lists episode IDs; an episode key counts as started when any row
// with that key is.
func nextExpectedEpisode(anchor int, episodeKeys map[string]int, started []string) (key int, open bool) {
	startedKeys := make(map[int]struct{}, len(started))
	for _, id := range started {
		if k, ok := episodeKeys[id]; ok {
			startedKeys[k] = struct{}{}
		}
	}
	highest, next, found := anchor, 0, false
	for _, k := range episodeKeys {
		if k <= anchor {
			continue
		}
		highest = max(highest, k)
		if _, ok := startedKeys[k]; ok {
			continue
		}
		if !found || k < next {
			next, found = k, true
		}
	}
	if found {
		return next, false
	}
	return highest + 1, true
}

// completedLater reports whether a completion at (at, key) anchors Next Up
// over the current one: it is newer, or as new and a later episode.
func completedLater(at time.Time, key int, currentAt time.Time, currentKey int) bool {
	return at.After(currentAt) || (at.Equal(currentAt) && key > currentKey)
}

// loadSeriesEpisodes returns the series' episode keys (content_id ->
// episode_key), including episodes the library has no file for yet.
func (u *InterestUpdater) loadSeriesEpisodes(ctx context.Context, seriesID string) (map[string]int, error) {
	rows, err := u.pool.Query(ctx,
		`SELECT content_id, season_number, episode_number FROM episodes WHERE series_id = $1`,
		seriesID)
	if err != nil {
		return nil, fmt.Errorf("load series episodes: %w", err)
	}
	defer rows.Close()
	episodeKeys := make(map[string]int, 64)
	for rows.Next() {
		var contentID string
		var seasonNumber, episodeNumber int
		if err := rows.Scan(&contentID, &seasonNumber, &episodeNumber); err != nil {
			return nil, fmt.Errorf("scan series episode: %w", err)
		}
		if !ValidEpisodeOrdinals(seasonNumber, episodeNumber) {
			continue
		}
		episodeKeys[contentID] = EpisodeKey(seasonNumber, episodeNumber)
	}
	return episodeKeys, rows.Err()
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

package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ProgressLister pages watch-progress rows for one profile.
// userstore.UserStore satisfies it.
type ProgressLister interface {
	ListProgress(ctx context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error)
}

// CompletedProgressSinceLister reads a profile's completed rows newer than a
// cutoff in one bounded query. userstore.UserStore satisfies it; a lister
// without it falls back to paging ListProgress.
type CompletedProgressSinceLister interface {
	ListCompletedProgressSince(ctx context.Context, profileID string, since, until time.Time, limit int) ([]userstore.WatchProgress, error)
}

// ProgressSnapshot pairs a media item with the time its progress row last changed.
type ProgressSnapshot struct {
	ContentID string
	UpdatedAt time.Time
}

// ContinueWatchingProgressFilter identifies in-progress entries that Continue
// Watching surfaces should hide: episodes superseded by a later-completed
// episode in the same series. The first-party sections fetcher and the
// jellycompat Resume endpoint share it so both surfaces agree on what "still
// watching" means.
type ContinueWatchingProgressFilter struct {
	pool *pgxpool.Pool
}

// NewContinueWatchingProgressFilter creates a filter. A nil pool disables the
// superseded-episode check, leaving entries unfiltered.
func NewContinueWatchingProgressFilter(pool *pgxpool.Pool) *ContinueWatchingProgressFilter {
	return &ContinueWatchingProgressFilter{pool: pool}
}

const supersededProgressPageSize = 500

// supersededProgressMaxPages hard-caps how many completed-history pages the
// superseded-episode walk reads in one request (supersededProgressMaxRows in
// the one-query form). The cutoff is the oldest in-progress *episode*, so a
// single episode left unfinished months ago puts every completion since then
// in range; on a heavy watcher that reaches the cap on every load. Hitting it
// means the tail of the completed set went unscanned, so an episode superseded
// only by an older completion can survive on the Continue Watching row. We log
// when that happens rather than silently mis-filter.
const (
	supersededProgressMaxPages = 5
	supersededProgressMaxRows  = supersededProgressMaxPages * supersededProgressPageSize
)

// SupersededEpisodeProgressIDs returns the content IDs of in-progress entries
// whose series has a later episode completed more recently than the entry's
// own progress. Those entries are stale — the viewer already moved past them.
// Non-episode entries never match.
func (f *ContinueWatchingProgressFilter) SupersededEpisodeProgressIDs(ctx context.Context, store ProgressLister, profileID string, entries []userstore.WatchProgress) (map[string]struct{}, error) {
	return f.SupersededEpisodeProgressIDsCached(ctx, store, profileID, entries, nil)
}

// SupersededEpisodeProgressIDsCached is SupersededEpisodeProgressIDs with a
// caller-supplied completed-history cache. Callers that ask more than once
// within a single request — Continue Watching walks its in-progress rows a page
// at a time — pass one cache across all the calls so the completed side is read
// once instead of once per page. A nil cache reads it fresh, which is what a
// one-shot caller wants.
func (f *ContinueWatchingProgressFilter) SupersededEpisodeProgressIDsCached(ctx context.Context, store ProgressLister, profileID string, entries []userstore.WatchProgress, cache *CompletedProgressCache) (map[string]struct{}, error) {
	if f == nil || f.pool == nil {
		return map[string]struct{}{}, nil
	}
	inProgress, err := f.episodeSnapshots(ctx, ProgressSnapshots(entries))
	if err != nil {
		return nil, err
	}
	if len(inProgress) == 0 {
		return map[string]struct{}{}, nil
	}

	// A completed episode can only supersede an in-progress one it was finished
	// more recently than (the query gates on
	// done_progress.updated_at > ip_progress.updated_at). So the only completed
	// rows that can matter are those updated after the oldest in-progress entry;
	// anything older can supersede nothing. Bounding the completed walk at that
	// timestamp keeps import-heavy profiles — whose entire back-catalogue is
	// completed=TRUE with old timestamps — from re-paging hundreds of thousands
	// of irrelevant rows on every Resume/Continue Watching load (the 60–116s
	// tail in the 2026-07-06 slow-query comparison). Only episodes can be
	// superseded, so an old in-progress movie must not pull the cutoff back.
	oldestInProgress := inProgress[0].UpdatedAt
	for _, snapshot := range inProgress[1:] {
		if snapshot.UpdatedAt.Before(oldestInProgress) {
			oldestInProgress = snapshot.UpdatedAt
		}
	}

	var completed []ProgressSnapshot
	// A store that can read progress by item (every userstore.UserStore)
	// checks only the later episodes of the in-progress series instead of
	// walking the completed history newest first.
	if lister, ok := store.(PlayableTargetProgressStore); ok {
		completed, err = f.laterEpisodeCompletions(ctx, lister, profileID, inProgress, oldestInProgress, cache)
	} else {
		completed, err = cache.snapshots(ctx, store, profileID, oldestInProgress)
	}
	if err != nil {
		return nil, err
	}
	if len(completed) == 0 {
		return map[string]struct{}{}, nil
	}

	inProgressIDs, inProgressUpdatedAts := splitProgressSnapshots(inProgress)
	completedIDs, completedUpdatedAts := splitProgressSnapshots(completed)
	query := buildSupersededEpisodeProgressQuery()
	rows, err := f.pool.Query(ctx, query, inProgressIDs, inProgressUpdatedAts, completedIDs, completedUpdatedAts)
	if err != nil {
		return nil, fmt.Errorf("querying superseded episode progress: %w", err)
	}
	defer rows.Close()

	superseded := make(map[string]struct{})
	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning superseded episode progress: %w", err)
		}
		superseded[mediaItemID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating superseded episode progress: %w", err)
	}
	return superseded, nil
}

// laterEpisodeIDsChunk bounds one ListProgressByMediaItems batch.
const laterEpisodeIDsChunk = 5000

// laterEpisodeCompletions returns the completed progress, updated after
// notBefore, of every episode that sits after an in-progress episode in the
// same series. Those are the only rows the superseded query can match, so
// reading them by item replaces the newest-first walk of the whole completed
// history and its row cap: how much history a profile has no longer decides
// whether an old resume point is checked.
//
// A cache for the same profile keeps each item's answer, so a later
// Continue Watching page reads progress only for items no earlier page asked
// about. A nil cache, or one bound to another profile, reads fresh.
func (f *ContinueWatchingProgressFilter) laterEpisodeCompletions(ctx context.Context, lister PlayableTargetProgressStore, profileID string, inProgress []ProgressSnapshot, notBefore time.Time, cache *CompletedProgressCache) ([]ProgressSnapshot, error) {
	ids, _ := splitProgressSnapshots(inProgress)
	rows, err := f.pool.Query(ctx, buildLaterEpisodesQuery(), ids)
	if err != nil {
		return nil, fmt.Errorf("querying later episodes of in-progress series: %w", err)
	}
	later, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scanning later episodes of in-progress series: %w", err)
	}

	if cache == nil || (cache.profileID != "" && cache.profileID != profileID) {
		cache = &CompletedProgressCache{}
	}
	cache.profileID = profileID
	if cache.byItem == nil {
		cache.byItem = make(map[string]*ProgressSnapshot)
	}

	unread := make([]string, 0, len(later))
	for _, id := range later {
		if _, ok := cache.byItem[id]; !ok {
			unread = append(unread, id)
		}
	}
	for start := 0; start < len(unread); start += laterEpisodeIDsChunk {
		batch := unread[start:min(start+laterEpisodeIDsChunk, len(unread))]
		progress, err := lister.ListProgressByMediaItems(ctx, profileID, batch)
		if err != nil {
			return nil, fmt.Errorf("listing completed progress for superseded episodes: %w", err)
		}
		for _, id := range batch {
			cache.byItem[id] = nil
		}
		entries := make([]userstore.WatchProgress, 0, len(progress))
		for _, entry := range progress {
			if entry.Completed {
				entries = append(entries, entry)
			}
		}
		for _, snapshot := range ProgressSnapshots(entries) {
			cache.byItem[snapshot.ContentID] = &snapshot
		}
	}

	var completed []ProgressSnapshot
	for _, id := range later {
		if snapshot := cache.byItem[id]; snapshot != nil && snapshot.UpdatedAt.After(notBefore) {
			completed = append(completed, *snapshot)
		}
	}
	return completed, nil
}

// buildLaterEpisodesQuery lists the episodes that could supersede one of the
// in-progress episodes in $1: same series, a later (season, episode), and the
// same specials side, as in buildSupersededEpisodeProgressQuery. For each
// series it starts from that series' earliest in-progress episode, so the
// result holds each candidate once. The lateral subquery reads each series as
// one range of episodes_series_season_episode_key; OFFSET 0 keeps the planner
// from flattening it into a hash join over the whole episodes table, which it
// picks when the in-progress series hold a large share of a small catalog.
func buildLaterEpisodesQuery() string {
	return `
		WITH first_in_progress AS (
			SELECT DISTINCT ON (ip.series_id, ip.season_number = 0)
			       ip.series_id, ip.season_number, ip.episode_number
			FROM episodes ip
			WHERE ip.content_id = ANY($1::text[])
			ORDER BY ip.series_id, ip.season_number = 0, ip.season_number, ip.episode_number
		)
		SELECT done.content_id
		FROM first_in_progress ip
		CROSS JOIN LATERAL (
			SELECT d.content_id
			FROM episodes d
			WHERE d.series_id = ip.series_id
			  AND (d.season_number, d.episode_number) > (ip.season_number, ip.episode_number)
			  AND (d.season_number = 0) = (ip.season_number = 0)
			OFFSET 0
		) done`
}

// episodeSnapshots keeps the snapshots whose content is an episode. Callers
// such as jellycompat Resume do not know each entry's type, and an unfinished
// movie would otherwise set the completed-walk cutoff.
func (f *ContinueWatchingProgressFilter) episodeSnapshots(ctx context.Context, snapshots []ProgressSnapshot) ([]ProgressSnapshot, error) {
	if len(snapshots) == 0 {
		return snapshots, nil
	}
	ids, _ := splitProgressSnapshots(snapshots)
	rows, err := f.pool.Query(ctx, `SELECT content_id FROM episodes WHERE content_id = ANY($1::text[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("querying in-progress episode ids: %w", err)
	}
	defer rows.Close()
	episodes := make(map[string]struct{}, len(ids))
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, fmt.Errorf("scanning in-progress episode id: %w", err)
		}
		episodes[contentID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating in-progress episode ids: %w", err)
	}
	kept := snapshots[:0:0]
	for _, snapshot := range snapshots {
		if _, ok := episodes[snapshot.ContentID]; ok {
			kept = append(kept, snapshot)
		}
	}
	return kept, nil
}

// CompletedProgressCache holds one request's walk of a profile's completed
// progress rows so repeated superseded-episode checks share it.
//
// Continue Watching pages its in-progress rows (up to continueProgressMaxScanned
// / continueProgressPageSize pages) and asks for the superseded set once per
// page. Each ask needs the completed rows newer than that page's oldest
// in-progress entry. Because in-progress pages come back updated_at DESC, later
// pages ask for older cutoffs, so the walks are nested rather than disjoint:
// without a cache every page re-reads everything the previous page already read,
// which on a full run is supersededProgressMaxPages reads repeated for each
// in-progress page.
//
// The cache reads strictly forward. It keeps the rows it has seen plus the
// updated_at it has scanned down to, and only reads more pages when a caller
// asks for a cutoff older than that. A row is never read twice, and the answer
// for any cutoff is identical to a fresh walk.
//
// Not safe for concurrent use: scope one to a single request.
type CompletedProgressCache struct {
	profileID string
	snaps     []ProgressSnapshot
	seen      map[string]struct{}
	pagesRead int
	// scannedTo is the updated_at of the last row read. Every unread row is at
	// or before it. Meaningful only once pagesRead > 0.
	scannedTo time.Time
	// done means the source is exhausted or the page cap stopped the walk;
	// either way there is nothing further to read.
	done   bool
	capped bool
	// readSinceAt is the oldest cutoff the one-query form has read down to:
	// snaps then holds every completed row after it (up to the row cap).
	readSinceAt time.Time
	sinceValid  bool
	// byItem holds the by-item lookup's answers: an item's completed
	// snapshot, or nil when it has no completed progress. An item is present
	// once it has been read.
	byItem map[string]*ProgressSnapshot
}

// NewCompletedProgressCache returns a cache for one request.
func NewCompletedProgressCache() *CompletedProgressCache {
	return &CompletedProgressCache{seen: make(map[string]struct{})}
}

// snapshots returns the deduplicated completed snapshots updated after
// notBefore, reading only the pages the cache has not already read. A nil
// receiver, or a cache already bound to a different profile, falls back to a
// fresh uncached walk.
func (c *CompletedProgressCache) snapshots(ctx context.Context, store ProgressLister, profileID string, notBefore time.Time) ([]ProgressSnapshot, error) {
	if c == nil || (c.profileID != "" && c.profileID != profileID) {
		return CompletedProgressSnapshots(ctx, store, profileID, notBefore)
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	c.profileID = profileID
	if lister, ok := store.(CompletedProgressSinceLister); ok {
		if err := c.readSince(ctx, lister, profileID, notBefore); err != nil {
			return nil, err
		}
		return c.snapshotsAfter(notBefore), nil
	}

	// Read forward until the unread remainder is entirely at or before the
	// cutoff, at which point it cannot contain anything this caller wants.
	for !c.done && (c.pagesRead == 0 || c.scannedTo.After(notBefore)) {
		if c.pagesRead >= supersededProgressMaxPages {
			c.done = true
			c.capped = true
			// Fell out with a full final page: the cap halted the walk before
			// the cutoff, so completed rows past the scanned window were
			// skipped. Log it so a real profile that trips this backstop is
			// visible rather than silently mis-filtered.
			slog.WarnContext(ctx, "continue-watching: superseded-episode walk hit page cap; completed-history tail left unscanned",
				"profile_id", profileID,
				"pages_scanned", supersededProgressMaxPages,
				"rows_scanned", len(c.snaps))
			break
		}
		offset := c.pagesRead * supersededProgressPageSize
		entries, err := store.ListProgress(ctx, profileID, "completed", supersededProgressPageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("listing completed progress for superseded episodes: %w", err)
		}
		c.pagesRead++

		for _, snapshot := range ProgressSnapshots(entries) {
			c.scannedTo = snapshot.UpdatedAt
			if _, ok := c.seen[snapshot.ContentID]; ok {
				continue
			}
			c.seen[snapshot.ContentID] = struct{}{}
			c.snaps = append(c.snaps, snapshot)
		}

		if len(entries) < supersededProgressPageSize {
			c.done = true
		}
	}

	return c.snapshotsAfter(notBefore), nil
}

// readSince loads the completed rows after notBefore in one query unless an
// earlier read already covers that cutoff. A later, older cutoff reads only the
// rows between it and the previous one, so the request reads each row once and
// the row cap bounds the whole request, as it does for the offset walk. A zero
// cutoff already covers the whole history.
func (c *CompletedProgressCache) readSince(ctx context.Context, lister CompletedProgressSinceLister, profileID string, notBefore time.Time) error {
	if c.capped || (c.sinceValid && (c.readSinceAt.IsZero() || !notBefore.Before(c.readSinceAt))) {
		return nil
	}
	var until time.Time
	if c.sinceValid {
		until = c.readSinceAt
	}
	remaining := supersededProgressMaxRows - len(c.snaps)
	entries, err := lister.ListCompletedProgressSince(ctx, profileID, notBefore, until, remaining+1)
	if err != nil {
		return fmt.Errorf("listing completed progress for superseded episodes: %w", err)
	}
	if len(entries) > remaining {
		entries = entries[:remaining]
		c.capped = true
		slog.WarnContext(ctx, "continue-watching: superseded-episode walk hit page cap; completed-history tail left unscanned",
			"profile_id", profileID,
			"pages_scanned", supersededProgressMaxPages,
			"rows_scanned", len(c.snaps)+len(entries))
	}
	// The new rows are all older than the cached ones, so snaps stays
	// updated_at DESC.
	c.snaps = append(c.snaps, ProgressSnapshots(entries)...)
	c.readSinceAt = notBefore
	c.sinceValid = true
	return nil
}

// snapshotsAfter returns the cached snapshots updated after notBefore.
func (c *CompletedProgressCache) snapshotsAfter(notBefore time.Time) []ProgressSnapshot {
	// c.snaps is updated_at DESC, so the wanted rows are a prefix of it.
	result := make([]ProgressSnapshot, 0, len(c.snaps))
	for _, snapshot := range c.snaps {
		if !snapshot.UpdatedAt.After(notBefore) {
			break
		}
		result = append(result, snapshot)
	}
	return result
}

// CompletedProgressSnapshots pages through the profile's completed progress
// rows and returns deduplicated snapshots updated after notBefore. The
// completed listing is ordered updated_at DESC (newest first), so once a row at
// or before notBefore is reached every later page is older still and paging
// stops — callers only care about completed episodes finished more recently
// than an in-progress entry, so older rows are irrelevant. Pass a zero
// notBefore to walk the whole history.
//
// This is the one-shot form. A caller that asks repeatedly within one request
// should hold a CompletedProgressCache instead and pass it to
// SupersededEpisodeProgressIDsCached.
func CompletedProgressSnapshots(ctx context.Context, store ProgressLister, profileID string, notBefore time.Time) ([]ProgressSnapshot, error) {
	fresh := &CompletedProgressCache{seen: make(map[string]struct{}), profileID: profileID}
	return fresh.snapshots(ctx, store, profileID, notBefore)
}

// ProgressSnapshots converts progress rows to snapshots, dropping rows with a
// blank media item ID or an unparseable timestamp.
func ProgressSnapshots(entries []userstore.WatchProgress) []ProgressSnapshot {
	snapshots := make([]ProgressSnapshot, 0, len(entries))
	for _, entry := range entries {
		contentID := strings.TrimSpace(entry.MediaItemID)
		if contentID == "" {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339, entry.UpdatedAt)
		if err != nil || updatedAt.IsZero() {
			continue
		}
		snapshots = append(snapshots, ProgressSnapshot{
			ContentID: contentID,
			UpdatedAt: updatedAt.UTC(),
		})
	}
	return snapshots
}

func splitProgressSnapshots(snapshots []ProgressSnapshot) ([]string, []time.Time) {
	contentIDs := make([]string, len(snapshots))
	updatedAts := make([]time.Time, len(snapshots))
	for i, snapshot := range snapshots {
		contentIDs[i] = snapshot.ContentID
		updatedAts[i] = snapshot.UpdatedAt
	}
	return contentIDs, updatedAts
}

// The snapshots arrive as unnest arrays instead of joins against
// user_watch_progress because per-user progress may live in a SQLite store
// rather than this Postgres database. Specials (season 0) have no airing
// position, so only a later special supersedes a special, and a special never
// supersedes a regular episode.
func buildSupersededEpisodeProgressQuery() string {
	return `
		WITH in_progress(content_id, updated_at) AS (
			SELECT * FROM unnest($1::text[], $2::timestamptz[])
		),
		completed(content_id, updated_at) AS (
			SELECT * FROM unnest($3::text[], $4::timestamptz[])
		)
		SELECT DISTINCT ip.content_id
		FROM in_progress ip_progress
		JOIN episodes ip ON ip.content_id = ip_progress.content_id
		JOIN episodes done
		  ON done.series_id = ip.series_id
		 AND (done.season_number, done.episode_number) > (ip.season_number, ip.episode_number)
		 AND (done.season_number = 0) = (ip.season_number = 0)
		JOIN completed done_progress
		  ON done_progress.content_id = done.content_id
		WHERE done_progress.updated_at > ip_progress.updated_at`
}

// FilterSupersededProgress drops entries whose media item ID is in the
// superseded set.
func FilterSupersededProgress(entries []userstore.WatchProgress, superseded map[string]struct{}) []userstore.WatchProgress {
	if len(entries) == 0 || len(superseded) == 0 {
		return entries
	}

	filtered := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		if _, ok := superseded[entry.MediaItemID]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// HomeDismissalIndex maps media item ID to its dismissal row for one home surface.
type HomeDismissalIndex map[string]userstore.HomeItemDismissal

// NewHomeDismissalIndex builds an index from dismissal rows.
func NewHomeDismissalIndex(dismissals []userstore.HomeItemDismissal) HomeDismissalIndex {
	index := make(HomeDismissalIndex, len(dismissals))
	for _, dismissal := range dismissals {
		index[dismissal.MediaItemID] = dismissal
	}
	return index
}

// FilterProgress drops entries still covered by a dismissal. A dismissal only
// holds while the entry's progress timestamp matches the one captured when the
// user dismissed it; resuming playback re-surfaces the item.
func (idx HomeDismissalIndex) FilterProgress(entries []userstore.WatchProgress) []userstore.WatchProgress {
	if len(entries) == 0 || len(idx) == 0 {
		return entries
	}

	filtered := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		dismissal, ok := idx[entry.MediaItemID]
		if !ok || dismissal.ProgressUpdatedAt == nil || *dismissal.ProgressUpdatedAt != entry.UpdatedAt {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

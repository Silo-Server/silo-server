package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ProgressLister pages watch-progress rows for one profile.
// userstore.UserStore satisfies it.
type ProgressLister interface {
	ListProgress(ctx context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error)
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

// SupersededEpisodeProgressIDs returns the content IDs of in-progress entries
// whose series has a later episode completed more recently than the entry's
// own progress. Those entries are stale — the viewer already moved past them.
// Non-episode entries never match.
func (f *ContinueWatchingProgressFilter) SupersededEpisodeProgressIDs(ctx context.Context, store ProgressLister, profileID string, entries []userstore.WatchProgress) (map[string]struct{}, error) {
	if f == nil || f.pool == nil {
		return map[string]struct{}{}, nil
	}
	inProgress := ProgressSnapshots(entries)
	if len(inProgress) == 0 {
		return map[string]struct{}{}, nil
	}

	completed, err := CompletedProgressSnapshots(ctx, store, profileID)
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

// CompletedProgressSnapshots pages through all completed progress rows for the
// profile and returns deduplicated snapshots.
func CompletedProgressSnapshots(ctx context.Context, store ProgressLister, profileID string) ([]ProgressSnapshot, error) {
	seen := make(map[string]struct{})
	snapshots := make([]ProgressSnapshot, 0)

	for offset := 0; ; offset += supersededProgressPageSize {
		entries, err := store.ListProgress(ctx, profileID, "completed", supersededProgressPageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("listing completed progress for superseded episodes: %w", err)
		}

		for _, snapshot := range ProgressSnapshots(entries) {
			contentID := snapshot.ContentID
			if _, ok := seen[contentID]; ok {
				continue
			}
			seen[contentID] = struct{}{}
			snapshots = append(snapshots, snapshot)
		}

		if len(entries) < supersededProgressPageSize {
			return snapshots, nil
		}
	}
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
// rather than this Postgres database.
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

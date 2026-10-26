package trickplay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotOptedIn reports that none of an item's files belongs to a library
// with seek previews turned on.
var ErrNotOptedIn = errors.New("seek previews are off for this item's library")

// ErrItemNotFound reports an item without media files.
var ErrItemNotFound = errors.New("item has no media files")

// Admin answers administrators: an item's preview state, regeneration, and
// per-library progress.
type Admin struct {
	pool  *pgxpool.Pool
	repo  *Repository
	store interface{ Identity() string }
	// kick wakes this server's workers after a regeneration.
	kick func()
}

// NewAdmin returns the administrator view of the previews published into
// store. kick, which may be nil, wakes the workers.
func NewAdmin(pool *pgxpool.Pool, store interface{ Identity() string }, kick func()) *Admin {
	if pool == nil || store == nil {
		return nil
	}
	return &Admin{pool: pool, repo: NewRepository(pool), store: store, kick: kick}
}

// FileStatus is one media file's previews.
type FileStatus struct {
	FileID int
	// State is pending, running, ready, or unusable, or off when the file's
	// library does not generate previews.
	State string
	// Servable reports sheets players are served now; a regeneration keeps
	// serving the previous sheets until it publishes.
	Servable       bool
	Failures       int
	LastError      string
	GeneratedAt    *time.Time
	ThumbnailCount int
	Width          int
	IntervalMS     int
	SheetBytes     int64
}

// itemFilesSQL names an item's media files: a movie's or an episode's own
// files, or every episode file of a series.
const itemFilesSQL = `
	SELECT id FROM public.media_files WHERE content_id = $1 OR episode_id = $1
	UNION
	SELECT mf.id
	FROM public.episodes covered
	JOIN public.episodes first ON first.series_id = covered.series_id AND first.season_number = covered.season_number
	JOIN public.media_files mf ON mf.episode_id = first.content_id
	WHERE covered.content_id = $1
	  AND mf.multi_episode_end > mf.multi_episode_start
	  AND covered.episode_number BETWEEN mf.multi_episode_start AND mf.multi_episode_end`

func (a *Admin) itemFiles(ctx context.Context, itemID string) ([]int, error) {
	rows, err := a.pool.Query(ctx, itemFilesSQL, itemID)
	if err != nil {
		return nil, fmt.Errorf("list item files: %w", err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrItemNotFound
	}
	return ids, nil
}

// ItemStatus reports the previews of each of an item's files.
func (a *Admin) ItemStatus(ctx context.Context, itemID string) ([]FileStatus, error) {
	ids, err := a.itemFiles(ctx, itemID)
	if err != nil {
		return nil, err
	}
	servable, err := a.repo.Manifests(ctx, ids, a.store.Identity())
	if err != nil {
		return nil, err
	}
	rows, err := a.pool.Query(ctx, `
		SELECT mf.id, CASE
		       WHEN NOT f.trickplay_enabled OR f.enabled IS FALSE
		         OR lower(btrim(f.type)) <> ALL($2::text[]) THEN 'off'
		       WHEN lower(btrim(f.type)) = ANY($2::text[])
		         AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		         AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
		       THEN COALESCE(t.state, 'pending') ELSE 'unusable' END,
		       COALESCE(t.failure_count, 0), COALESCE(t.last_error, ''),
		       t.generated_at, COALESCE(t.thumbnail_count, 0), COALESCE(t.width, 0), COALESCE(t.interval_ms, 0),
		       COALESCE(t.sheet_bytes, 0)
		FROM public.media_files mf
		JOIN public.media_folders f ON f.id = mf.media_folder_id
		LEFT JOIN public.media_file_trickplay t ON t.media_file_id = mf.id
		WHERE mf.id = ANY($1)
		ORDER BY mf.id`, ids, videoLibraryTypes)
	if err != nil {
		return nil, fmt.Errorf("read trickplay status: %w", err)
	}
	defer rows.Close()
	var out []FileStatus
	for rows.Next() {
		var s FileStatus
		if err := rows.Scan(&s.FileID, &s.State, &s.Failures, &s.LastError, &s.GeneratedAt, &s.ThumbnailCount,
			&s.Width, &s.IntervalMS, &s.SheetBytes); err != nil {
			return nil, err
		}
		_, s.Servable = servable[s.FileID]
		out = append(out, s)
	}
	return out, rows.Err()
}

// Regenerate requeues an item's files ahead of the backlog and returns how
// many it requeued. A file whose previews are being made is left to finish.
func (a *Admin) Regenerate(ctx context.Context, itemID string) (int, error) {
	ids, err := a.itemFiles(ctx, itemID)
	if err != nil {
		return 0, err
	}
	// Eligibility follows the current library setting, including while a
	// reconcile has not yet added or removed queue rows.
	var enabled int
	if err := a.pool.QueryRow(ctx, `SELECT count(*) FROM public.media_files mf
		JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE mf.id = ANY($1) AND f.trickplay_enabled AND f.enabled IS NOT FALSE
		  AND lower(btrim(f.type)) = ANY($2::text[])`, ids, videoLibraryTypes).Scan(&enabled); err != nil {
		return 0, fmt.Errorf("count enabled trickplay files: %w", err)
	}
	if enabled == 0 {
		return 0, ErrNotOptedIn
	}
	requeued, err := a.repo.Regenerate(ctx, ids)
	if err == nil && requeued > 0 && a.kick != nil {
		a.kick()
	}
	return requeued, err
}

// LibraryStatus is the previews of one library that generates them.
type LibraryStatus struct {
	LibraryID  int
	Name       string
	Pending    int
	Running    int
	Ready      int
	Unusable   int
	SheetBytes int64
}

// LibraryStatuses reports every library that generates previews, with its
// files' counts by state and the bytes its sheets take.
func (a *Admin) LibraryStatuses(ctx context.Context) ([]LibraryStatus, error) {
	rows, err := a.pool.Query(ctx, `
		WITH files AS (
			SELECT f.id AS library_id, f.name, t.sheet_bytes,
			       CASE WHEN mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
			              AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
			            THEN COALESCE(t.state, 'pending')
			            WHEN t.media_file_id IS NOT NULL THEN 'unusable' END AS state
			FROM public.media_folders f
			LEFT JOIN public.media_files mf ON mf.media_folder_id = f.id
			LEFT JOIN public.media_file_trickplay t ON t.media_file_id = mf.id
			WHERE f.trickplay_enabled AND f.enabled IS NOT FALSE
			  AND lower(btrim(f.type)) = ANY($1::text[])
		)
		SELECT library_id, name,
		       count(*) FILTER (WHERE state = 'pending'),
		       count(*) FILTER (WHERE state = 'running'),
		       count(*) FILTER (WHERE state = 'ready'),
		       count(*) FILTER (WHERE state = 'unusable'),
		       COALESCE(sum(sheet_bytes), 0)::bigint
		FROM files
		GROUP BY library_id, name
		ORDER BY library_id`, videoLibraryTypes)
	if err != nil {
		return nil, fmt.Errorf("read trickplay library status: %w", err)
	}
	defer rows.Close()
	var out []LibraryStatus
	for rows.Next() {
		var s LibraryStatus
		if err := rows.Scan(&s.LibraryID, &s.Name, &s.Pending, &s.Running, &s.Ready, &s.Unusable, &s.SheetBytes); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

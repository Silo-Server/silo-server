package trickplay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Repository keeps media_file_trickplay: the queue of files to generate and
// their published manifests.
//
// Every server works the queue. A claim takes the next due row with FOR
// UPDATE SKIP LOCKED and leases it; the worker renews the lease while it
// runs, and a lease that runs out (the server died) is reclaimed as a
// failure, so another server retries it after a backoff. Publishing and
// failing are fenced on the lease, so a worker that lost its lease changes
// nothing.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a repository on pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Row states.
const (
	statePending  = "pending"
	stateRunning  = "running"
	stateReady    = "ready"
	stateUnusable = "unusable"
)

// failureBackoff is how long a file waits after its nth consecutive failure:
// 15 minutes, an hour, six hours, a day, then a week.
var failureBackoff = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

func backoffAfter(failures int) time.Duration {
	return failureBackoff[min(max(failures, 1), len(failureBackoff))-1]
}

// abandonedGrace is the minimum time an abandoned or displaced revision
// stays in storage. Issued URLs can extend a published revision's deadline.
const abandonedGrace = 48 * time.Hour

// videoLibraryTypes are the media_folders.type values of video libraries,
// the normalized spellings internal/librarykind accepts for movies, TV, and
// mixed libraries.
var videoLibraryTypes = []string{"movie", "movies", "series", "tv", "show", "tvshows", "mixed"}

// Job is a claimed file: what a worker needs to generate its sheets.
type Job struct {
	FileID int
	// LeaseToken identifies this attempt, including when the same server
	// reclaims the file after an earlier attempt expires.
	LeaseToken      string
	FilePath        string
	Container       string
	Codec           string
	DurationSeconds int
	HDR             bool
	VideoTracks     []models.VideoTrack
	// Failures counts the file's consecutive failures before this claim.
	Failures int
}

// Claim leases the next due file for owner, for a server at recipe version
// AlgorithmVersion, or returns nil when none is due. Mutations must use the
// returned Job.LeaseToken, which changes on every claim.
func (r *Repository) Claim(ctx context.Context, owner string, lease time.Duration) (*Job, error) {
	return r.claim(ctx, owner, lease, 0)
}

// ClaimFile leases fileID if it is due.
func (r *Repository) ClaimFile(ctx context.Context, fileID int, owner string, lease time.Duration) (*Job, error) {
	return r.claim(ctx, owner, lease, fileID)
}

func (r *Repository) claim(ctx context.Context, owner string, lease time.Duration, fileID int) (*Job, error) {
	var job Job
	leaseToken := owner + ":" + rand.Text()
	var videoTracks []byte
	var container, codec *string
	var hdr *bool
	var duration *int
	err := r.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT t.media_file_id FROM public.media_file_trickplay t
			JOIN public.media_files mf ON mf.id = t.media_file_id
			JOIN public.media_folders f ON f.id = mf.media_folder_id
			WHERE t.state = 'pending' AND t.available_at <= now() AND t.recipe_version <= $3
			  AND ($4::bigint = 0 OR t.media_file_id = $4)
			  AND f.trickplay_enabled AND f.enabled IS NOT FALSE AND lower(btrim(f.type)) = ANY($5::text[])
			  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
			  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
			ORDER BY t.available_at, t.media_file_id
			LIMIT 1
			FOR UPDATE OF t SKIP LOCKED
		)
		UPDATE public.media_file_trickplay t
		SET state = 'running', lease_owner = $1, lease_expires_at = now() + make_interval(secs => $2),
		    work_revision = NULL, source_size = mf.file_size, source_hash = mf.file_hash,
		    source_duration = mf.duration, recipe_version = $3, updated_at = now()
		FROM next, public.media_files mf JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE t.media_file_id = next.media_file_id AND mf.id = t.media_file_id
		  AND f.trickplay_enabled AND f.enabled IS NOT FALSE AND lower(btrim(f.type)) = ANY($5::text[])
		  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
		RETURNING t.media_file_id, mf.file_path, mf.container, mf.codec_video, mf.duration, mf.hdr,
		          COALESCE(mf.video_tracks, '[]'::jsonb), t.failure_count, t.lease_owner`,
		leaseToken, lease.Seconds(), AlgorithmVersion, fileID, videoLibraryTypes,
	).Scan(&job.FileID, &job.FilePath, &container, &codec, &duration, &hdr, &videoTracks, &job.Failures, &job.LeaseToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim trickplay work: %w", err)
	}
	if container != nil {
		job.Container = *container
	}
	if codec != nil {
		job.Codec = *codec
	}
	if duration != nil {
		job.DurationSeconds = *duration
	}
	job.HDR = hdr != nil && *hdr
	if err := json.Unmarshal(videoTracks, &job.VideoTracks); err != nil {
		job.VideoTracks = nil
	}
	return &job, nil
}

// Heartbeat renews the claim identified by leaseToken, and reports false
// when the lease is lost or the file is no longer eligible.
func (r *Repository) Heartbeat(ctx context.Context, fileID int, leaseToken string, lease time.Duration) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE public.media_file_trickplay t
		SET lease_expires_at = now() + make_interval(secs => $3), updated_at = now()
		FROM public.media_files mf JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE t.media_file_id = $1 AND t.lease_owner = $2 AND t.state = 'running' AND t.lease_expires_at > now()
		  AND mf.id = t.media_file_id AND f.trickplay_enabled AND f.enabled IS NOT FALSE
		  AND lower(btrim(f.type)) = ANY($4::text[])
		  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0`,
		fileID, leaseToken, lease.Seconds(), videoLibraryTypes)
	if err != nil {
		return false, fmt.Errorf("renew trickplay lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// BeginUpload picks the revision the claim's generation of fileID uploads under,
// and reports false when the lease is lost or the file is no longer eligible.
// Until it publishes, the revision is queued for deletion if the work is abandoned.
func (r *Repository) BeginUpload(ctx context.Context, fileID int, leaseToken string) (int64, bool, error) {
	revision := newRevision()
	tag, err := r.pool.Exec(ctx, `
		UPDATE public.media_file_trickplay t SET work_revision = $3, updated_at = now()
		FROM public.media_files mf JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE t.media_file_id = $1 AND t.lease_owner = $2 AND t.state = 'running' AND t.lease_expires_at > now()
		  AND t.work_revision IS NULL AND mf.id = t.media_file_id
		  AND f.trickplay_enabled AND f.enabled IS NOT FALSE AND lower(btrim(f.type)) = ANY($4::text[])
		  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0`,
		fileID, leaseToken, revision, videoLibraryTypes)
	if err != nil {
		return 0, false, fmt.Errorf("start trickplay upload: %w", err)
	}
	return revision, tag.RowsAffected() == 1, nil
}

// newRevision is a random positive 63-bit number. Revisions name storage
// prefixes served as immutable, so they must never repeat, even after a
// database restore rewinds any sequence.
func newRevision() int64 {
	var b [8]byte
	for {
		_, _ = rand.Read(b[:])
		if revision := int64(binary.BigEndian.Uint64(b[:]) >> 1); revision > 0 {
			return revision
		}
	}
}

// Published is what a finished generation publishes.
type Published struct {
	Recipe        Recipe
	StoreIdentity string
	Height        int
	Count         int
	SheetBytes    []int
	Decoder       string
	Filled        int
}

// Publish makes the claim's uploaded revision of fileID the one served, and
// queues the revision it replaces for deletion. It reports false, changing
// nothing, when the lease was lost or the file is no longer eligible.
func (r *Repository) Publish(ctx context.Context, fileID int, leaseToken string, revision int64, p Published) (bool, error) {
	return r.fenced(ctx, fileID, leaseToken, func(tx pgx.Tx, current fencedRow) (bool, error) {
		if !current.eligible || current.workRevision == nil || *current.workRevision != revision {
			return false, nil
		}
		if current.revision != nil {
			if err := queueRevision(ctx, tx, fileID, *current.revision, current.urlExpiresAt); err != nil {
				return false, err
			}
		}
		columns, rows := p.Recipe.Grid()
		var total int64
		for _, n := range p.SheetBytes {
			total += int64(n)
		}
		_, err := tx.Exec(ctx, `
			UPDATE public.media_file_trickplay
			SET state = 'ready', lease_owner = NULL, lease_expires_at = NULL,
			    revision = work_revision, work_revision = NULL, failure_count = 0, last_error = '',
			    published_recipe = $2, published_size = source_size, published_hash = source_hash,
			    published_duration = source_duration, published_expires_at = NULL, store_identity = $3,
			    width = $4, height = $5, tile_columns = $6, tile_rows = $7, interval_ms = $8,
			    thumbnail_count = $9, sheet_count = $10, bandwidth = $11, sheet_bytes = $12,
			    decoder = $13, filled = $14, generated_at = now(), updated_at = now()
			WHERE media_file_id = $1`,
			fileID, p.Recipe.String(), p.StoreIdentity, p.Recipe.Width, p.Height, columns, rows, p.Recipe.IntervalMS,
			p.Count, len(p.SheetBytes), p.Recipe.Bandwidth(p.SheetBytes), total, p.Decoder, p.Filled)
		if err != nil {
			return false, fmt.Errorf("publish trickplay: %w", err)
		}
		return true, nil
	})
}

// Outcome is how a generation that did not publish ends.
type Outcome int

const (
	// Failed counts a failure and backs off.
	Failed Outcome = iota
	// Unusable records that the file cannot yield sheets (no video, broken
	// data): it waits until the file changes.
	Unusable
	// Released returns the file to the queue after a delay without counting
	// a failure: the cause was this server's, such as a shutdown or no
	// transcode node to run on.
	Released
)

// Finish ends the claim on fileID without publishing, and queues any
// revision it uploaded for deletion. delay applies to Released. It reports
// false when the lease was lost. A file that is no longer eligible can still
// finish so its abandoned upload is retired promptly.
func (r *Repository) Finish(ctx context.Context, fileID int, leaseToken string, outcome Outcome, cause string, delay time.Duration) (bool, error) {
	return r.fenced(ctx, fileID, leaseToken, func(tx pgx.Tx, current fencedRow) (bool, error) {
		if current.workRevision != nil {
			if err := queueRevision(ctx, tx, fileID, *current.workRevision, nil); err != nil {
				return false, err
			}
		}
		state, failures, wait := statePending, current.failures, delay
		switch outcome {
		case Failed:
			failures++
			wait = backoffAfter(failures)
		case Unusable:
			state, failures, wait = stateUnusable, failures+1, 0
		}
		_, err := tx.Exec(ctx, `
			UPDATE public.media_file_trickplay
			SET state = $2, lease_owner = NULL, lease_expires_at = NULL, work_revision = NULL,
			    failure_count = $3, last_error = $4, available_at = now() + make_interval(secs => $5), updated_at = now()
			WHERE media_file_id = $1`,
			fileID, state, failures, cleanError(cause), wait.Seconds())
		if err != nil {
			return false, fmt.Errorf("finish trickplay work: %w", err)
		}
		return true, nil
	})
}

// fencedRow is a row the attempt still leases.
type fencedRow struct {
	revision     *int64
	workRevision *int64
	urlExpiresAt *time.Time
	failures     int
	eligible     bool
}

// fenced runs apply in a transaction holding fileID's row, if leaseToken
// still identifies the current attempt.
func (r *Repository) fenced(ctx context.Context, fileID int, leaseToken string, apply func(pgx.Tx, fencedRow) (bool, error)) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var row fencedRow
	err = tx.QueryRow(ctx, `
		SELECT t.revision, t.work_revision, t.published_expires_at, t.failure_count,
		       COALESCE(f.trickplay_enabled AND f.enabled IS NOT FALSE AND lower(btrim(f.type)) = ANY($3::text[])
		       AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		       AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0, false)
		FROM public.media_file_trickplay t
		JOIN public.media_files mf ON mf.id = t.media_file_id
		JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE t.media_file_id = $1 AND t.lease_owner = $2 AND t.state = 'running' AND t.lease_expires_at > now()
		FOR UPDATE OF t`, fileID, leaseToken, videoLibraryTypes).Scan(&row.revision, &row.workRevision, &row.urlExpiresAt, &row.failures, &row.eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock trickplay row: %w", err)
	}
	ok, err := apply(tx, row)
	if err != nil || !ok {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// queueRevision queues a revision's sheets for deletion after the grace.
func queueRevision(ctx context.Context, tx pgx.Tx, fileID int, revision int64, urlExpiresAt *time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO public.blob_gc_queue (prefix, not_before)
		VALUES ($1, GREATEST(now() + make_interval(secs => $2), $3::timestamptz))
		ON CONFLICT (prefix) DO UPDATE
		SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before)`,
		revisionPrefix(fileID, revision), abandonedGrace.Seconds(), urlExpiresAt)
	if err != nil {
		return fmt.Errorf("queue trickplay revision for deletion: %w", err)
	}
	return nil
}

// ProtectRevision keeps revision stored until the latest URL issued for it
// expires. It reports false if publication or deletion replaced the row, so
// a reader cannot return URLs for a displaced or no longer servable revision.
func (r *Repository) ProtectRevision(ctx context.Context, fileID int, revision int64, expiresAt time.Time, storeIdentity string) (bool, error) {
	// PostgreSQL stores timestamps with microsecond precision. Round up so
	// encoding cannot shorten the lifetime of an issued URL.
	rounded := expiresAt.Truncate(time.Microsecond)
	if expiresAt.After(rounded) {
		expiresAt = rounded.Add(time.Microsecond)
	}
	// A covered revision needs only a snapshot read. Replacing or deleting it
	// preserves this committed expiry in the retired revision's GC deadline.
	// The update retains GREATEST: simultaneous first readers may both extend
	// retention, and neither may shorten the other reader's issued URL lifetime.
	var protected bool
	err := r.pool.QueryRow(ctx, `
		WITH covered AS (
			SELECT t.media_file_id
			FROM public.media_file_trickplay t
			JOIN public.media_files mf ON mf.id = t.media_file_id
			JOIN public.media_folders f ON f.id = mf.media_folder_id
			WHERE t.media_file_id = $1 AND t.revision = $2 AND t.published_expires_at >= $3::timestamptz
			  AND f.trickplay_enabled AND f.enabled IS NOT FALSE AND t.store_identity = $4
			  AND lower(btrim(f.type)) = ANY($5::text[])
			  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
			  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
			  AND t.published_size IS NOT DISTINCT FROM mf.file_size
			  AND (t.published_hash IS NULL OR mf.file_hash IS NULL OR t.published_hash = mf.file_hash)
			  AND abs(COALESCE(t.published_duration, 0) - COALESCE(mf.duration, 0)) <= 2
		), extended AS (
			UPDATE public.media_file_trickplay t
			SET published_expires_at = GREATEST(t.published_expires_at, $3::timestamptz)
			FROM public.media_files mf JOIN public.media_folders f ON f.id = mf.media_folder_id
			WHERE t.media_file_id = $1 AND t.revision = $2 AND mf.id = t.media_file_id
			  AND f.trickplay_enabled AND f.enabled IS NOT FALSE AND t.store_identity = $4
			  AND lower(btrim(f.type)) = ANY($5::text[])
			  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
			  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
			  AND t.published_size IS NOT DISTINCT FROM mf.file_size
			  AND (t.published_hash IS NULL OR mf.file_hash IS NULL OR t.published_hash = mf.file_hash)
			  AND abs(COALESCE(t.published_duration, 0) - COALESCE(mf.duration, 0)) <= 2
			  AND NOT EXISTS (SELECT 1 FROM covered)
			RETURNING t.media_file_id
		)
		SELECT EXISTS (SELECT 1 FROM covered) OR EXISTS (SELECT 1 FROM extended)`, fileID, revision, expiresAt, storeIdentity, videoLibraryTypes).Scan(&protected)
	if err != nil {
		return false, fmt.Errorf("protect issued trickplay URLs: %w", err)
	}
	return protected, nil
}

func cleanError(message string) string {
	message = strings.ToValidUTF8(strings.ReplaceAll(message, "\x00", ""), "")
	if len(message) > 1000 {
		message = strings.ToValidUTF8(message[:1000], "")
	}
	return message
}

// ReconcileStats counts what one reconcile pass changed.
type ReconcileStats struct {
	Reclaimed int `json:"reclaimed"`
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Stale     int `json:"stale"`
}

// Reconcile brings the queue in line with the libraries, files, settings,
// and storage: it reclaims leases that ran out, adds files of opted-in
// libraries, removes rows of libraries that opted out or became ineligible
// (their sheets are queued for deletion), and requeues rows whose sheets no longer match
// their file, recipe, or storage. Each step handles at most batch rows.
func (r *Repository) Reconcile(ctx context.Context, recipe Recipe, storeIdentity string, batch int) (ReconcileStats, error) {
	return r.reconcile(ctx, r.pool.Exec, recipe, storeIdentity, batch)
}

// reconcile runs on the caller's session when an advisory lock guards the pass.
func (r *Repository) reconcile(ctx context.Context, exec func(context.Context, string, ...any) (pgconn.CommandTag, error), recipe Recipe, storeIdentity string, batch int) (ReconcileStats, error) {
	var stats ReconcileStats
	steps := []struct {
		count *int
		sql   string
		args  []any
	}{
		{&stats.Reclaimed, reclaimSQL, []any{backoffSeconds(), abandonedGrace.Seconds(), batch}},
		{&stats.Added, addSQL, []any{AlgorithmVersion, videoLibraryTypes, batch}},
		{&stats.Removed, removeSQL, []any{batch, videoLibraryTypes}},
		{&stats.Stale, staleSQL, []any{AlgorithmVersion, recipe.String(), storeIdentity, batch, videoLibraryTypes}},
	}
	for _, step := range steps {
		tag, err := exec(ctx, step.sql, step.args...)
		if err != nil {
			return stats, fmt.Errorf("reconcile trickplay: %w", err)
		}
		*step.count = int(tag.RowsAffected())
	}
	return stats, nil
}

func backoffSeconds() []float64 {
	seconds := make([]float64, len(failureBackoff))
	for i, d := range failureBackoff {
		seconds[i] = d.Seconds()
	}
	return seconds
}

// reclaimSQL returns expired leases to the queue as failures, queuing any
// revision they had started uploading.
const reclaimSQL = `
	WITH expired AS (
		SELECT media_file_id, work_revision, failure_count + 1 AS failures
		FROM public.media_file_trickplay
		WHERE state = 'running' AND lease_expires_at < now()
		ORDER BY lease_expires_at
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	), queued AS (
		INSERT INTO public.blob_gc_queue (prefix, not_before)
		SELECT 'trickplay/' || media_file_id || '/' || work_revision || '/', now() + make_interval(secs => $2)
		FROM expired WHERE work_revision IS NOT NULL
		ON CONFLICT (prefix) DO NOTHING
	)
	UPDATE public.media_file_trickplay t
	SET state = 'pending', lease_owner = NULL, lease_expires_at = NULL, work_revision = NULL,
	    failure_count = expired.failures, last_error = 'lease expired',
	    available_at = now() + make_interval(secs => ($1::float8[])[LEAST(expired.failures, cardinality($1::float8[]))]),
	    updated_at = now()
	FROM expired
	WHERE t.media_file_id = expired.media_file_id`

// addSQL queues the files of opted-in video libraries that have no row,
// newest first. A file needs a probe with a duration and a video stream.
const addSQL = `
	INSERT INTO public.media_file_trickplay (media_file_id, recipe_version)
	SELECT mf.id, $1
	FROM public.media_files mf
	JOIN public.media_folders f ON f.id = mf.media_folder_id
	WHERE f.trickplay_enabled AND f.enabled IS NOT FALSE
	  AND lower(btrim(f.type)) = ANY($2::text[])
	  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
	  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
	  AND NOT EXISTS (SELECT 1 FROM public.media_file_trickplay t WHERE t.media_file_id = mf.id)
	ORDER BY mf.id DESC
	LIMIT $3
	ON CONFLICT (media_file_id) DO NOTHING`

// removeSQL deletes the rows of libraries that opted out, are disabled, or
// no longer have a supported video type. The delete trigger queues their
// sheets. Rows being generated are left to finish or expire first.
const removeSQL = `
	DELETE FROM public.media_file_trickplay t
	WHERE t.state <> 'running' AND t.media_file_id IN (
		SELECT t2.media_file_id
		FROM public.media_file_trickplay t2
		JOIN public.media_files mf ON mf.id = t2.media_file_id
		JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE (NOT f.trickplay_enabled OR f.enabled IS FALSE OR NOT (lower(btrim(f.type)) = ANY($2::text[])))
		  AND t2.state <> 'running'
		LIMIT $1
	)`

// staleSQL requeues finished rows whose sheets no longer fit: made by an
// older algorithm, with other settings, for a file that has changed, or in
// another store. A row a newer algorithm touched is left to newer servers.
// A file's identity is its size, its hash when both sides have one, and its
// duration to within two seconds, which probes may round differently.
const staleSQL = `
	UPDATE public.media_file_trickplay t
	SET state = 'pending', available_at = now(), failure_count = 0, last_error = '',
	    recipe_version = $1, updated_at = now()
	FROM public.media_files mf JOIN public.media_folders f ON f.id = mf.media_folder_id
	WHERE mf.id = t.media_file_id
	  AND f.trickplay_enabled AND f.enabled IS NOT FALSE AND lower(btrim(f.type)) = ANY($5::text[])
	  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
	  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
	  AND t.state IN ('ready', 'unusable') AND t.recipe_version <= $1
	  AND t.media_file_id IN (
		SELECT t2.media_file_id
		FROM public.media_file_trickplay t2
		JOIN public.media_files mf2 ON mf2.id = t2.media_file_id
		JOIN public.media_folders f2 ON f2.id = mf2.media_folder_id
		WHERE t2.state IN ('ready', 'unusable') AND t2.recipe_version <= $1
		  AND f2.trickplay_enabled AND f2.enabled IS NOT FALSE AND lower(btrim(f2.type)) = ANY($5::text[])
		  AND mf2.missing_since IS NULL AND mf2.probe_updated_at IS NOT NULL AND mf2.duration > 0
		  AND jsonb_typeof(mf2.video_tracks) = 'array' AND jsonb_array_length(mf2.video_tracks) > 0
		  AND (
			t2.recipe_version < $1
			OR (t2.state = 'ready' AND (t2.published_recipe IS DISTINCT FROM $2 OR t2.store_identity IS DISTINCT FROM $3
				OR t2.published_size IS DISTINCT FROM mf2.file_size
				OR (t2.published_hash IS NOT NULL AND mf2.file_hash IS NOT NULL AND t2.published_hash <> mf2.file_hash)
				OR abs(COALESCE(t2.published_duration, 0) - COALESCE(mf2.duration, 0)) > 2))
			OR (t2.state = 'unusable' AND (t2.source_size IS DISTINCT FROM mf2.file_size
				OR (t2.source_hash IS NOT NULL AND mf2.file_hash IS NOT NULL AND t2.source_hash <> mf2.file_hash)
				OR abs(COALESCE(t2.source_duration, 0) - COALESCE(mf2.duration, 0)) > 2))
		  )
		LIMIT $4
	)`

// Manifests returns the servable manifests of fileIDs: published into the
// store named storeIdentity, from the file as it is now.
func (r *Repository) Manifests(ctx context.Context, fileIDs []int, storeIdentity string) (map[int]Manifest, error) {
	manifests := map[int]Manifest{}
	if len(fileIDs) == 0 {
		return manifests, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT t.media_file_id, t.revision, t.width, t.height, t.tile_columns, t.tile_rows, t.interval_ms,
		       t.thumbnail_count, t.sheet_count, COALESCE(t.bandwidth, 0)
		FROM public.media_file_trickplay t
		JOIN public.media_files mf ON mf.id = t.media_file_id
        JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE f.trickplay_enabled AND f.enabled IS NOT FALSE
		  AND lower(btrim(f.type)) = ANY($3::text[])
		  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
		  AND t.media_file_id = ANY($1) AND t.revision IS NOT NULL AND t.store_identity = $2
		  AND t.published_size IS NOT DISTINCT FROM mf.file_size
		  AND (t.published_hash IS NULL OR mf.file_hash IS NULL OR t.published_hash = mf.file_hash)
		  AND abs(COALESCE(t.published_duration, 0) - COALESCE(mf.duration, 0)) <= 2`,
		fileIDs, storeIdentity, videoLibraryTypes)
	if err != nil {
		return nil, fmt.Errorf("read trickplay manifests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m Manifest
		if err := rows.Scan(&m.FileID, &m.Revision, &m.Width, &m.Height, &m.TileColumns, &m.TileRows, &m.IntervalMS,
			&m.ThumbnailCount, &m.SheetCount, &m.Bandwidth); err != nil {
			return nil, err
		}
		manifests[m.FileID] = m
	}
	return manifests, rows.Err()
}

// Regenerate requeues fileIDs ahead of the backlog, clearing any backoff,
// and returns how many rows it requeued. Rows being generated are left
// alone; their current sheets keep serving until new ones publish. A row
// whose lease expired (its worker died) is taken back first, as the
// periodic reclaim would, so a regeneration does not wait for that pass.
func (r *Repository) Regenerate(ctx context.Context, fileIDs []int) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("requeue trickplay: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, regenerateReclaimSQL, fileIDs, abandonedGrace.Seconds()); err != nil {
		return 0, fmt.Errorf("reclaim expired trickplay leases: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO public.media_file_trickplay AS t (media_file_id, recipe_version, available_at)
		SELECT mf.id, $2, '-infinity'
		FROM public.media_files mf
		JOIN public.media_folders f ON f.id = mf.media_folder_id
		WHERE mf.id = ANY($1) AND f.trickplay_enabled AND f.enabled IS NOT FALSE
		  AND lower(btrim(f.type)) = ANY($3::text[])
		  AND mf.missing_since IS NULL AND mf.probe_updated_at IS NOT NULL AND mf.duration > 0
		  AND jsonb_typeof(mf.video_tracks) = 'array' AND jsonb_array_length(mf.video_tracks) > 0
		ON CONFLICT (media_file_id) DO UPDATE
		SET state = 'pending', available_at = '-infinity', failure_count = 0, last_error = '',
		    recipe_version = EXCLUDED.recipe_version, updated_at = now()
		WHERE t.state <> 'running' AND t.recipe_version <= $2`,
		fileIDs, AlgorithmVersion, videoLibraryTypes)
	if err != nil {
		return 0, fmt.Errorf("requeue trickplay: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("requeue trickplay: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// regenerateReclaimSQL takes back the expired leases of the given files
// without counting a failure, since the regeneration clears the backoff
// anyway, and queues any revision their work had started uploading.
const regenerateReclaimSQL = `
	WITH expired AS (
		SELECT media_file_id, work_revision
		FROM public.media_file_trickplay
		WHERE media_file_id = ANY($1) AND state = 'running' AND lease_expires_at < now()
		FOR UPDATE SKIP LOCKED
	), queued AS (
		INSERT INTO public.blob_gc_queue (prefix, not_before)
		SELECT 'trickplay/' || media_file_id || '/' || work_revision || '/', now() + make_interval(secs => $2)
		FROM expired WHERE work_revision IS NOT NULL
		ON CONFLICT (prefix) DO NOTHING
	)
	UPDATE public.media_file_trickplay t
	SET state = 'pending', lease_owner = NULL, lease_expires_at = NULL, work_revision = NULL, updated_at = now()
	FROM expired
	WHERE t.media_file_id = expired.media_file_id`

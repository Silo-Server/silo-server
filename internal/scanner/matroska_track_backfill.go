package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	matroskaTrackBackfillBatchSize = 200
	// Each candidate costs at least one remote read on a network mount, so a
	// few at a time keeps the backfill from competing with playback.
	matroskaTrackBackfillWorkers = 4
	// matroskaTrackBackfillMaxTimeouts consecutive timed-out reads mean the
	// storage is stalled, not that a few files are slow. The pass stops instead
	// of waiting out every remaining file, and the next run resumes.
	matroskaTrackBackfillMaxTimeouts = 8
	// matroskaTrackMatcherVersion is recorded with each check. Bump it when
	// the matching rules change, so files checked under the old rules are
	// read once more.
	matroskaTrackMatcherVersion = 1
)

// matroskaNativeTrackCodecs are the subtitle codecs, as FFmpeg names them,
// that a client plays from the stream by Matroska track number. Today that
// client is Android, which renders these two; a file whose tracks without an
// ID are all other codecs, such as PGS, gains nothing from a read. A file
// skipped for its codecs has no check, so adding a codec here makes it a
// candidate without a version bump.
var matroskaNativeTrackCodecs = []string{ffmpegCodecSubRip, ffmpegCodecASS}

// SQL shared by the statements that record a check. matroskaTrackProbeMD5
// hashes every column the match reads from the row: the video and audio
// tracks it counts and the subtitle tracks it matches. Its column names are
// unqualified so it reads the media_files row of each statement that uses it.
// matroskaTrackRowAsRead matches a row exactly as loadCandidates read it ($1
// ID, $2 file size, $3 mtime, $4 subtitle tracks, $5 probe hash).
const (
	matroskaTrackProbeMD5  = `md5(jsonb_build_array(video_tracks, audio_tracks, subtitle_tracks)::text)`
	matroskaTrackRowAsRead = `id = $1
		  AND file_size = $2
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $3::timestamptz
		  AND subtitle_tracks = $4::jsonb
		  AND ` + matroskaTrackProbeMD5 + ` = $5`
	matroskaTrackCheckInsert = `
		INSERT INTO matroska_track_backfill_checks
			(media_file_id, matcher_version, file_size, file_modified_at, probe_md5)`
	matroskaTrackCheckConflict = `
		ON CONFLICT (media_file_id) DO UPDATE SET
			matcher_version = EXCLUDED.matcher_version,
			file_size = EXCLUDED.file_size,
			file_modified_at = EXCLUDED.file_modified_at,
			probe_md5 = EXCLUDED.probe_md5,
			checked_at = now()`
)

// errMatroskaTrackStorageStalled ends a pass whose reads keep timing out.
var errMatroskaTrackStorageStalled = errors.New("media storage is not responding")

// MatroskaTrackBackfillResult counts what one backfill pass did. Updated,
// Unmatched, and Failed on the file's content are recorded, so the file is not
// read again until its row changes. Changed, TimedOut, and Failed on a storage
// error are read again on the next pass.
type MatroskaTrackBackfillResult struct {
	// Checked is the number of candidate files examined.
	Checked int `json:"checked"`
	// Updated is the number of files that gained at least one ID.
	Updated int `json:"updated"`
	// Unmatched files had a readable Tracks element that did not match the
	// probed streams with certainty, or matched without a corroborated codec.
	Unmatched int `json:"unmatched"`
	// Changed files differ on disk from their stored probe, or their row
	// changed during the pass. The next scan reprobes them and rewrites the
	// row, which the next pass then checks.
	Changed int `json:"changed"`
	// Failed files could not be read: storage failed to open or read them, or
	// they are not Matroska or their Tracks element cannot be decoded. Only
	// the content failures are recorded; storage may recover, as a mount that
	// comes up after the server does.
	Failed int `json:"failed"`
	// TimedOut files did not answer within the read timeout.
	TimedOut int `json:"timed_out"`
}

// MatroskaTrackBackfiller records Matroska TrackNumbers on subtitle tracks
// probed before the scanner read them. It reads only each file's Tracks
// element and leaves the rest of the probe alone.
//
// Candidates are present MKV files with a subtitle track in
// matroskaNativeTrackCodecs that lacks a container track ID and no check
// recorded for the row as it now stands. A pass is resumable by construction:
// a file drops out once it is checked, and an interrupted pass resumes with
// the files it did not reach. A check is recorded against the file's size and
// mtime and its stored tracks, so a file is read again only when a scan
// reprobes it or its tracks change.
type MatroskaTrackBackfiller struct {
	pool    *pgxpool.Pool
	workers int
	batch   int
}

// NewMatroskaTrackBackfiller returns a backfiller over repo's database, or nil
// when repo has none.
func NewMatroskaTrackBackfiller(repo *FileRepository) *MatroskaTrackBackfiller {
	if repo == nil || repo.pool == nil {
		return nil
	}
	return &MatroskaTrackBackfiller{pool: repo.pool, workers: matroskaTrackBackfillWorkers, batch: matroskaTrackBackfillBatchSize}
}

type matroskaTrackCandidate struct {
	id             int
	path           string
	size           int64
	modifiedAt     *time.Time
	videoTracks    int
	audioTracks    int
	subtitleTracks []models.SubtitleTrack
	subtitleJSON   []byte
	probeMD5       string
}

type matroskaTrackOutcome int

const (
	matroskaTrackOutcomeUpdated matroskaTrackOutcome = iota
	matroskaTrackOutcomeUnmatched
	matroskaTrackOutcomeChanged
	matroskaTrackOutcomeFailed
	matroskaTrackOutcomeTimedOut
)

// Run makes one pass over every candidate file. progress, when non-nil, is
// called after each batch with the running totals and the share of
// media_files IDs the pass has covered, from 0 to 100.
func (b *MatroskaTrackBackfiller) Run(ctx context.Context, progress func(MatroskaTrackBackfillResult, float64)) (MatroskaTrackBackfillResult, error) {
	var result MatroskaTrackBackfillResult
	if b == nil || b.pool == nil {
		return result, nil
	}
	var maxID int
	if err := b.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM media_files`).Scan(&maxID); err != nil {
		return result, fmt.Errorf("reading media_files id range: %w", err)
	}
	afterID := 0
	// Timeouts in a row, in completion order across all workers.
	consecutiveTimeouts := 0
	for {
		candidates, lastID, rowCount, err := b.loadCandidates(ctx, afterID)
		if err != nil {
			return result, err
		}
		if rowCount == 0 {
			return result, nil
		}
		afterID = lastID

		var mu sync.Mutex
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(b.workers)
		for _, candidate := range candidates {
			group.Go(func() error {
				outcome, err := b.backfillFile(groupCtx, candidate)
				if err != nil {
					return err
				}
				mu.Lock()
				defer mu.Unlock()
				result.Checked++
				if outcome == matroskaTrackOutcomeTimedOut {
					consecutiveTimeouts++
				} else {
					consecutiveTimeouts = 0
				}
				switch outcome {
				case matroskaTrackOutcomeUpdated:
					result.Updated++
				case matroskaTrackOutcomeUnmatched:
					result.Unmatched++
				case matroskaTrackOutcomeChanged:
					result.Changed++
				case matroskaTrackOutcomeFailed:
					result.Failed++
				case matroskaTrackOutcomeTimedOut:
					result.TimedOut++
				}
				if consecutiveTimeouts >= matroskaTrackBackfillMaxTimeouts {
					return fmt.Errorf("%w: %d Matroska track reads in a row timed out after %s",
						errMatroskaTrackStorageStalled, consecutiveTimeouts, matroskaTracksReadTimeout)
				}
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return result, err
		}
		if progress != nil && maxID > 0 {
			progress(result, min(100, float64(afterID)*100/float64(maxID)))
		}
		// Count rows, not decoded candidates: a row skipped for unreadable
		// JSON must not end the pass early.
		if rowCount < b.batch {
			return result, nil
		}
	}
}

// loadCandidates returns the next batch after afterID, the last row ID it
// read, and how many rows it read, which can exceed len(candidates) when a
// row's tracks do not decode.
func (b *MatroskaTrackBackfiller) loadCandidates(ctx context.Context, afterID int) (candidates []matroskaTrackCandidate, lastID, rowCount int, err error) {
	rows, err := b.pool.Query(ctx, `
		SELECT mf.id, mf.file_path, mf.file_size, mf.file_modified_at, mf.video_tracks, mf.audio_tracks, mf.subtitle_tracks,
		       `+matroskaTrackProbeMD5+`
		FROM media_files mf
		WHERE mf.id > $1
		  AND mf.container = 'mkv'
		  AND mf.missing_since IS NULL
		  AND mf.file_size IS NOT NULL
		  AND jsonb_typeof(mf.subtitle_tracks) = 'array'
		  AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(mf.subtitle_tracks) t
			WHERE COALESCE(t->>'container_track_id', '') = ''
			  AND lower(t->>'codec') = ANY($3::text[])
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM matroska_track_backfill_checks c
			WHERE c.media_file_id = mf.id
			  AND c.matcher_version = $4
			  AND c.file_size = mf.file_size
			  AND c.file_modified_at IS NOT DISTINCT FROM mf.file_modified_at
			  AND c.probe_md5 = `+matroskaTrackProbeMD5+`
		  )
		ORDER BY mf.id
		LIMIT $2`, afterID, b.batch, matroskaNativeTrackCodecs, matroskaTrackMatcherVersion)
	if err != nil {
		return nil, afterID, 0, fmt.Errorf("loading matroska track backfill candidates: %w", err)
	}
	defer rows.Close()
	lastID = afterID
	for rows.Next() {
		var (
			c                    matroskaTrackCandidate
			videoJSON, audioJSON []byte
			videoTracks          []json.RawMessage
			audioTracks          []json.RawMessage
		)
		if err := rows.Scan(&c.id, &c.path, &c.size, &c.modifiedAt, &videoJSON, &audioJSON, &c.subtitleJSON, &c.probeMD5); err != nil {
			return nil, afterID, 0, fmt.Errorf("scanning matroska track backfill candidate: %w", err)
		}
		lastID = c.id
		rowCount++
		// Only the counts matter for the match; a malformed column leaves the
		// count at zero, which then fails the match instead of guessing.
		_ = json.Unmarshal(videoJSON, &videoTracks)
		_ = json.Unmarshal(audioJSON, &audioTracks)
		if err := json.Unmarshal(c.subtitleJSON, &c.subtitleTracks); err != nil {
			slog.WarnContext(ctx, "scanner: skipping file with unreadable subtitle_tracks",
				"component", "scanner", "file_id", c.id, "error", err)
			continue
		}
		c.videoTracks, c.audioTracks = len(videoTracks), len(audioTracks)
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, afterID, 0, fmt.Errorf("iterating matroska track backfill candidates: %w", err)
	}
	return candidates, lastID, rowCount, nil
}

// backfillFile returns an error for a database failure or for storage too
// stalled to take another read, either of which ends the pass. A file that
// cannot be read or matched is an outcome, not an error, and is recorded
// unless reading it again could give a different result.
func (b *MatroskaTrackBackfiller) backfillFile(ctx context.Context, c matroskaTrackCandidate) (matroskaTrackOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tracks, info, err := readMatroskaTracks(ctx, c.path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	if errors.Is(err, errMatroskaTracksReadBusy) {
		// Earlier reads are still stuck on storage. Reading more would only
		// add to them, so the pass ends here and the next run resumes.
		return 0, fmt.Errorf("%w: %w", errMatroskaTrackStorageStalled, err)
	}
	if errors.Is(err, errMatroskaTracksReadTimeout) {
		slog.WarnContext(ctx, "scanner: Matroska track read timed out",
			"component", "scanner", "file_id", c.id, "path", c.path, "timeout", matroskaTracksReadTimeout)
		return matroskaTrackOutcomeTimedOut, nil
	}
	// The stored tracks describe the file as it was probed. A file that has
	// changed since is matched by its next scan, not against stale streams.
	// A row stored without an mtime can only be checked on size. Nothing is
	// recorded: the difference is what this read saw on disk, and the scan
	// that rewrites the row settles it.
	if info != nil && (info.Size() != c.size || (c.modifiedAt != nil && !sameFileModifiedAt(c.modifiedAt, info.ModTime()))) {
		return matroskaTrackOutcomeChanged, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska track backfill could not read file",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		// Open, stat and read failures come from storage, which may recover;
		// the file stays a candidate. Any other error is the file's content.
		if _, ok := errors.AsType[*fs.PathError](err); ok {
			return matroskaTrackOutcomeFailed, nil
		}
		return b.recordCheck(ctx, c, matroskaTrackOutcomeFailed)
	}

	subtitles := make([]matroskaSubtitleStream, len(c.subtitleTracks))
	for i, track := range c.subtitleTracks {
		subtitles[i] = matroskaSubtitleStream{Index: track.Index, Codec: track.Codec}
	}
	ids, err := matroskaSubtitleTrackIDs(tracks, c.videoTracks, c.audioTracks, subtitles)
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska subtitle track numbers not recorded",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		return b.recordCheck(ctx, c, matroskaTrackOutcomeUnmatched)
	}

	changed := false
	for i, track := range c.subtitleTracks {
		if track.ContainerTrackID != "" {
			ids[i] = ""
		}
		if ids[i] != "" {
			changed = true
		}
	}
	if !changed {
		return b.recordCheck(ctx, c, matroskaTrackOutcomeUnmatched)
	}
	// Set only container_track_id on each element, so fields this binary does
	// not know survive. Write only over the exact row that was read: the same
	// file revision and the same stored tracks. A scan that rewrote the row
	// in the meantime already recorded its own IDs. The check is recorded
	// against the row as written, so a track left without an ID, such as a
	// legacy S_ASS one, does not bring the file back.
	tag, err := b.pool.Exec(ctx, `
		WITH updated AS (
			UPDATE media_files
			SET subtitle_tracks = (
				SELECT jsonb_agg(
					CASE WHEN COALESCE(($6::text[])[e.ord::int], '') = '' THEN e.elem
					     ELSE jsonb_set(e.elem, '{container_track_id}', to_jsonb(($6::text[])[e.ord::int]))
					END ORDER BY e.ord)
				FROM jsonb_array_elements(media_files.subtitle_tracks) WITH ORDINALITY AS e(elem, ord)
			    ),
			    updated_at = NOW()
			WHERE `+matroskaTrackRowAsRead+`
			  AND jsonb_array_length(subtitle_tracks) = cardinality($6::text[])
			RETURNING id, file_size, file_modified_at, video_tracks, audio_tracks, subtitle_tracks
		)`+matroskaTrackCheckInsert+`
		SELECT id, $7::int, file_size, file_modified_at, `+matroskaTrackProbeMD5+` FROM updated`+
		matroskaTrackCheckConflict,
		c.id, c.size, c.normalizedModifiedAt(), c.subtitleJSON, c.probeMD5, ids, matroskaTrackMatcherVersion)
	if err != nil {
		return 0, fmt.Errorf("updating subtitle_tracks for file %d: %w", c.id, err)
	}
	if tag.RowsAffected() == 0 {
		// The next pass checks the row's new revision.
		return matroskaTrackOutcomeChanged, nil
	}
	return matroskaTrackOutcomeUpdated, nil
}

// recordCheck records that c's row, as it was read, has been checked, and
// returns outcome. It records nothing when the row has changed since, or has
// been deleted: a changed row is a candidate again.
func (b *MatroskaTrackBackfiller) recordCheck(ctx context.Context, c matroskaTrackCandidate, outcome matroskaTrackOutcome) (matroskaTrackOutcome, error) {
	_, err := b.pool.Exec(ctx, matroskaTrackCheckInsert+`
		SELECT id, $6::int, file_size, file_modified_at, $5
		FROM media_files
		WHERE `+matroskaTrackRowAsRead+matroskaTrackCheckConflict,
		c.id, c.size, c.normalizedModifiedAt(), c.subtitleJSON, c.probeMD5, matroskaTrackMatcherVersion)
	// A file deleted after the insert read its row fails the foreign key.
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23503" {
		return outcome, nil
	}
	if err != nil {
		return 0, fmt.Errorf("recording Matroska track check for file %d: %w", c.id, err)
	}
	return outcome, nil
}

// normalizedModifiedAt is the stored mtime in the form row comparisons use.
func (c matroskaTrackCandidate) normalizedModifiedAt() *time.Time {
	if c.modifiedAt == nil {
		return nil
	}
	normalized := models.NormalizeFileModifiedAt(*c.modifiedAt)
	return &normalized
}

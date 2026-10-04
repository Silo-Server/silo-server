package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
)

// errMatroskaTrackStorageStalled ends a pass whose reads keep timing out.
var errMatroskaTrackStorageStalled = errors.New("media storage is not responding")

// MatroskaTrackBackfillResult counts what one backfill pass did.
type MatroskaTrackBackfillResult struct {
	// Checked is the number of candidate files examined.
	Checked int `json:"checked"`
	// Updated is the number of files that gained at least one ID.
	Updated int `json:"updated"`
	// Unmatched files had a readable Tracks element that did not match the
	// probed streams with certainty, or matched without a corroborated codec.
	Unmatched int `json:"unmatched"`
	// Changed files differ on disk from their stored probe, or their row
	// changed during the pass. The next scan reprobes them.
	Changed int `json:"changed"`
	// Failed files could not be read.
	Failed int `json:"failed"`
	// TimedOut files did not answer within the read timeout.
	TimedOut int `json:"timed_out"`
}

// MatroskaTrackBackfiller records Matroska TrackNumbers on subtitle tracks
// probed before the scanner read them. It reads only each file's Tracks
// element and leaves the rest of the probe alone.
//
// A pass is resumable by construction: candidates are present MKV files with
// an embedded subtitle track lacking a container track ID, so a file drops out
// once it gains its IDs. Files that cannot be matched stay candidates and are
// read again on the next pass, which costs one small read each.
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
		SELECT id, file_path, file_size, file_modified_at, video_tracks, audio_tracks, subtitle_tracks
		FROM media_files
		WHERE id > $1
		  AND container = 'mkv'
		  AND missing_since IS NULL
		  AND jsonb_typeof(subtitle_tracks) = 'array'
		  AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(subtitle_tracks) t
			WHERE COALESCE(t->>'container_track_id', '') = ''
		  )
		ORDER BY id
		LIMIT $2`, afterID, b.batch)
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
		if err := rows.Scan(&c.id, &c.path, &c.size, &c.modifiedAt, &videoJSON, &audioJSON, &c.subtitleJSON); err != nil {
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

// backfillFile returns an error only for a database failure, which ends the
// pass. A file that cannot be read or matched is an outcome, not an error.
func (b *MatroskaTrackBackfiller) backfillFile(ctx context.Context, c matroskaTrackCandidate) (matroskaTrackOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tracks, info, err := readMatroskaTracks(ctx, c.path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	if errors.Is(err, errMatroskaTracksReadTimeout) {
		slog.WarnContext(ctx, "scanner: Matroska track read timed out",
			"component", "scanner", "file_id", c.id, "path", c.path, "timeout", matroskaTracksReadTimeout)
		return matroskaTrackOutcomeTimedOut, nil
	}
	// The stored tracks describe the file as it was probed. A file that has
	// changed since is matched by its next scan, not against stale streams.
	// A row stored without an mtime can only be checked on size.
	if info != nil && (info.Size() != c.size || (c.modifiedAt != nil && !sameFileModifiedAt(c.modifiedAt, info.ModTime()))) {
		return matroskaTrackOutcomeChanged, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska track backfill could not read file",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		return matroskaTrackOutcomeFailed, nil
	}

	subtitles := make([]matroskaSubtitleStream, len(c.subtitleTracks))
	for i, track := range c.subtitleTracks {
		subtitles[i] = matroskaSubtitleStream{Index: track.Index, Codec: track.Codec}
	}
	ids, err := matroskaSubtitleTrackIDs(tracks, c.videoTracks, c.audioTracks, subtitles)
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska subtitle track numbers not recorded",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		return matroskaTrackOutcomeUnmatched, nil
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
		return matroskaTrackOutcomeUnmatched, nil
	}
	// Set only container_track_id on each element, so fields this binary does
	// not know survive. Write only over the exact row that was read: the same
	// file revision and the same subtitle tracks. A scan that rewrote the row
	// in the meantime already recorded its own IDs.
	var modifiedAt *time.Time
	if c.modifiedAt != nil {
		normalized := models.NormalizeFileModifiedAt(*c.modifiedAt)
		modifiedAt = &normalized
	}
	tag, err := b.pool.Exec(ctx, `
		UPDATE media_files
		SET subtitle_tracks = (
			SELECT jsonb_agg(
				CASE WHEN COALESCE(($2::text[])[e.ord::int], '') = '' THEN e.elem
				     ELSE jsonb_set(e.elem, '{container_track_id}', to_jsonb(($2::text[])[e.ord::int]))
				END ORDER BY e.ord)
			FROM jsonb_array_elements(media_files.subtitle_tracks) WITH ORDINALITY AS e(elem, ord)
		    ),
		    updated_at = NOW()
		WHERE id = $1
		  AND file_size = $3
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $4::timestamptz
		  AND subtitle_tracks = $5::jsonb
		  AND jsonb_array_length(subtitle_tracks) = cardinality($2::text[])`,
		c.id, ids, c.size, modifiedAt, c.subtitleJSON)
	if err != nil {
		return 0, fmt.Errorf("updating subtitle_tracks for file %d: %w", c.id, err)
	}
	if tag.RowsAffected() == 0 {
		return matroskaTrackOutcomeChanged, nil
	}
	return matroskaTrackOutcomeUpdated, nil
}

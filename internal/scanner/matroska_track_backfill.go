package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
)

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
)

// Run makes one pass over every candidate file. progress, when non-nil, is
// called after each batch with the running totals.
func (b *MatroskaTrackBackfiller) Run(ctx context.Context, progress func(MatroskaTrackBackfillResult)) (MatroskaTrackBackfillResult, error) {
	var result MatroskaTrackBackfillResult
	if b == nil || b.pool == nil {
		return result, nil
	}
	afterID := 0
	for {
		candidates, err := b.loadCandidates(ctx, afterID)
		if err != nil {
			return result, err
		}
		if len(candidates) == 0 {
			return result, nil
		}
		afterID = candidates[len(candidates)-1].id

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
				switch outcome {
				case matroskaTrackOutcomeUpdated:
					result.Updated++
				case matroskaTrackOutcomeUnmatched:
					result.Unmatched++
				case matroskaTrackOutcomeChanged:
					result.Changed++
				case matroskaTrackOutcomeFailed:
					result.Failed++
				}
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return result, err
		}
		if progress != nil {
			progress(result)
		}
		if len(candidates) < b.batch {
			return result, nil
		}
	}
}

func (b *MatroskaTrackBackfiller) loadCandidates(ctx context.Context, afterID int) ([]matroskaTrackCandidate, error) {
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
		return nil, fmt.Errorf("loading matroska track backfill candidates: %w", err)
	}
	defer rows.Close()
	var candidates []matroskaTrackCandidate
	for rows.Next() {
		var (
			c                    matroskaTrackCandidate
			videoJSON, audioJSON []byte
			videoTracks          []json.RawMessage
			audioTracks          []json.RawMessage
		)
		if err := rows.Scan(&c.id, &c.path, &c.size, &c.modifiedAt, &videoJSON, &audioJSON, &c.subtitleJSON); err != nil {
			return nil, fmt.Errorf("scanning matroska track backfill candidate: %w", err)
		}
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
		return nil, fmt.Errorf("iterating matroska track backfill candidates: %w", err)
	}
	return candidates, nil
}

// backfillFile returns an error only for a database failure, which ends the
// pass. A file that cannot be read or matched is an outcome, not an error.
func (b *MatroskaTrackBackfiller) backfillFile(ctx context.Context, c matroskaTrackCandidate) (matroskaTrackOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := os.Stat(c.path)
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska track backfill could not stat file",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		return matroskaTrackOutcomeFailed, nil
	}
	// The stored tracks describe the file as it was probed. A file that has
	// changed since is matched by its next scan, not against stale streams.
	if info.Size() != c.size || !sameFileModifiedAt(c.modifiedAt, info.ModTime()) {
		return matroskaTrackOutcomeChanged, nil
	}

	subtitles := make([]matroskaSubtitleStream, len(c.subtitleTracks))
	for i, track := range c.subtitleTracks {
		subtitles[i] = matroskaSubtitleStream{Index: track.Index, Codec: track.Codec}
	}
	ids, err := readMatroskaSubtitleTrackIDs(c.path, c.videoTracks, c.audioTracks, subtitles)
	if err != nil {
		slog.DebugContext(ctx, "scanner: Matroska subtitle track numbers not recorded",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		if errors.Is(err, errMatroskaLayoutMismatch) {
			return matroskaTrackOutcomeUnmatched, nil
		}
		return matroskaTrackOutcomeFailed, nil
	}

	changed := false
	tracks := make([]models.SubtitleTrack, len(c.subtitleTracks))
	copy(tracks, c.subtitleTracks)
	for i := range tracks {
		if tracks[i].ContainerTrackID == "" && ids[i] != "" {
			tracks[i].ContainerTrackID = ids[i]
			changed = true
		}
	}
	if !changed {
		return matroskaTrackOutcomeUnmatched, nil
	}
	data, err := json.Marshal(tracks)
	if err != nil {
		return 0, fmt.Errorf("marshaling subtitle_tracks for file %d: %w", c.id, err)
	}
	// Write only over the exact row that was read: the same file revision and
	// the same subtitle tracks. A scan that rewrote the row in the meantime
	// already recorded its own IDs.
	var modifiedAt *time.Time
	if c.modifiedAt != nil {
		normalized := models.NormalizeFileModifiedAt(*c.modifiedAt)
		modifiedAt = &normalized
	}
	tag, err := b.pool.Exec(ctx, `
		UPDATE media_files
		SET subtitle_tracks = $2::jsonb,
		    updated_at = NOW()
		WHERE id = $1
		  AND file_size = $3
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $4::timestamptz
		  AND subtitle_tracks = $5::jsonb`,
		c.id, data, c.size, modifiedAt, c.subtitleJSON)
	if err != nil {
		return 0, fmt.Errorf("updating subtitle_tracks for file %d: %w", c.id, err)
	}
	if tag.RowsAffected() == 0 {
		return matroskaTrackOutcomeChanged, nil
	}
	return matroskaTrackOutcomeUpdated, nil
}

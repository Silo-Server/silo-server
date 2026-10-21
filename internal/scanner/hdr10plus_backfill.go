package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	hdr10PlusBackfillBatchSize = 200
	// Each candidate decodes one 4K frame from what is often a network mount,
	// so a few at a time keeps the backfill from competing with playback.
	hdr10PlusBackfillWorkers = 4
	// hdr10PlusBackfillMaxTimeouts consecutive timed-out reads mean the
	// storage is stalled, not that a few files are slow. The pass stops instead
	// of waiting out every remaining file, and the next run resumes.
	hdr10PlusBackfillMaxTimeouts = 8
	// hdr10PlusPrimaryVideoStream selects the first video stream that is not
	// cover art, the stream the scanner stores as the first video track.
	hdr10PlusPrimaryVideoStream = "V:0"
)

// errHDR10PlusStorageStalled ends a pass whose frame reads keep timing out.
var errHDR10PlusStorageStalled = errors.New("media storage is not responding")

// hdr10PlusStatTimeout bounds the on-disk check after a frame read.
const hdr10PlusStatTimeout = 5 * time.Second

// hdr10PlusStatSlots caps on-disk checks in flight, stuck ones included. It
// leaves room above the worker count so one stuck check doesn't end a pass on
// storage that is otherwise answering; truly stalled storage still ends it
// through the timed-out streak.
const hdr10PlusStatSlots = 4 * hdr10PlusBackfillWorkers

var (
	errHDR10PlusStatTimeout = errors.New("file check did not finish in time; the storage may be stalled")
	errHDR10PlusStatBusy    = errors.New("file checks are already waiting on stalled storage")
)

// HDR10PlusBackfillResult counts what one backfill pass did. HDR10Plus and
// NotHDR10Plus are recorded on the file, so it is not read again until a scan
// rewrites its row. Changed, Unreadable, and TimedOut are read again on the
// next pass.
type HDR10PlusBackfillResult struct {
	// Checked is the number of candidate files examined.
	Checked int `json:"checked"`
	// HDR10Plus files carry HDR10+ metadata on their first frame.
	HDR10Plus int `json:"hdr10_plus"`
	// NotHDR10Plus files have a first frame without HDR10+ metadata.
	NotHDR10Plus int `json:"not_hdr10_plus"`
	// Changed files differ on disk from their row, or their row was rewritten
	// during the pass. The next scan or pass settles them.
	Changed int `json:"changed"`
	// Unreadable files could not be opened or decoded, or ffprobe failed.
	// None is recorded as not HDR10+: a misconfigured ffprobe would otherwise
	// mark the whole library that way.
	Unreadable int `json:"unreadable"`
	// TimedOut files did not answer within the frame read timeout.
	TimedOut int `json:"timed_out"`
}

// HDR10PlusBackfiller records HDR10+ status on video probed before the
// scanner read it from a decoded frame. FFmpeg reports HDR10+ only as frame
// side data, so earlier probes stored every HDR10+ file as plain HDR10.
//
// Candidates are present files whose first video track is PQ and has no
// hdr10_plus key. The backfiller decodes the first frame of the primary video
// stream and sets only hdr10_plus and, for an HDR10+ file, video_range_type on
// that track. It leaves the rest of the probe alone, so chapters, probe time,
// and everything keyed to them stay as they are. A pass is resumable by
// construction: a file drops out once its track has the key, and a pass that
// stops early resumes with the files it did not reach.
type HDR10PlusBackfiller struct {
	pool        *pgxpool.Pool
	ffprobePath string
	workers     int
	batch       int
	// readFrame is the frame read and statFile the on-disk check; tests
	// replace them.
	readFrame func(ctx context.Context, ffprobePath, filePath, streamSpecifier string) ([]ffprobeSideData, error)
	statFile  func(path string) (fs.FileInfo, error)
	// statSlots caps on-disk checks still blocked in the kernel; see
	// statFileBounded.
	statSlots   chan struct{}
	statTimeout time.Duration
}

// NewHDR10PlusBackfiller returns a backfiller over repo's database that reads
// frames with ffprobePath, or nil when repo has none.
func NewHDR10PlusBackfiller(repo *FileRepository, ffprobePath string) *HDR10PlusBackfiller {
	if repo == nil || repo.pool == nil {
		return nil
	}
	return &HDR10PlusBackfiller{
		pool:        repo.pool,
		ffprobePath: ffprobePath,
		workers:     hdr10PlusBackfillWorkers,
		batch:       hdr10PlusBackfillBatchSize,
		readFrame:   probeVideoFrameSideData,
		statFile:    os.Stat,
		statSlots:   make(chan struct{}, hdr10PlusStatSlots),
		statTimeout: hdr10PlusStatTimeout,
	}
}

type hdr10PlusCandidate struct {
	id         int
	path       string
	size       int64
	modifiedAt *time.Time
	track      models.VideoTrack
	trackJSON  []byte
}

type hdr10PlusOutcome int

const (
	hdr10PlusOutcomeHDR10Plus hdr10PlusOutcome = iota
	hdr10PlusOutcomeNotHDR10Plus
	hdr10PlusOutcomeChanged
	hdr10PlusOutcomeUnreadable
	hdr10PlusOutcomeTimedOut
)

// Run makes one pass over every candidate file. progress, when non-nil, is
// called after each batch with the running totals and the share of
// media_files IDs the pass has covered, from 0 to 100.
func (b *HDR10PlusBackfiller) Run(ctx context.Context, progress func(HDR10PlusBackfillResult, float64)) (HDR10PlusBackfillResult, error) {
	var result HDR10PlusBackfillResult
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
			logHDR10PlusUnreadable(ctx, result)
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
				if outcome == hdr10PlusOutcomeTimedOut {
					consecutiveTimeouts++
				} else {
					consecutiveTimeouts = 0
				}
				switch outcome {
				case hdr10PlusOutcomeHDR10Plus:
					result.HDR10Plus++
				case hdr10PlusOutcomeNotHDR10Plus:
					result.NotHDR10Plus++
				case hdr10PlusOutcomeChanged:
					result.Changed++
				case hdr10PlusOutcomeUnreadable:
					result.Unreadable++
				case hdr10PlusOutcomeTimedOut:
					result.TimedOut++
				}
				if consecutiveTimeouts >= hdr10PlusBackfillMaxTimeouts {
					return fmt.Errorf("%w: %d HDR10+ frame reads in a row timed out after %s",
						errHDR10PlusStorageStalled, consecutiveTimeouts, frameSideDataProbeTimeout)
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
			logHDR10PlusUnreadable(ctx, result)
			return result, nil
		}
	}
}

// loadCandidates returns the next batch after afterID, the last row ID it
// read, and how many rows it read, which can exceed len(candidates) when a
// row's first video track does not decode.
func (b *HDR10PlusBackfiller) loadCandidates(ctx context.Context, afterID int) (candidates []hdr10PlusCandidate, lastID, rowCount int, err error) {
	rows, err := b.pool.Query(ctx, `
		SELECT mf.id, mf.file_path, mf.file_size, mf.file_modified_at, mf.video_tracks->0
		FROM media_files mf
		WHERE mf.id > $1
		  AND mf.missing_since IS NULL
		  AND mf.file_size IS NOT NULL
		  AND jsonb_typeof(mf.video_tracks) = 'array'
		  AND jsonb_typeof(mf.video_tracks->0) = 'object'
		  AND lower(COALESCE(mf.video_tracks->0->>'color_transfer', '')) LIKE '%smpte2084%'
		  AND NOT (mf.video_tracks->0 ? 'hdr10_plus')
		ORDER BY mf.id
		LIMIT $2`, afterID, b.batch)
	if err != nil {
		return nil, afterID, 0, fmt.Errorf("loading HDR10+ backfill candidates: %w", err)
	}
	defer rows.Close()
	lastID = afterID
	for rows.Next() {
		var c hdr10PlusCandidate
		if err := rows.Scan(&c.id, &c.path, &c.size, &c.modifiedAt, &c.trackJSON); err != nil {
			return nil, afterID, 0, fmt.Errorf("scanning HDR10+ backfill candidate: %w", err)
		}
		lastID = c.id
		rowCount++
		if err := json.Unmarshal(c.trackJSON, &c.track); err != nil {
			slog.WarnContext(ctx, "scanner: skipping file with an unreadable video track",
				"component", "scanner", "file_id", c.id, "error", err)
			continue
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, afterID, 0, fmt.Errorf("iterating HDR10+ backfill candidates: %w", err)
	}
	return candidates, lastID, rowCount, nil
}

// backfillFile returns an error for a database failure or a canceled pass,
// either of which ends the pass. A file that cannot be read is an outcome,
// not an error, and is recorded unless reading it again could give a
// different result.
func (b *HDR10PlusBackfiller) backfillFile(ctx context.Context, c hdr10PlusCandidate) (hdr10PlusOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sideData, err := b.readFrame(ctx, b.ffprobePath, c.path, hdr10PlusPrimaryVideoStream)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	if errors.Is(err, errFrameSideDataTimeout) {
		slog.WarnContext(ctx, "scanner: HDR10+ frame read timed out",
			"component", "scanner", "file_id", c.id, "path", c.path, "timeout", frameSideDataProbeTimeout)
		return hdr10PlusOutcomeTimedOut, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: HDR10+ backfill could not read the first frame",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		return hdr10PlusOutcomeUnreadable, nil
	}
	// The frame describes the file on disk, which a scan has not reprobed if
	// it changed since the row was written. Such a file is settled by that
	// scan, not by writing its new status onto the old row. A row stored
	// without an mtime can only be checked on size.
	info, err := b.statFileBounded(ctx, c.path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	if errors.Is(err, errHDR10PlusStatBusy) {
		// Earlier checks are still stuck on storage. Checking more would
		// only add to them, so the pass ends here and the next run resumes.
		return 0, fmt.Errorf("%w: %w", errHDR10PlusStorageStalled, err)
	}
	if errors.Is(err, errHDR10PlusStatTimeout) {
		slog.WarnContext(ctx, "scanner: HDR10+ backfill file check timed out",
			"component", "scanner", "file_id", c.id, "path", c.path, "timeout", b.statTimeout)
		return hdr10PlusOutcomeTimedOut, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: HDR10+ backfill could not stat the file",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		return hdr10PlusOutcomeUnreadable, nil
	}
	if info.Size() != c.size || (c.modifiedAt != nil && !sameFileModifiedAt(c.modifiedAt, info.ModTime())) {
		return hdr10PlusOutcomeChanged, nil
	}
	outcome := hdr10PlusOutcomeNotHDR10Plus
	if mediaprobe.HasHDR10Plus(sideData) {
		outcome = hdr10PlusOutcomeHDR10Plus
	}

	hdr10Plus := outcome == hdr10PlusOutcomeHDR10Plus
	// Set only hdr10_plus, and for an HDR10+ file video_range_type, on the
	// first track, so fields this binary does not know survive. Write only
	// over the exact row that was read: the same file revision and the same
	// first track. A scan that rewrote the row in the meantime read the frame
	// itself.
	tag, err := b.pool.Exec(ctx, `
		UPDATE media_files
		SET video_tracks = CASE WHEN $5::boolean
		        THEN jsonb_set(jsonb_set(video_tracks, '{0,hdr10_plus}', 'true'::jsonb), '{0,video_range_type}', to_jsonb($6::text))
		        ELSE jsonb_set(video_tracks, '{0,hdr10_plus}', 'false'::jsonb)
		    END,
		    updated_at = NOW()
		WHERE id = $1
		  AND file_size = $2
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $3::timestamptz
		  AND video_tracks->0 = $4::jsonb`,
		c.id, c.size, c.normalizedModifiedAt(), c.trackJSON, hdr10Plus, mediaprobe.RangeTypeWithHDR10Plus(c.track))
	if err != nil {
		return 0, fmt.Errorf("recording HDR10+ status for file %d: %w", c.id, err)
	}
	if tag.RowsAffected() == 0 {
		return hdr10PlusOutcomeChanged, nil
	}
	return outcome, nil
}

// statFileBounded runs statFile off the worker's goroutine, bounded by ctx and
// statTimeout, because a stat on a stalled network mount can block in the
// kernel indefinitely. A blocked syscall cannot be canceled, so the goroutine
// of a timed-out check lingers until the kernel returns; statSlots caps how
// many can, and a check holds its slot until its syscall returns. When every
// slot is taken the check is skipped rather than queued behind the stall.
func (b *HDR10PlusBackfiller) statFileBounded(ctx context.Context, path string) (fs.FileInfo, error) {
	slots, stat, timeout := b.statSlots, b.statFile, b.statTimeout
	select {
	case slots <- struct{}{}:
	default:
		return nil, errHDR10PlusStatBusy
	}
	type statResult struct {
		info fs.FileInfo
		err  error
	}
	done := make(chan statResult, 1)
	go func() {
		info, err := stat(path)
		// Free the slot before publishing: a caller that receives the
		// answer and immediately checks again must find it available.
		<-slots
		done <- statResult{info, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.info, result.err
	case <-timer.C:
		return nil, errHDR10PlusStatTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// logHDR10PlusUnreadable warns once per pass about files it could not read,
// so a misconfigured ffprobe or unreachable storage is visible.
func logHDR10PlusUnreadable(ctx context.Context, result HDR10PlusBackfillResult) {
	if result.Unreadable > 0 {
		slog.WarnContext(ctx, "scanner: HDR10+ backfill could not read some files; they are read again on the next pass",
			"component", "scanner", "unreadable", result.Unreadable, "checked", result.Checked)
	}
}

// normalizedModifiedAt is the stored mtime in the form row comparisons use.
func (c hdr10PlusCandidate) normalizedModifiedAt() *time.Time {
	if c.modifiedAt == nil {
		return nil
	}
	normalized := models.NormalizeFileModifiedAt(*c.modifiedAt)
	return &normalized
}

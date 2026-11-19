package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

const (
	attachmentBitrateBackfillBatchSize = 200
	// Each candidate costs one header read, often on a network mount, so a
	// few at a time keeps the pass from competing with playback.
	attachmentBitrateBackfillWorkers = 4
	// attachmentBitrateBackfillMaxFailures probes in a row that timed out or
	// could not read their file mean the storage is stalled or ffprobe cannot
	// run, not that a few files are bad. The pass stops without moving its
	// cursor past the batch, and the next run resumes there.
	attachmentBitrateBackfillMaxFailures = 8
	// attachmentBitrateProbeTimeout bounds one header read: format and stream
	// entries only, no packets or frames.
	attachmentBitrateProbeTimeout = 30 * time.Second
)

// attachmentBitrateBackfillSubtitleCodecs are the subtitle codecs, as FFmpeg
// names them, of the releases that embed fonts: ASS and SSA render with the
// fonts attached to the file. Other MKV files rarely carry attachments large
// enough to move their rate, so they are not read.
var attachmentBitrateBackfillSubtitleCodecs = []string{ffmpegCodecASS, "ssa"}

var (
	errAttachmentBitrateStorageStalled = errors.New("media storage is not responding or ffprobe cannot read it")
	errAttachmentBitrateProbeTimeout   = errors.New("ffprobe timed out")
)

// AttachmentBitrateBackfillResult counts what one pass did.
type AttachmentBitrateBackfillResult struct {
	// Checked is the number of candidate files probed.
	Checked int `json:"checked"`
	// Updated files had their stored bitrate corrected.
	Updated int `json:"updated"`
	// Unchanged files already stored the attachment-free rate.
	Unchanged int `json:"unchanged"`
	// Changed files differ on disk from their row, or their row was
	// rewritten during the pass. The scan that reprobes them stores the
	// corrected rate itself.
	Changed int `json:"changed"`
	// Failed files could not be probed. They keep their stored rate until a
	// scan reprobes them.
	Failed int `json:"failed"`
	// TimedOut files did not answer within attachmentBitrateProbeTimeout.
	TimedOut int `json:"timed_out"`
	// Done reports that the pass has covered every file, now or on an
	// earlier run.
	Done bool `json:"done"`
}

// AttachmentBitrateBackfiller corrects the stored bitrate of MKV files scanned
// while their attachments still counted toward it (see formatBitrateKbps).
// Normal rescans skip unchanged files, so without it a font-heavy release
// keeps a rate up to twice its real one, which pushes it off direct play.
//
// The stored probe does not keep the format bitrate or attachment sizes, so
// each candidate's header is read again: ffprobe's format and stream entries,
// no packets. Candidates are present MKV files with an ASS or SSA subtitle
// track. Only the bitrate column is written, and only over the row exactly as
// it was read.
//
// It runs once per deployment. attachment_bitrate_backfill_state holds the
// last media file ID a finished batch covered, so a restart or a stop on
// stalled storage resumes after it, and marks the pass done once it reaches
// the end. Files scanned after that already store the corrected rate.
type AttachmentBitrateBackfiller struct {
	pool        *pgxpool.Pool
	ffprobePath string
	workers     int
	batch       int
	// probe reads a file's attachment-free rate; tests replace it.
	probe func(ctx context.Context, ffprobePath, filePath string) (containerBitrate, error)
}

// containerBitrate is what one header read found: the attachment-free rate in
// kbps and the file size ffprobe saw.
type containerBitrate struct {
	kbps int
	size int64
}

// NewAttachmentBitrateBackfiller returns a backfiller over repo's database that
// probes with ffprobePath, or nil when repo has none.
func NewAttachmentBitrateBackfiller(repo *FileRepository, ffprobePath string) *AttachmentBitrateBackfiller {
	if repo == nil || repo.pool == nil {
		return nil
	}
	return &AttachmentBitrateBackfiller{
		pool:        repo.pool,
		ffprobePath: ffprobePath,
		workers:     attachmentBitrateBackfillWorkers,
		batch:       attachmentBitrateBackfillBatchSize,
		probe:       probeContainerBitrate,
	}
}

type attachmentBitrateCandidate struct {
	id         int
	path       string
	size       int64
	modifiedAt *time.Time
	bitrate    int
}

type attachmentBitrateOutcome int

const (
	attachmentBitrateOutcomeUpdated attachmentBitrateOutcome = iota
	attachmentBitrateOutcomeUnchanged
	attachmentBitrateOutcomeChanged
	attachmentBitrateOutcomeRejected
	attachmentBitrateOutcomeUnreadable
	attachmentBitrateOutcomeTimedOut
)

// Run makes the pass, or resumes it, unless an earlier run finished it.
// progress, when non-nil, is called after each batch with the running totals
// and the share of media_files IDs covered, from 0 to 100.
func (b *AttachmentBitrateBackfiller) Run(ctx context.Context, progress func(AttachmentBitrateBackfillResult, float64)) (AttachmentBitrateBackfillResult, error) {
	var result AttachmentBitrateBackfillResult
	if b == nil || b.pool == nil {
		return result, nil
	}
	var (
		afterID   int
		completed *time.Time
	)
	if err := b.pool.QueryRow(ctx, `SELECT last_media_file_id, completed_at FROM attachment_bitrate_backfill_state WHERE id = 1`).
		Scan(&afterID, &completed); err != nil {
		return result, fmt.Errorf("reading attachment bitrate backfill state: %w", err)
	}
	if completed != nil {
		result.Done = true
		return result, nil
	}
	var maxID int
	if err := b.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM media_files`).Scan(&maxID); err != nil {
		return result, fmt.Errorf("reading media_files id range: %w", err)
	}
	// Failed reads in a row, in completion order across all workers, and
	// the cursor of the batch the run began in: a stop resumes there, so
	// files that failed before the stop was certain are read again.
	consecutiveFailures, streakFrom := 0, afterID
	for {
		batchFrom := afterID
		candidates, lastID, err := b.loadCandidates(ctx, afterID)
		if err != nil {
			return result, err
		}
		if len(candidates) == 0 {
			if _, err := b.pool.Exec(ctx, `
				UPDATE attachment_bitrate_backfill_state
				SET completed_at = now(), updated_at = now()
				WHERE id = 1`); err != nil {
				return result, fmt.Errorf("recording attachment bitrate backfill completion: %w", err)
			}
			result.Done = true
			logAttachmentBitrateFailures(ctx, result)
			return result, nil
		}

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
				case attachmentBitrateOutcomeUnreadable, attachmentBitrateOutcomeTimedOut:
					if consecutiveFailures == 0 {
						streakFrom = batchFrom
					}
					consecutiveFailures++
				default:
					consecutiveFailures = 0
				}
				switch outcome {
				case attachmentBitrateOutcomeUpdated:
					result.Updated++
				case attachmentBitrateOutcomeUnchanged:
					result.Unchanged++
				case attachmentBitrateOutcomeChanged:
					result.Changed++
				case attachmentBitrateOutcomeRejected, attachmentBitrateOutcomeUnreadable:
					result.Failed++
				case attachmentBitrateOutcomeTimedOut:
					result.TimedOut++
				}
				if consecutiveFailures >= attachmentBitrateBackfillMaxFailures {
					return fmt.Errorf("%w: %d probes in a row failed or timed out", errAttachmentBitrateStorageStalled, consecutiveFailures)
				}
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			logAttachmentBitrateFailures(ctx, result)
			if errors.Is(err, errAttachmentBitrateStorageStalled) {
				// Re-reading from where the failures began writes nothing
				// already written: a corrected row no longer matches.
				if saveErr := b.saveCursor(ctx, streakFrom); saveErr != nil {
					return result, errors.Join(err, saveErr)
				}
			}
			return result, err
		}
		afterID = lastID
		if err := b.saveCursor(ctx, afterID); err != nil {
			return result, err
		}
		if progress != nil && maxID > 0 {
			progress(result, min(100, float64(afterID)*100/float64(maxID)))
		}
	}
}

// saveCursor records afterID as the point the next run resumes after. One
// runner holds the cluster lock, so the cursor may move back.
func (b *AttachmentBitrateBackfiller) saveCursor(ctx context.Context, afterID int) error {
	if _, err := b.pool.Exec(ctx, `
		UPDATE attachment_bitrate_backfill_state
		SET last_media_file_id = $1, updated_at = now()
		WHERE id = 1`, afterID); err != nil {
		return fmt.Errorf("recording attachment bitrate backfill progress: %w", err)
	}
	return nil
}

// loadCandidates returns the next batch of candidates after afterID and the
// last ID among them.
func (b *AttachmentBitrateBackfiller) loadCandidates(ctx context.Context, afterID int) ([]attachmentBitrateCandidate, int, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT mf.id, mf.file_path, mf.file_size, mf.file_modified_at, mf.bitrate
		FROM media_files mf
		WHERE mf.id > $1
		  AND mf.container = 'mkv'
		  AND mf.missing_since IS NULL
		  AND mf.file_size IS NOT NULL
		  AND mf.bitrate > 0
		  AND jsonb_typeof(mf.subtitle_tracks) = 'array'
		  AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(mf.subtitle_tracks) t
			WHERE lower(COALESCE(t->>'codec', '')) = ANY($3::text[])
		  )
		ORDER BY mf.id
		LIMIT $2`, afterID, b.batch, attachmentBitrateBackfillSubtitleCodecs)
	if err != nil {
		return nil, afterID, fmt.Errorf("loading attachment bitrate backfill candidates: %w", err)
	}
	defer rows.Close()
	var candidates []attachmentBitrateCandidate
	lastID := afterID
	for rows.Next() {
		var c attachmentBitrateCandidate
		if err := rows.Scan(&c.id, &c.path, &c.size, &c.modifiedAt, &c.bitrate); err != nil {
			return nil, afterID, fmt.Errorf("scanning attachment bitrate backfill candidate: %w", err)
		}
		lastID = c.id
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, afterID, fmt.Errorf("iterating attachment bitrate backfill candidates: %w", err)
	}
	return candidates, lastID, nil
}

// backfillFile returns an error for a database failure or a canceled pass. A
// file that cannot be probed is an outcome, not an error.
func (b *AttachmentBitrateBackfiller) backfillFile(ctx context.Context, c attachmentBitrateCandidate) (attachmentBitrateOutcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	probed, err := b.probe(ctx, b.ffprobePath, c.path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	if errors.Is(err, errAttachmentBitrateProbeTimeout) {
		slog.WarnContext(ctx, "scanner: attachment bitrate probe timed out",
			"component", "scanner", "file_id", c.id, "path", c.path, "timeout", attachmentBitrateProbeTimeout)
		return attachmentBitrateOutcomeTimedOut, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: attachment bitrate backfill could not probe file",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		// A file ffprobe read and refused fails the same way every time.
		// Anything else is the storage or the binary, which a run of them
		// turns into a stop instead of skipping the library.
		if IsProbeRejection(ctx, c.path, err) {
			return attachmentBitrateOutcomeRejected, nil
		}
		return attachmentBitrateOutcomeUnreadable, nil
	}
	// The probe read the file on disk, which a scan has not reprobed if it
	// changed since. That scan stores the corrected rate itself.
	if probed.size != c.size {
		return attachmentBitrateOutcomeChanged, nil
	}
	if probed.kbps <= 0 || probed.kbps == c.bitrate {
		return attachmentBitrateOutcomeUnchanged, nil
	}
	// Write only over the exact row that was read: a scan that rewrote it in
	// the meantime stored its own rate.
	tag, err := b.pool.Exec(ctx, `
		UPDATE media_files
		SET bitrate = $5, updated_at = NOW()
		WHERE id = $1
		  AND file_size = $2
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $3::timestamptz
		  AND bitrate = $4`,
		c.id, c.size, c.normalizedModifiedAt(), c.bitrate, probed.kbps)
	if err != nil {
		return 0, fmt.Errorf("recording attachment-free bitrate for file %d: %w", c.id, err)
	}
	if tag.RowsAffected() == 0 {
		return attachmentBitrateOutcomeChanged, nil
	}
	return attachmentBitrateOutcomeUpdated, nil
}

// probeContainerBitrate reads a file's format and stream entries and returns
// the bitrate a scan would store for it now, and the file size ffprobe saw.
func probeContainerBitrate(ctx context.Context, ffprobePath, filePath string) (containerBitrate, error) {
	runCtx, cancel := context.WithTimeout(ctx, attachmentBitrateProbeTimeout)
	defer cancel()
	cmd := fspath.CommandContext(runCtx, ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_entries", "format=duration,size,bit_rate:stream=index,codec_type,extradata_size",
		filePath)
	cmd.WaitDelay = defaultProbeLimits.waitDelay
	output, err := cmd.Output()
	processmetrics.Record(processmetrics.Probe, cmd.ProcessState, err, runCtx.Err())
	if err != nil {
		if probeTimedOut(ctx, runCtx) {
			return containerBitrate{}, fmt.Errorf("%w: no result for %s within %s", errAttachmentBitrateProbeTimeout, filePath, attachmentBitrateProbeTimeout)
		}
		return containerBitrate{}, fmt.Errorf("ffprobe failed for %s: %w", filePath, err)
	}
	var raw ffprobeOutput
	if err := json.Unmarshal(output, &raw); err != nil {
		return containerBitrate{}, fmt.Errorf("ffprobe JSON parse failed for %s: %w", filePath, err)
	}
	size, _ := strconv.ParseInt(raw.Format.Size, 10, 64)
	return containerBitrate{kbps: formatBitrateKbps(&raw), size: size}, nil
}

// logAttachmentBitrateFailures warns once per pass about files it could not
// probe, so a misconfigured ffprobe or unreachable storage is visible.
func logAttachmentBitrateFailures(ctx context.Context, result AttachmentBitrateBackfillResult) {
	if result.Failed > 0 || result.TimedOut > 0 {
		slog.WarnContext(ctx, "scanner: attachment bitrate backfill could not probe some files; they keep their stored bitrate until a scan reprobes them",
			"component", "scanner", "failed", result.Failed, "timed_out", result.TimedOut, "checked", result.Checked)
	}
}

// normalizedModifiedAt is the stored mtime in the form row comparisons use.
func (c attachmentBitrateCandidate) normalizedModifiedAt() *time.Time {
	if c.modifiedAt == nil {
		return nil
	}
	normalized := models.NormalizeFileModifiedAt(*c.modifiedAt)
	return &normalized
}

package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

const (
	attachmentBitrateBackfillBatchSize = 200
	// Each candidate costs one header read, often on a network mount, so a
	// few at a time keeps the pass from competing with playback.
	attachmentBitrateBackfillWorkers = 4
	// attachmentBitrateBackfillMaxTimeouts probes in a row that timed out
	// mean the storage is stalled, not that a few files are slow. The run
	// stops and lists the batch it stopped in, so the next run reads those
	// files again and walks on past them. A read that fails fast (an offline
	// library root) does not count: its file is listed to read again.
	attachmentBitrateBackfillMaxTimeouts = 8
)

// attachmentBitrateProbeTimeout bounds one header read: format and stream
// entries only, the same stream analysis a scan's probe does, with no packets
// or frames printed. FFmpeg loads every Matroska attachment when it opens the
// file, so for the files this pass reads, the read covers the whole font
// payload, often most of the file. It gets the scan probe's limit, so a file a
// scan can read on slow storage does not time out here.
var attachmentBitrateProbeTimeout = defaultProbeLimits.probe

// attachmentBitrateBackfillSubtitleCodecs are the subtitle codecs, as FFmpeg
// names them, of the releases that embed fonts: ASS and SSA render with the
// fonts attached to the file. Other MKV files rarely carry attachments large
// enough to move their rate, so they are not read.
var attachmentBitrateBackfillSubtitleCodecs = []string{ffmpegCodecASS, "ssa"}

var errAttachmentBitrateStorageStalled = errors.New("media storage is not responding")

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
	// Failed files could not be probed. A file ffprobe read and refused keeps
	// its stored rate until a scan reprobes it; any other failure is read
	// again on a later run.
	Failed int `json:"failed"`
	// TimedOut files did not answer within attachmentBitrateProbeTimeout.
	// They are read again on a later run.
	TimedOut int `json:"timed_out"`
	// Pending is the number of files listed to read again on a later run.
	Pending int `json:"pending"`
	// Done reports that the pass has covered every file and has no file left
	// to read again, now or on an earlier run.
	Done bool `json:"done"`
}

// AttachmentBitrateBackfiller corrects the stored bitrate of MKV files scanned
// while their attachments still counted toward it (see formatBitrateKbps).
// Normal rescans skip unchanged files, so without it a font-heavy release
// keeps a rate up to twice its real one, which pushes it off direct play.
//
// The stored probe does not keep the format bitrate or attachment sizes, so
// each candidate's header is read again: ffprobe's format and stream entries,
// from the same stream analysis a scan runs, so the rate matches what a scan
// stores. Candidates are present MKV files with an ASS or SSA subtitle
// track. Only the bitrate column is written, and only over the row exactly as
// it was read.
//
// It runs once per deployment. attachment_bitrate_backfill_state holds the
// last media file ID a batch covered, so a restart or a stop on stalled
// storage resumes after it, and marks the walk finished once it reaches the
// end. It also lists the files a run could not read, such as those under an
// offline or stalled library root; each later run reads them again until they
// answer or stop being candidates. Files scanned after the walk already store
// the corrected rate.
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

// Run makes the pass, or resumes it, unless an earlier run finished it: it
// first reads again the files earlier runs could not read, then walks on from
// the cursor. progress, when non-nil, is called after each batch of the walk
// with the running totals and the share of media_files IDs covered, from 0 to
// 100.
func (b *AttachmentBitrateBackfiller) Run(ctx context.Context, progress func(AttachmentBitrateBackfillResult, float64)) (AttachmentBitrateBackfillResult, error) {
	var result AttachmentBitrateBackfillResult
	if b == nil || b.pool == nil {
		return result, nil
	}
	var (
		afterID    int
		retryIDs   []int64
		retryAfter int64
		completed  *time.Time
	)
	if err := b.pool.QueryRow(ctx, `
		SELECT last_media_file_id, retry_media_file_ids, retry_after_id, completed_at
		FROM attachment_bitrate_backfill_state WHERE id = 1`).
		Scan(&afterID, &retryIDs, &retryAfter, &completed); err != nil {
		return result, fmt.Errorf("reading attachment bitrate backfill state: %w", err)
	}
	if completed != nil && len(retryIDs) == 0 {
		result.Done = true
		return result, nil
	}
	err := b.run(ctx, afterID, retryIDs, retryAfter, completed == nil, progress, &result)
	logAttachmentBitrateFailures(ctx, result)
	if pendingErr := b.pool.QueryRow(ctx, `SELECT cardinality(retry_media_file_ids) FROM attachment_bitrate_backfill_state WHERE id = 1`).
		Scan(&result.Pending); pendingErr != nil {
		return result, errors.Join(err, fmt.Errorf("reading attachment bitrate backfill retries: %w", pendingErr))
	}
	result.Done = err == nil && result.Pending == 0
	return result, err
}

// run reads the listed files again and then, unless the walk has finished,
// walks on from afterID.
func (b *AttachmentBitrateBackfiller) run(ctx context.Context, afterID int, retryIDs []int64, retryAfter int64, walk bool, progress func(AttachmentBitrateBackfillResult, float64), result *AttachmentBitrateBackfillResult) error {
	// Timed-out reads in a row, in completion order.
	timeouts := 0
	if len(retryIDs) > 0 {
		// A stall leaves the rest listed and goes on to the walk: one slow
		// root must not hold up the others, and a stall that is not the
		// root's stops the walk too.
		err := b.retryListed(ctx, retryIDs, retryAfter, result, &timeouts)
		switch {
		case errors.Is(err, errAttachmentBitrateStorageStalled):
			slog.WarnContext(ctx, "scanner: attachment bitrate backfill stopped reading listed files again; the rest stay listed for the next run",
				"component", "scanner", "error", err)
		case err != nil:
			return err
		}
		timeouts = 0
	}
	if !walk {
		return nil
	}
	var maxID int
	if err := b.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM media_files`).Scan(&maxID); err != nil {
		return fmt.Errorf("reading media_files id range: %w", err)
	}
	// Each batch moves the cursor past itself and lists the files it could
	// not read, the whole batch when it stalled, so a root whose reads keep
	// stalling does not hold up the files after it.
	if err := b.eachBatch(ctx, afterID, nil, result, &timeouts, func(lastID int, failed []int64, _ int) error {
		if _, err := b.pool.Exec(ctx, `
			UPDATE attachment_bitrate_backfill_state
			SET last_media_file_id = $1,
			    retry_media_file_ids = ARRAY(SELECT DISTINCT unnest(retry_media_file_ids || $2::bigint[]) ORDER BY 1),
			    updated_at = now()
			WHERE id = 1`, lastID, failed); err != nil {
			return fmt.Errorf("recording attachment bitrate backfill progress: %w", err)
		}
		if progress != nil && maxID > 0 {
			progress(*result, min(100, float64(lastID)*100/float64(maxID)))
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := b.pool.Exec(ctx, `
		UPDATE attachment_bitrate_backfill_state
		SET completed_at = now(), updated_at = now()
		WHERE id = 1`); err != nil {
		return fmt.Errorf("recording attachment bitrate backfill completion: %w", err)
	}
	return nil
}

// retryListed reads the listed files again, starting after retryAfter, where
// the last run stopped, and wrapping around, so files listed after a root
// whose reads keep stalling are still read. Each batch drops the listed files
// in its ID range that answered or are no longer candidates and records where
// it got to.
func (b *AttachmentBitrateBackfiller) retryListed(ctx context.Context, retryIDs []int64, retryAfter int64, result *AttachmentBitrateBackfillResult, timeouts *int) error {
	var later, earlier []int64
	for _, id := range retryIDs {
		if id > retryAfter {
			later = append(later, id)
		} else {
			earlier = append(earlier, id)
		}
	}
	for _, pass := range []struct {
		from, through int64
		ids           []int64
	}{{retryAfter, math.MaxInt64, later}, {0, retryAfter, earlier}} {
		if len(pass.ids) == 0 {
			continue
		}
		prunedThrough := pass.from
		if err := b.eachBatch(ctx, int(pass.from), pass.ids, result, timeouts, func(lastID int, failed []int64, readThrough int) error {
			from := prunedThrough
			prunedThrough = int64(lastID)
			return b.pruneRetries(ctx, pass.ids, from, prunedThrough, failed, int64(readThrough))
		}); err != nil {
			return err
		}
		// Listed files after the last batch are no longer candidates.
		if err := b.pruneRetries(ctx, pass.ids, prunedThrough, pass.through, nil, 0); err != nil {
			return err
		}
	}
	return nil
}

// pruneRetries drops the listed files after afterID up to throughID, except
// failed, the ones to read again, and records resumeAfter as where the next
// run starts reading the list. It drops only files in read, the list this run
// started from: a file listed since, by a run on another server that took the
// lock after this one lost its session, stays listed.
func (b *AttachmentBitrateBackfiller) pruneRetries(ctx context.Context, read []int64, afterID, throughID int64, failed []int64, resumeAfter int64) error {
	if _, err := b.pool.Exec(ctx, `
		UPDATE attachment_bitrate_backfill_state
		SET retry_media_file_ids = ARRAY(
		        SELECT id FROM unnest(retry_media_file_ids) id
		        WHERE id <= $1 OR id > $2 OR id = ANY($3::bigint[]) OR id <> ALL($5::bigint[])
		        ORDER BY id),
		    retry_after_id = $4,
		    updated_at = now()
		WHERE id = 1`, afterID, throughID, failed, resumeAfter, read); err != nil {
		return fmt.Errorf("recording attachment bitrate backfill retries: %w", err)
	}
	return nil
}

// eachBatch probes the candidates after afterID, only those in onlyIDs when
// it is non-nil, a batch at a time, and calls finished after each batch with
// the batch's last ID, the IDs of the files to read again later, and the last
// ID it read through. Once *timeouts reaches
// attachmentBitrateBackfillMaxTimeouts it calls finished with every file of
// the batch, and the last ID it read before the stop, and stops with
// errAttachmentBitrateStorageStalled.
func (b *AttachmentBitrateBackfiller) eachBatch(ctx context.Context, afterID int, onlyIDs []int64, result *AttachmentBitrateBackfillResult, timeouts *int, finished func(lastID int, failed []int64, readThrough int) error) error {
	for {
		candidates, lastID, err := b.loadCandidates(ctx, afterID, onlyIDs)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		var (
			mu     sync.Mutex
			failed []int64
			// readThrough is the highest ID read in the batch; files after
			// it were not reached when a stall stopped the batch.
			readThrough = afterID
		)
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
				readThrough = max(readThrough, candidate.id)
				if outcome == attachmentBitrateOutcomeTimedOut {
					*timeouts++
				} else {
					*timeouts = 0
				}
				switch outcome {
				case attachmentBitrateOutcomeUpdated:
					result.Updated++
				case attachmentBitrateOutcomeUnchanged:
					result.Unchanged++
				case attachmentBitrateOutcomeChanged:
					result.Changed++
				case attachmentBitrateOutcomeRejected:
					result.Failed++
				case attachmentBitrateOutcomeUnreadable:
					result.Failed++
					failed = append(failed, int64(candidate.id))
				case attachmentBitrateOutcomeTimedOut:
					result.TimedOut++
					failed = append(failed, int64(candidate.id))
				}
				if *timeouts >= attachmentBitrateBackfillMaxTimeouts {
					return fmt.Errorf("%w: %d probes in a row timed out", errAttachmentBitrateStorageStalled, *timeouts)
				}
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			if !errors.Is(err, errAttachmentBitrateStorageStalled) {
				return err
			}
			// Files the batch corrected before the stop no longer match
			// their stored rate, so reading them again writes nothing.
			batch := make([]int64, 0, len(candidates))
			for _, c := range candidates {
				batch = append(batch, int64(c.id))
			}
			if finishErr := finished(lastID, batch, readThrough); finishErr != nil {
				return finishErr
			}
			return err
		}
		if err := finished(lastID, failed, lastID); err != nil {
			return err
		}
		afterID = lastID
	}
}

// loadCandidates returns the next batch of candidates after afterID, only
// those in onlyIDs when it is non-nil, and the last ID among them.
func (b *AttachmentBitrateBackfiller) loadCandidates(ctx context.Context, afterID int, onlyIDs []int64) ([]attachmentBitrateCandidate, int, error) {
	// The listed files get their own statement. A "$4 IS NULL OR id = ANY($4)"
	// shared with the walk turns into a generic plan once the walk has run it a
	// few times, and that plan reads every row after afterID for a handful of
	// listed IDs.
	idFilter := ""
	args := []any{afterID, b.batch, attachmentBitrateBackfillSubtitleCodecs}
	if onlyIDs != nil {
		idFilter = "AND mf.id = ANY($4::bigint[])"
		args = append(args, onlyIDs)
	}
	rows, err := b.pool.Query(ctx, `
		SELECT mf.id, mf.file_path, mf.file_size, mf.file_modified_at, mf.bitrate
		FROM media_files mf
		WHERE mf.id > $1
		  `+idFilter+`
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
		LIMIT $2`, args...)
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
	if errors.Is(err, errProbeTimeout) {
		slog.WarnContext(ctx, "scanner: attachment bitrate probe timed out",
			"component", "scanner", "file_id", c.id, "path", c.path, "timeout", attachmentBitrateProbeTimeout)
		return attachmentBitrateOutcomeTimedOut, nil
	}
	if err != nil {
		slog.DebugContext(ctx, "scanner: attachment bitrate backfill could not probe file",
			"component", "scanner", "file_id", c.id, "path", c.path, "error", err)
		// A file ffprobe read and refused fails the same way every time.
		// Anything else is the storage or the binary, so the file is read
		// again on a later run.
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
	// the meantime stored its own rate, and a relink that moved it to another
	// path leaves the file just read at the old one.
	tag, err := b.pool.Exec(ctx, `
		UPDATE media_files
		SET bitrate = $5, updated_at = NOW()
		WHERE id = $1
		  AND file_size = $2
		  AND date_trunc('microseconds', file_modified_at) IS NOT DISTINCT FROM $3::timestamptz
		  AND bitrate = $4
		  AND file_path = $6`,
		c.id, c.size, normalizedStoredModifiedAt(c.modifiedAt), c.bitrate, probed.kbps, c.path)
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
			return containerBitrate{}, fmt.Errorf("%w: no result for %s within %s", errProbeTimeout, filePath, attachmentBitrateProbeTimeout)
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
		slog.WarnContext(ctx, "scanner: attachment bitrate backfill could not probe some files; it reads unreadable and timed-out files again on its next run",
			"component", "scanner", "failed", result.Failed, "timed_out", result.TimedOut, "checked", result.Checked)
	}
}

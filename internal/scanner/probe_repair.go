package scanner

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"golang.org/x/sync/singleflight"
)

// NeedsCriticalProbeRepair reports whether playback-critical probe metadata is
// missing and the file should be reprobed before making playback decisions.
func NeedsCriticalProbeRepair(file *models.MediaFile) bool {
	if file == nil {
		return true
	}
	// Ebook/comic files (epub, pdf, cbz, cbr — including manga chapters, which
	// are BaseType "ebook") are read directly by the reader and never go through
	// the transcode/playback probe pipeline. ffprobe yields nothing useful for
	// them, so requiring probe metadata re-ran ffprobe on every detail/watch
	// load and never converged.
	if file.BaseType == "ebook" {
		return false
	}
	if file.HasLegacyAttachedPictureVideo() {
		return true
	}
	if strings.TrimSpace(file.ProbeSource) == "" || file.ProbeUpdatedAt == nil {
		return true
	}
	if file.Duration <= 0 {
		return true
	}
	// Legacy probes could turn malformed multi-day container timestamps into a
	// few seconds by treating ffprobe's seconds as microseconds. Reprobe the
	// narrow, physically implausible shape produced by that conversion.
	if needsLegacyDurationRepair(file) {
		return true
	}
	if strings.TrimSpace(file.Container) == "" {
		return true
	}
	hasVideo := strings.TrimSpace(file.CodecVideo) != "" || len(file.VideoTracks) > 0
	hasAudio := strings.TrimSpace(file.CodecAudio) != "" || len(file.AudioTracks) > 0
	if !hasVideo && !hasAudio {
		return true
	}
	if hasAudio && (strings.TrimSpace(file.CodecAudio) == "" || len(file.AudioTracks) == 0) {
		return true
	}
	if !hasVideo && hasAudio && !file.IsAudioOnly() {
		return true
	}
	// Video metadata is playback-critical only for files that actually carry a
	// video stream. Audio-only files (audiobooks, music) legitimately probe to
	// zero video tracks and an empty video codec/resolution; treating that as
	// "needs repair" re-ran ffprobe on every playback decision (applyProbeData
	// only populates video fields under a "video" stream), so an audio-only
	// file would never satisfy the check. The inverse is also valid: synthetic
	// clips and some test assets carry video with no audio stream. Demand each
	// stream family's fields only when that family is present.
	if hasVideo {
		if strings.TrimSpace(file.CodecVideo) == "" || strings.TrimSpace(file.Resolution) == "" || len(file.VideoTracks) == 0 {
			return true
		}
		if videoTracksMissingColorRange(file.VideoTracks) {
			return true
		}
		if videoTracksHaveLegacyDVProvenance(file.VideoTracks) {
			return true
		}
	}
	if file.Chapters == nil {
		return true
	}
	return false
}

// videoTracksHaveLegacyDVProvenance reports whether a Dolby Vision track was
// written by a probe generation that predates the DV provenance columns. Those
// rows decode to explicit false, which is indistinguishable from a genuine
// "the source really has no DV configuration record" — and the tone-map
// preflight needs the distinction, so the row is reprobed once.
func videoTracksHaveLegacyDVProvenance(tracks []models.VideoTrack) bool {
	for _, track := range tracks {
		isDV := track.DVProfile > 0 || strings.Contains(strings.ToLower(track.VideoRangeType), "dovi") ||
			strings.Contains(strings.ToLower(track.DolbyVision), "dolby")
		if isDV && track.DVProvenanceCurrent != nil && !*track.DVProvenanceCurrent {
			return true
		}
	}
	return false
}

func videoTracksMissingColorRange(tracks []models.VideoTrack) bool {
	for _, track := range tracks {
		if strings.TrimSpace(track.ColorRange) == "" {
			return true
		}
	}
	return false
}

// copySafetyWriter persists a multi-PPS verdict. *FileRepository satisfies it;
// the indirection keeps the ensurer testable without a database.
//
// scanMtime is nil for a row that carries no file mtime: such a verdict is
// still recorded, and validated on size alone when it is read back. Refusing to
// write it would leave those rows permanently unverdicted, so every replica
// would rescan and re-invalidate the same sessions forever.
type copySafetyWriter interface {
	UpdateMultiplePPS(ctx context.Context, fileID int, multiplePPS bool, scanSize int64, scanMtime *time.Time) error
}

// playbackProbeFileRepository is the slice of *FileRepository the ensurer
// needs: re-read the row inside the repair flight, and persist the reprobe.
// The indirection keeps the coalescing tests off a live database.
type playbackProbeFileRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
	Upsert(ctx context.Context, file models.MediaFile) (*models.MediaFile, error)
}

// PlaybackProbeEnsurer repairs missing playback-critical probe metadata on
// demand by running a local ffprobe and persisting the result.
type PlaybackProbeEnsurer struct {
	fileRepo    playbackProbeFileRepository
	ffprobePath string
	ffmpegPath  string
	timeout     time.Duration
	// probeFile is the ffprobe entry point; nil means the package's ProbeFile.
	// Tests substitute it to drive the coalescing behavior deterministically.
	probeFile func(context.Context, string, string) (*ProbeData, error)
	// probeRepair collapses concurrent repairs of the same source revision so a
	// burst of playback/detail requests spawns one ffprobe, not one each.
	probeRepair singleflight.Group
	// probeSlots bounds how many distinct repairs may run ffprobe at once, so a
	// library-wide browse cannot fork an unbounded number of processes.
	probeSlots chan struct{}
	// copySafetyRepo persists multi-PPS verdicts. Normally the same
	// *FileRepository as fileRepo; tests substitute a double.
	copySafetyRepo copySafetyWriter
	// copySafety memoizes the multi-PPS bitstream scan per file for the life of
	// the process, in front of the persisted media_files verdict. Both layers
	// are validated against the file's current size and mtime.
	copySafety sync.Map // file ID -> copySafetyResult
	// copySafetyFlight collapses concurrent first scans of the same file so a
	// burst of playback/detail requests spawns one ffmpeg, not one each.
	copySafetyFlight singleflight.Group
}

type copySafetyResult struct {
	size  int64
	mtime *time.Time
	multi bool
	// persisted records whether this verdict reached the media_files row. A
	// verdict memoized with persisted=false is correct for this process but
	// invisible to every other replica, so a later lookup retries the write —
	// the write only, never the scan.
	persisted bool
}

// matches reports whether a memoized verdict still describes the given file.
func (r copySafetyResult) matches(file *models.MediaFile) bool {
	if r.size != file.FileSize {
		return false
	}
	if r.mtime == nil || file.FileModifiedAt == nil {
		// A verdict recorded without an mtime can only be trusted on size.
		return r.mtime == nil && file.FileModifiedAt == nil
	}
	return sameFileModifiedAt(r.mtime, *file.FileModifiedAt)
}

func NewPlaybackProbeEnsurer(fileRepo *FileRepository, ffprobePath, ffmpegPath string, timeout time.Duration) *PlaybackProbeEnsurer {
	e := &PlaybackProbeEnsurer{
		ffprobePath: ffprobePath,
		ffmpegPath:  ffmpegPath,
		timeout:     timeout,
		probeSlots:  make(chan struct{}, 4),
	}
	// Assign through a typed nil check so a nil *FileRepository keeps the
	// interfaces nil: the e.fileRepo == nil guard is the constructor's
	// contract, and a typed-nil interface would panic inside GetByID.
	if fileRepo != nil {
		e.fileRepo = fileRepo
		e.copySafetyRepo = fileRepo
	}
	return e
}

// Ensure repairs playback-critical probe metadata and resolves the H.264
// copy-safety verdict. Use it where a play is being prepared — the planner
// consumes the verdict to decide whether a video stream-copy is safe.
func (e *PlaybackProbeEnsurer) Ensure(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	current, err := e.ensureProbeRepair(ctx, file)
	if err != nil || current == nil || e == nil {
		return current, err
	}

	// Copy-safety analysis is independent of critical probe repair: an
	// already-probed file still needs its multi-PPS verdict before the planner
	// can decide whether a video stream-copy is safe.
	return e.ensureCopySafety(ctx, current)
}

// EnsureProbeOnly repairs playback-critical probe metadata and stops there.
//
// Browse surfaces (item, episode and extra detail pages) use this: they never
// consume the copy-safety verdict — VideoTrack.MultiplePPS is json:"-" and
// never reaches a client — so running the bitstream scan there was pure
// warm-up, and it is exactly what made first-time browsing slow on remote
// storage. The verdict is resolved when a play is actually being prepared.
func (e *PlaybackProbeEnsurer) EnsureProbeOnly(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	return e.ensureProbeRepair(ctx, file)
}

// EnsureCopySafetyCached repairs playback-critical probe metadata and stamps
// the copy-safety verdict only when it is already known — from the process
// cache or from the verdict persisted on the media_files row. It never execs
// ffmpeg, so it never blocks a play or a watch page on a bitstream scan.
//
// An unknown verdict is left unknown: VideoTrack.MultiplePPS stays nil and
// VideoCopyUnsafe stays false, which the planner reads as "stream copy is
// allowed". That is the optimistic half of the race — the caller is expected to
// kick off ScanCopySafety asynchronously and switch live sessions off the copy
// route if the scan comes back multi-PPS.
func (e *PlaybackProbeEnsurer) EnsureCopySafetyCached(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	current, err := e.ensureProbeRepair(ctx, file)
	if err != nil || current == nil || e == nil {
		return current, err
	}
	if !needsCopySafetyProbe(current) {
		return current, nil
	}
	if multi, ok := e.knownCopySafetyVerdict(current); ok {
		e.retryUnpersistedCopySafety(ctx, current)
		return fileWithMultiplePPS(current, multi), nil
	}
	return current, nil
}

// NeedsCopySafetyScan reports whether an asynchronous ScanCopySafety would do
// real work for this file: an H.264 video whose verdict is neither cached nor
// persisted, on a server that has an ffmpeg to scan with.
func (e *PlaybackProbeEnsurer) NeedsCopySafetyScan(file *models.MediaFile) bool {
	if e == nil || strings.TrimSpace(e.ffmpegPath) == "" || !needsCopySafetyProbe(file) {
		return false
	}
	_, known := e.knownCopySafetyVerdict(file)
	return !known
}

// ScanCopySafety runs the multi-PPS bitstream scan for a file whose verdict is
// unknown, persisting and memoizing the result. Concurrent callers for one file
// share a single scan, so a start, a replan and a watch-page load racing on the
// same file spawn one ffmpeg between them.
//
// The second return reports that the verdict is stale: the row moved to another
// generation of the file while the scan ran, so the answer is correct for bytes
// the server is no longer serving. It is not an error — nothing failed — but a
// caller must neither trust it nor act on it.
func (e *PlaybackProbeEnsurer) ScanCopySafety(ctx context.Context, file *models.MediaFile) (multi bool, stale bool, err error) {
	if e == nil || file == nil {
		return false, false, nil
	}
	if strings.TrimSpace(e.ffmpegPath) == "" {
		return false, false, errCopySafetyScanUnavailable
	}
	return e.scanAndPersistCopySafety(ctx, file)
}

// KnownCopySafetyVerdict answers the copy-safety question for a file without
// ever running ffmpeg, from the process memo or from the persisted row, and
// re-attempts a write this process reached but never managed to store.
//
// It exists because "unknown" and "known but unpersisted" are different states
// that the media_files row cannot tell apart. A verdict whose write failed is
// authoritative on this replica and invisible everywhere else, so the paths
// that gate a revived stream-copy — and the race that withdraws one — have to
// be able to ask this process what it already knows rather than only asking the
// row.
func (e *PlaybackProbeEnsurer) KnownCopySafetyVerdict(ctx context.Context, file *models.MediaFile) (bool, bool) {
	if e == nil || file == nil {
		return false, false
	}
	multi, known := e.knownCopySafetyVerdict(file)
	if !known {
		return false, false
	}
	e.retryUnpersistedCopySafety(ctx, file)
	return multi, true
}

var errCopySafetyScanUnavailable = errors.New("ffmpeg path not configured")

// knownCopySafetyVerdict answers the copy-safety question from memory or from
// the persisted row, never from ffmpeg. A persisted verdict is self-validating:
// it is only honored while the recorded size and mtime still describe the file,
// so a rewrite in place falls through to a rescan without any writer having to
// clear it. Promoting it into the process cache keeps later calls off the row.
func (e *PlaybackProbeEnsurer) knownCopySafetyVerdict(file *models.MediaFile) (bool, bool) {
	if e == nil || file == nil {
		return false, false
	}
	if entry, ok := e.memoizedCopySafety(file); ok {
		return entry.multi, true
	}
	if multi, ok := persistedCopySafetyVerdict(file); ok {
		// The row already holds it, so there is nothing left to write.
		e.storeCopySafety(file, multi, true)
		return multi, true
	}
	return false, false
}

func (e *PlaybackProbeEnsurer) ensureProbeRepair(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	if file == nil || e == nil || e.fileRepo == nil {
		return file, nil
	}

	current := file
	if NeedsCriticalProbeRepair(file) && strings.TrimSpace(e.ffprobePath) != "" {
		repaired, err := e.ensureCriticalProbe(ctx, file)
		if err != nil {
			return file, err
		}
		current = repaired
	}

	return current, nil
}

// ensureCriticalProbe reprobes one source revision at a time, shared across
// every caller looking at the same bytes.
//
// The flight key is the tone-map source revision fingerprint rather than the
// file ID: a rewrite in place keeps the row ID and changes the bitstream, and a
// caller holding the replacement must not consume the old generation's probe.
// The work runs on a context detached from any single caller, so the leader
// walking away (a client that closed its connection) cannot poison the probe
// for the callers still waiting on it; each caller still honors its own
// cancellation while waiting. Inside the flight the row is re-read first, so a
// caller holding a stale snapshot of an already-repaired file spawns no ffprobe
// at all.
func (e *PlaybackProbeEnsurer) ensureCriticalProbe(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	sharedCtx := context.WithoutCancel(ctx)
	revisionKey := tonemap.RevisionForFile(file).Fingerprint()
	resultCh := e.probeRepair.DoChan(revisionKey, func() (any, error) {
		lookupTimeout := e.timeout
		if lookupTimeout <= 0 {
			lookupTimeout = 5 * time.Second
		}
		lookupCtx, cancelLookup := context.WithTimeout(sharedCtx, lookupTimeout)
		current, err := e.fileRepo.GetByID(lookupCtx, file.ID)
		cancelLookup()
		if err != nil {
			return nil, err
		}
		if current == nil || !NeedsCriticalProbeRepair(current) {
			return current, nil
		}
		timeout := e.timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		if reprobeMayScanPackets(current) && timeout < time.Minute {
			timeout = time.Minute
		}
		probeCtx, cancel := context.WithTimeout(sharedCtx, timeout)
		defer cancel()
		if e.probeSlots != nil {
			select {
			case e.probeSlots <- struct{}{}:
				defer func() { <-e.probeSlots }()
			case <-probeCtx.Done():
				return nil, probeCtx.Err()
			}
		}
		probeFile := e.probeFile
		if probeFile == nil {
			probeFile = ProbeFile
		}
		probe, err := probeFile(probeCtx, e.ffprobePath, current.FilePath)
		if err != nil || probe == nil {
			return nil, err
		}
		updated := *current
		applyProbeData(&updated, probe, "local")
		return e.fileRepo.Upsert(probeCtx, updated)
	})
	select {
	case <-ctx.Done():
		return file, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return file, result.Err
		}
		repaired, _ := result.Val.(*models.MediaFile)
		if repaired == nil {
			return file, nil
		}
		return repaired, nil
	}
}

// ensureCopySafety resolves the multi-PPS copy-safety flag for H.264 files at
// playback start and stamps it on an in-memory copy of the file. It answers
// from the process cache first, then from the verdict persisted on the
// media_files row, and only then runs the bitstream scan — so a restart no
// longer re-reads the opening seconds of every browsed H.264 file.
func (e *PlaybackProbeEnsurer) ensureCopySafety(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	if !needsCopySafetyProbe(file) || strings.TrimSpace(e.ffmpegPath) == "" {
		return file, nil
	}

	if multi, ok := e.knownCopySafetyVerdict(file); ok {
		e.retryUnpersistedCopySafety(ctx, file)
		return fileWithMultiplePPS(file, multi), nil
	}

	multi, stale, err := e.scanAndPersistCopySafety(ctx, file)
	if err != nil {
		// Unknown safety must not fail open to the video-copy path this probe is
		// intended to guard. Leave MultiplePPS unset and do not cache or persist
		// the result, so a later request retries the scan without misreporting
		// the cause.
		slog.WarnContext(ctx, "video copy-safety scan failed; disabling stream copy",
			"component", "scanner",
			"file_id", file.ID,
			"error", err,
		)
		return fileWithCopySafety(file, nil, true), nil
	}
	if stale {
		// The caller is holding a snapshot of a generation the row has moved
		// past. Its verdict describes bytes this file no longer contains, so it
		// is treated exactly like an unresolved scan rather than stamped on.
		slog.InfoContext(ctx, "video copy-safety verdict superseded before it could be recorded",
			"component", "scanner",
			"file_id", file.ID,
		)
		return fileWithCopySafety(file, nil, true), nil
	}

	return fileWithMultiplePPS(file, multi), nil
}

// copySafetyFlightKey identifies one generation of one file. The file ID alone
// is not enough: a rewrite in place keeps the row ID and changes the bytes, so a
// caller holding the replacement would otherwise join the flight scanning the
// old file and consume its verdict — condemning a copy-safe replacement, or
// worse, clearing the condemnation of a copy-unsafe one. Only callers that agree
// on the size and mtime are looking at the same bitstream, and therefore only
// they may share a scan.
//
// The mtime is normalized exactly as sameFileModifiedAt normalizes it, so two
// reads of the same generation that differ only in stored precision still share
// a flight. A row with no mtime gets a marker no timestamp can produce: it is a
// generation of its own, not a match for every other.
func copySafetyFlightKey(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	mtime := "none"
	if file.FileModifiedAt != nil {
		mtime = strconv.FormatInt(normalizeFileModifiedAt(*file.FileModifiedAt).UnixMicro(), 10)
	}
	return strconv.Itoa(file.ID) + ":" + strconv.FormatInt(file.FileSize, 10) + ":" + mtime
}

// scanAndPersistCopySafety runs the multi-PPS bitstream scan, persists the
// verdict, and memoizes it. Concurrent callers for the same file *generation*
// share one scan; a failed database write is logged and the scan result is still
// used, since it is correct for this request. The memo remembers that the write
// did not land, so the next lookup for the file retries it — see
// retryUnpersistedCopySafety.
//
// Everything the flight closure records — the persisted row and the process memo
// — is bound to the leader's own snapshot of the file, so a joiner never writes
// another generation's facts. The key is what keeps a joiner from *reading*
// them.
//
// A write refused as stale is neither memoized nor reported as a verdict: the
// row has moved to a generation this scan never read, and both the memo and any
// downstream notification would be facts about bytes nobody is serving.
func (e *PlaybackProbeEnsurer) scanAndPersistCopySafety(ctx context.Context, file *models.MediaFile) (bool, bool, error) {
	fileID := file.ID
	filePath := file.FilePath
	fileSize := file.FileSize
	fileModifiedAt := file.FileModifiedAt

	outcome, err, _ := e.copySafetyFlight.Do(copySafetyFlightKey(file), func() (any, error) {
		timeout := e.timeout
		if timeout < 30*time.Second {
			timeout = 30 * time.Second
		}
		scanCtx, cancel := context.WithTimeout(ctx, timeout)
		multi, err := DetectMultiplePPSH264(scanCtx, e.ffmpegPath, filePath)
		cancel()
		if err != nil {
			return copySafetyOutcome{}, err
		}

		// With no writer there is nowhere for the verdict to land, so it is not
		// pending: nothing would ever clear the flag.
		persisted := true
		if e.copySafetyRepo != nil {
			if writeErr := e.copySafetyRepo.UpdateMultiplePPS(ctx, fileID, multi, fileSize, fileModifiedAt); writeErr != nil {
				if errors.Is(writeErr, ErrStaleCopySafetyScan) {
					slog.InfoContext(ctx, "discarding a video copy-safety verdict for a superseded generation of the file",
						"component", "scanner",
						"file_id", fileID,
					)
					return copySafetyOutcome{stale: true}, nil
				}
				persisted = false
				slog.WarnContext(ctx, "persisting video copy-safety verdict failed",
					"component", "scanner",
					"file_id", fileID,
					"error", writeErr,
				)
			}
		}
		e.storeCopySafety(file, multi, persisted)
		return copySafetyOutcome{multi: multi}, nil
	})
	if err != nil {
		return false, false, err
	}
	result, _ := outcome.(copySafetyOutcome)
	return result.multi, result.stale, nil
}

// copySafetyOutcome is what one shared scan produced, carried through the
// singleflight so joiners learn about a superseded write as well as the verdict.
type copySafetyOutcome struct {
	multi bool
	stale bool
}

// retryUnpersistedCopySafety re-attempts the media_files write for a verdict
// this process already reached but never managed to store. No ffmpeg runs: the
// memo holds the answer, so this is a bare UPDATE.
//
// Without the retry a single failed write is lost until the process restarts.
// The verdict stays correct here, but every other replica keeps rescanning the
// same file and keeps planning fresh sessions onto the copy route it condemns.
// The write shares scanAndPersistCopySafety's singleflight key — the same
// generation-scoped key, so a retry never joins a flight scanning a different
// generation of the row — and so a burst of playback requests for one file
// cannot stampede the row, while a retry racing a scan of its own generation
// simply joins it.
func (e *PlaybackProbeEnsurer) retryUnpersistedCopySafety(ctx context.Context, file *models.MediaFile) {
	if e == nil || file == nil || e.copySafetyRepo == nil {
		return
	}
	if entry, ok := e.memoizedCopySafety(file); !ok || entry.persisted {
		return
	}

	fileID := file.ID
	_, _, _ = e.copySafetyFlight.Do(copySafetyFlightKey(file), func() (any, error) {
		// Re-read inside the flight: a concurrent scan or retry may have landed
		// the write while this caller queued behind it.
		entry, ok := e.memoizedCopySafety(file)
		if !ok || entry.persisted {
			return copySafetyOutcome{multi: entry.multi}, nil
		}
		if err := e.copySafetyRepo.UpdateMultiplePPS(ctx, fileID, entry.multi, entry.size, entry.mtime); err != nil {
			if errors.Is(err, ErrStaleCopySafetyScan) {
				// The row has moved on. The memo stays — it is still the right
				// answer for the snapshot that produced it, and it can no longer
				// match a caller holding the current generation — but there is
				// nothing left to write, so this stops being a failure.
				slog.InfoContext(ctx, "video copy-safety verdict is no longer writable; the row holds another generation",
					"component", "scanner",
					"file_id", fileID,
				)
				return copySafetyOutcome{multi: entry.multi, stale: true}, nil
			}
			slog.WarnContext(ctx, "retrying the video copy-safety verdict write failed",
				"component", "scanner",
				"file_id", fileID,
				"error", err,
			)
			return copySafetyOutcome{multi: entry.multi}, nil
		}
		entry.persisted = true
		e.copySafety.Store(fileID, entry)
		return copySafetyOutcome{multi: entry.multi}, nil
	})
}

// memoizedCopySafety returns the process-cached verdict for file, but only
// while it still describes the file as it stands.
func (e *PlaybackProbeEnsurer) memoizedCopySafety(file *models.MediaFile) (copySafetyResult, bool) {
	cached, ok := e.copySafety.Load(file.ID)
	if !ok {
		return copySafetyResult{}, false
	}
	entry, ok := cached.(copySafetyResult)
	if !ok || !entry.matches(file) {
		return copySafetyResult{}, false
	}
	return entry, true
}

func (e *PlaybackProbeEnsurer) storeCopySafety(file *models.MediaFile, multi, persisted bool) {
	entry := copySafetyResult{size: file.FileSize, multi: multi, persisted: persisted}
	if file.FileModifiedAt != nil {
		mtime := *file.FileModifiedAt
		entry.mtime = &mtime
	}
	e.copySafety.Store(file.ID, entry)
}

// persistedCopySafetyVerdict returns the multi-PPS verdict stored on the
// media_files row, and whether it is still valid for the file as it stands.
// The rule lives on the model because playback reads the same columns from
// files this package never touches.
func persistedCopySafetyVerdict(file *models.MediaFile) (bool, bool) {
	return file.PersistedVideoCopyVerdict()
}

// fileWithMultiplePPS returns a shallow copy of file with the (runtime-only)
// MultiplePPS flag set on its first video track, without mutating the caller's
// file or its shared VideoTracks slice.
func fileWithMultiplePPS(file *models.MediaFile, multi bool) *models.MediaFile {
	value := multi
	return fileWithCopySafety(file, &value, multi)
}

func fileWithCopySafety(file *models.MediaFile, multiplePPS *bool, copyUnsafe bool) *models.MediaFile {
	updated := *file
	tracks := make([]models.VideoTrack, len(file.VideoTracks))
	copy(tracks, file.VideoTracks)
	tracks[0].MultiplePPS = multiplePPS
	tracks[0].VideoCopyUnsafe = copyUnsafe
	updated.VideoTracks = tracks
	return &updated
}

// needsCopySafetyProbe reports whether the file is an H.264 video whose
// multi-PPS copy-safety flag has not yet been computed.
func needsCopySafetyProbe(file *models.MediaFile) bool {
	return file.VideoCopySafetyUnknown()
}

// reprobeMayScanPackets reports whether reprobing this file is likely to hit
// ProbeFile's packet-scan fallback, which demuxes the entire file and cannot
// finish inside the default metadata-probe timeout.
func reprobeMayScanPackets(file *models.MediaFile) bool {
	if file == nil || len(file.VideoTracks) == 0 {
		return false
	}
	return file.Duration <= 0 ||
		videoDurationImplausible(float64(file.Duration), file.FileSize, true)
}

// legacyProbeDurationFixTime marks the revision of the duration-validity rule
// in probe.go. Rows probed before it were judged by an older, weaker rule and
// are re-checked once under the current one. Rows probed after it are
// authoritative: their duration already passed the current rule, and
// re-flagging them would reprobe genuinely short clips on every playback
// decision forever.
//
// Bump this whenever videoDurationImplausible changes, or existing rows never
// re-converge on the improved rule. Last bumped when the implied-bitrate
// ceiling was added, which catches durations the absolute floor missed —
// a feature film probing as 61 seconds passed the old rule untouched.
var legacyProbeDurationFixTime = time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)

func needsLegacyDurationRepair(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	return legacyDurationRepairNeeded(file.Duration, file.FileSize, len(file.VideoTracks) > 0, file.ProbeUpdatedAt)
}

func legacyDurationRepairNeeded(duration int, sizeBytes int64, hasVideo bool, probeUpdatedAt *time.Time) bool {
	if !videoDurationImplausible(float64(duration), sizeBytes, hasVideo) {
		return false
	}
	return probeUpdatedAt == nil || probeUpdatedAt.Before(legacyProbeDurationFixTime)
}

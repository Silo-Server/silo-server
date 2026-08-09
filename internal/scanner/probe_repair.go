package scanner

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
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
	}
	if file.Chapters == nil {
		return true
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

// PlaybackProbeEnsurer repairs missing playback-critical probe metadata on
// demand by running a local ffprobe and persisting the result.
type PlaybackProbeEnsurer struct {
	fileRepo    *FileRepository
	ffprobePath string
	ffmpegPath  string
	timeout     time.Duration
	// copySafety memoizes the multi-PPS bitstream scan per file for the life of
	// the process. It is never persisted: the scan runs on the first playback
	// after a restart and is recomputed lazily thereafter.
	copySafety sync.Map // file ID -> copySafetyResult
}

type copySafetyResult struct {
	size  int64
	multi bool
}

func NewPlaybackProbeEnsurer(fileRepo *FileRepository, ffprobePath, ffmpegPath string, timeout time.Duration) *PlaybackProbeEnsurer {
	return &PlaybackProbeEnsurer{
		fileRepo:    fileRepo,
		ffprobePath: ffprobePath,
		ffmpegPath:  ffmpegPath,
		timeout:     timeout,
	}
}

func (e *PlaybackProbeEnsurer) Ensure(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	if file == nil || e == nil || e.fileRepo == nil {
		return file, nil
	}

	current := file
	if NeedsCriticalProbeRepair(file) && strings.TrimSpace(e.ffprobePath) != "" {
		timeout := e.timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		if reprobeMayScanPackets(file) && timeout < time.Minute {
			timeout = time.Minute
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		probe, err := ProbeFile(probeCtx, e.ffprobePath, file.FilePath)
		cancel()
		if err != nil || probe == nil {
			return file, err
		}
		updated := *file
		applyProbeData(&updated, probe, "local")
		repaired, err := e.fileRepo.Upsert(ctx, updated)
		if err != nil {
			return file, err
		}
		current = repaired
	}

	// Copy-safety analysis is independent of critical probe repair: an
	// already-probed file still needs its one-time multi-PPS scan before the
	// planner can decide whether a video stream-copy is safe.
	return e.ensureCopySafety(ctx, current)
}

// ensureCopySafety computes the multi-PPS copy-safety flag for H.264 files at
// playback start and stamps it on an in-memory copy of the file. The result is
// memoized per process and never written to the database, so it is recomputed
// on the first play after a restart.
func (e *PlaybackProbeEnsurer) ensureCopySafety(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	if !needsCopySafetyProbe(file) || strings.TrimSpace(e.ffmpegPath) == "" {
		return file, nil
	}

	if cached, ok := e.copySafety.Load(file.ID); ok {
		if result, ok := cached.(copySafetyResult); ok && result.size == file.FileSize {
			return fileWithMultiplePPS(file, result.multi), nil
		}
	}

	timeout := e.timeout
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	multi, err := DetectMultiplePPSH264(scanCtx, e.ffmpegPath, file.FilePath)
	cancel()
	if err != nil {
		// Unknown safety must not fail open to the video-copy path this probe is
		// intended to guard. Leave MultiplePPS unset and do not cache the result,
		// so a later request retries the scan without misreporting the cause.
		slog.WarnContext(ctx, "video copy-safety scan failed; disabling stream copy",
			"component", "scanner",
			"file_id", file.ID,
			"error", err,
		)
		return fileWithCopySafety(file, nil, true), nil
	}

	e.copySafety.Store(file.ID, copySafetyResult{size: file.FileSize, multi: multi})
	return fileWithMultiplePPS(file, multi), nil
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
	if file == nil || len(file.VideoTracks) == 0 {
		return false
	}
	if file.VideoTracks[0].MultiplePPS != nil {
		return false
	}
	codec := strings.ToLower(strings.TrimSpace(file.VideoTracks[0].Codec))
	if codec == "" {
		codec = strings.ToLower(strings.TrimSpace(file.CodecVideo))
	}
	return codec == "h264" || codec == "avc" || codec == "avc1"
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

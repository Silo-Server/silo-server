package scanner

import (
	"context"
	"strings"
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
	if strings.TrimSpace(file.ProbeSource) == "" || file.ProbeUpdatedAt == nil {
		return true
	}
	if file.Duration <= 0 {
		return true
	}
	if strings.TrimSpace(file.Container) == "" {
		return true
	}
	if strings.TrimSpace(file.CodecAudio) == "" {
		return true
	}
	if len(file.AudioTracks) == 0 {
		return true
	}
	// Video metadata is playback-critical only for files that actually carry a
	// video stream. Audio-only files (audiobooks, music) legitimately probe to
	// zero video tracks and an empty video codec/resolution; treating that as
	// "needs repair" re-ran ffprobe on every playback decision (applyProbeData
	// only populates video fields under a "video" stream), so an audio-only
	// file would never satisfy the check. Only demand video fields when the
	// file was found to have a video stream.
	if len(file.VideoTracks) > 0 {
		if strings.TrimSpace(file.CodecVideo) == "" || strings.TrimSpace(file.Resolution) == "" {
			return true
		}
	}
	if file.Chapters == nil {
		return true
	}
	return false
}

// PlaybackProbeEnsurer repairs missing playback-critical probe metadata on
// demand by running a local ffprobe and persisting the result.
type PlaybackProbeEnsurer struct {
	fileRepo    *FileRepository
	ffprobePath string
	timeout     time.Duration
}

func NewPlaybackProbeEnsurer(fileRepo *FileRepository, ffprobePath string, timeout time.Duration) *PlaybackProbeEnsurer {
	return &PlaybackProbeEnsurer{
		fileRepo:    fileRepo,
		ffprobePath: ffprobePath,
		timeout:     timeout,
	}
}

func (e *PlaybackProbeEnsurer) Ensure(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	if file == nil || !NeedsCriticalProbeRepair(file) {
		return file, nil
	}
	if e == nil || e.fileRepo == nil || strings.TrimSpace(e.ffprobePath) == "" {
		return file, nil
	}

	timeout := e.timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	probe, err := ProbeFile(probeCtx, e.ffprobePath, file.FilePath)
	if err != nil || probe == nil {
		return file, err
	}

	updated := *file
	applyProbeData(&updated, probe, "local")
	return e.fileRepo.Upsert(ctx, updated)
}

package downloads

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Artifact status constants (download_artifacts.status).
const (
	ArtifactQueued  = "queued"
	ArtifactRunning = "running"
	ArtifactReady   = "ready"
	ArtifactFailed  = "failed"
)

// ErrNoArtifactJob is returned by the queue when no claimable job exists.
var ErrNoArtifactJob = errors.New("no claimable artifact job")

// Artifact is a prepared (remux/transcode) file, deduplicated by
// (media_file_id, format, params_hash), and a row in the durable encode queue.
type Artifact struct {
	ID                string
	MediaFileID       int
	Format            string // remux | transcode
	ParamsHash        string
	Container         string
	CodecVideo        string
	CodecAudio        string
	Resolution        string
	AudioTrackIndex   int
	TargetBitrateKbps int
	OutputPath        string
	FileSize          int64
	Status            string
	ErrorMessage      string
	Attempts          int
	MaxAttempts       int
	LeaseOwner        string
	LeaseExpiresAt    *time.Time
	NextRetryAt       *time.Time
	CreatedAt         time.Time
	CompletedAt       *time.Time
	LastUsedAt        time.Time
}

// paramsHash is the dedup key for an encode target:
// sha256(format | container | codec_video | codec_audio | resolution | audio_track_index | bitrate | subtitle_burn_in).
func paramsHash(format, container, codecVideo, codecAudio, resolution string, audioTrackIndex, targetBitrateKbps int, subtitleBurnIn bool) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%d|%d|%t",
		format, container, codecVideo, codecAudio, resolution, audioTrackIndex, targetBitrateKbps, subtitleBurnIn)))
	return hex.EncodeToString(sum[:])
}

// defaultTranscodeDir is config's TranscodeDir default and roots
// prepared download artifacts when neither download.artifact_dir nor the
// transcode dir is configured. Keeping it absolute avoids writing artifacts
// relative to the process working directory.
const defaultTranscodeDir = config.DefaultTranscodeDir

// effectiveArtifactDir resolves where prepared artifacts are written: the
// configured download.artifact_dir when set, otherwise a dedicated directory
// alongside the transcode dir. The result is always rooted at a real volume,
// never "" (which would land in the process cwd).
//
// Artifacts live as a SIBLING of the transcode dir, never inside it:
// CleanupOrphanedTranscodeDirs deletes every non-active subdirectory of the
// transcode dir, so an artifact dir nested under it would be wiped on the next
// transcode sweep.
func effectiveArtifactDir(artifactDir, transcodeDir string) string {
	if artifactDir != "" {
		return artifactDir
	}
	if transcodeDir == "" {
		transcodeDir = defaultTranscodeDir
	}
	return filepath.Join(filepath.Dir(transcodeDir), "silo-download-artifacts")
}

// artifactOutputPath derives a deterministic output path from
// (media_file_id, format, params_hash) so a reclaimed job targets the same file.
func artifactOutputPath(dir string, mediaFileID int, format, hash string) string {
	short := hash
	if len(short) > 16 {
		short = short[:16]
	}
	return filepath.Join(dir, fmt.Sprintf("%d_%s_%s.mp4", mediaFileID, format, short))
}

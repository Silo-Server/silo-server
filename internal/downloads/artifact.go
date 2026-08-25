package downloads

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// Artifact status constants (download_artifacts.status).
const (
	ArtifactQueued         = "queued"
	ArtifactRunning        = "running"
	ArtifactToneMapQueued  = "tone_map_queued"
	ArtifactToneMapRunning = "tone_map_running"
	ArtifactToneMapReady   = "tone_map_ready"
	ArtifactReady          = "ready"
	ArtifactFailed         = "failed"
)

func queuedArtifactStatus(mode tonemap.Mode) string {
	if mode != "" {
		return ArtifactToneMapQueued
	}
	return ArtifactQueued
}

func artifactReady(artifact *Artifact) bool {
	return artifact != nil && (artifact.Status == ArtifactReady || artifact.Status == ArtifactToneMapReady)
}

// ErrNoArtifactJob is returned by the queue when no claimable job exists.
var ErrNoArtifactJob = errors.New("no claimable artifact job")

// Artifact is a prepared (remux/transcode) file, deduplicated by
// (media_file_id, format, params_hash), and a row in the durable encode queue.
type Artifact struct {
	ID                         string
	MediaFileID                int
	Format                     string // remux | transcode
	ParamsHash                 string
	Container                  string
	CodecVideo                 string
	CodecAudio                 string
	Resolution                 string
	AudioTrackIndex            int
	TargetBitrateKbps          int
	ToneMapPolicy              tonemap.Policy
	ToneMapMode                tonemap.Mode
	ToneMapSourceKind          tonemap.SourceKind
	ToneMapRecipeVersion       string
	ToneMapPreflightRequired   bool
	ToneMapSourceRevision      string
	ToneMapDVConfigPresent     bool
	ToneMapDVBLCompatIDPresent bool
	ToneMapDVBLPresent         bool
	ToneMapDVRPUPresent        bool
	OutputPath                 string
	OriginNodeID               int
	OriginNodeURL              string
	OriginNodeGroup            string
	OriginArtifactID           string
	FileSize                   int64
	Status                     string
	ErrorMessage               string
	Attempts                   int
	MaxAttempts                int
	LeaseOwner                 string
	LeaseExpiresAt             *time.Time
	NextRetryAt                *time.Time
	CreatedAt                  time.Time
	CompletedAt                *time.Time
	LastUsedAt                 time.Time
}

type paramsHashParams struct {
	format, container, codecVideo, codecAudio, resolution string
	audioTrackIndex, targetBitrateKbps                    int
	subtitleBurnIn                                        bool
	policy                                                tonemap.Policy
	mode                                                  tonemap.Mode
	sourceKind                                            tonemap.SourceKind
	recipeVersion                                         string
	preflightRequired                                     bool
	sourceRevision                                        tonemap.SourceRevision
}

// paramsHash is the test-only legacy wrapper for an encode target without a
// tone-map recipe.
func paramsHash(format, container, codecVideo, codecAudio, resolution string, audioTrackIndex, targetBitrateKbps int, subtitleBurnIn bool) string {
	return paramsHashWithToneMapRevision(paramsHashParams{
		format: format, container: container, codecVideo: codecVideo, codecAudio: codecAudio, resolution: resolution,
		audioTrackIndex: audioTrackIndex, targetBitrateKbps: targetBitrateKbps, subtitleBurnIn: subtitleBurnIn,
		policy: tonemap.PolicyNone,
	})
}

// paramsHashWithToneMap extends the legacy encode identity with the frozen
// tone-map policy and recipe while preserving old hashes for ordinary encodes.
func paramsHashWithToneMap(format, container, codecVideo, codecAudio, resolution string, audioTrackIndex, targetBitrateKbps int, subtitleBurnIn bool, policy tonemap.Policy, mode tonemap.Mode, sourceKind tonemap.SourceKind, recipeVersion string) string {
	return paramsHashWithToneMapRevision(paramsHashParams{
		format: format, container: container, codecVideo: codecVideo, codecAudio: codecAudio, resolution: resolution,
		audioTrackIndex: audioTrackIndex, targetBitrateKbps: targetBitrateKbps, subtitleBurnIn: subtitleBurnIn,
		policy: policy, mode: mode, sourceKind: sourceKind, recipeVersion: recipeVersion,
	})
}

// paramsHashWithToneMapRevision binds prepared-output deduplication to the
// source revision and preflight requirement in addition to the executor recipe.
func paramsHashWithToneMapRevision(params paramsHashParams) string {
	input := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%d|%t", params.format, params.container, params.codecVideo, params.codecAudio, params.resolution, params.audioTrackIndex, params.targetBitrateKbps, params.subtitleBurnIn)
	if (params.policy != "" && params.policy != tonemap.PolicyNone) || params.mode != "" || params.sourceKind != "" || params.recipeVersion != "" || params.preflightRequired || !params.sourceRevision.IsZero() {
		input += fmt.Sprintf("|%s|%s|%s|%s|%t|%s", params.policy, params.mode, params.sourceKind, params.recipeVersion, params.preflightRequired, params.sourceRevision.Fingerprint())
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

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
	return config.EffectiveDownloadArtifactDir(artifactDir, transcodeDir)
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

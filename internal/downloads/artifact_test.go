package downloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestParamsHashStableAndDistinct(t *testing.T) {
	base := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false)
	if base == "" || len(base) != 64 {
		t.Fatalf("params hash should be a 64-char sha256 hex, got %q", base)
	}
	// Deterministic for identical inputs (dedup key).
	if again := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false); again != base {
		t.Fatalf("params hash not stable: %q != %q", base, again)
	}
	// Distinct when any parameter differs.
	for _, other := range []string{
		paramsHash("remux", "mp4", "h264", "aac", "1080p", -1, 10000, false),
		paramsHash("transcode", "mp4", "hevc", "aac", "1080p", -1, 10000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "720p", -1, 10000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", 1, 10000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 5000, false),
		paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, true),
	} {
		if other == base {
			t.Fatalf("params hash collision: %q", other)
		}
	}
}

// TestParamsHashIncludesFrozenToneMapRecipe verifies recipe changes produce distinct artifacts.
func TestParamsHashIncludesFrozenToneMapRecipe(t *testing.T) {
	base := paramsHash("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false)
	legacy := paramsHashWithToneMap("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false, tonemap.PolicyNone, "", "", "")
	hashWithRevision := func(policy tonemap.Policy, mode tonemap.Mode, sourceKind tonemap.SourceKind, recipeVersion string, preflightRequired bool, sourceRevision tonemap.SourceRevision) string {
		return paramsHashWithToneMapRevision(paramsHashParams{
			format: "transcode", container: "mp4", codecVideo: "h264", codecAudio: "aac", resolution: "1080p",
			audioTrackIndex: -1, targetBitrateKbps: 10000,
			policy: policy, mode: mode, sourceKind: sourceKind, recipeVersion: recipeVersion,
			preflightRequired: preflightRequired, sourceRevision: sourceRevision,
		})
	}
	if legacy != base {
		t.Fatalf("non-tone-mapped hash changed: %q != %q", legacy, base)
	}
	toneMapped := paramsHashWithToneMap("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false,
		tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3)
	for name, other := range map[string]string{
		"non-tone-mapped": base,
		"source kind": paramsHashWithToneMap("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false,
			tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourceHLG, playback.TransformationHDRToSDRToneMapRecipeVersionV3),
		"recipe version": paramsHashWithToneMap("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false,
			tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, "2"),
		"execution mode": paramsHashWithToneMap("transcode", "mp4", "h264", "aac", "1080p", -1, 10000, false,
			tonemap.PolicyHardwareThenSoftware, tonemap.ModeSoftware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3),
	} {
		if other == toneMapped {
			t.Fatalf("%s did not affect tone-map artifact hash", name)
		}
	}
	revision := tonemap.SourceRevision{MediaFileID: 1, FileSize: 10, FileModifiedUnixNano: 100, StreamSignature: "stream"}
	for name, other := range map[string]string{
		"policy only":          hashWithRevision(tonemap.PolicySoftwareOnly, "", "", "", false, tonemap.SourceRevision{}),
		"preflight only":       hashWithRevision(tonemap.PolicyNone, "", "", "", true, tonemap.SourceRevision{}),
		"source revision only": hashWithRevision(tonemap.PolicyNone, "", "", "", false, revision),
	} {
		if other == base {
			t.Fatalf("%s did not enter the tone-map artifact hash", name)
		}
	}
	revisionHash := hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3, true, revision)
	for name, other := range map[string]string{
		"preflight":       hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3, false, revision),
		"source revision": hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3, true, tonemap.SourceRevision{MediaFileID: 1, FileSize: 11, FileModifiedUnixNano: 100, StreamSignature: "stream"}),
	} {
		if other == revisionHash {
			t.Fatalf("%s did not affect tone-map artifact hash", name)
		}
	}
}

// TestBuildOptsWarnsWithoutLoggingInvalidSourceRevisionValue verifies invalid revisions are handled safely.
func TestBuildOptsWarnsWithoutLoggingInvalidSourceRevisionValue(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	manager := &ArtifactManager{}
	artifact := &Artifact{ID: "artifact-invalid-revision", ToneMapMode: tonemap.ModeSoftware, ToneMapSourceRevision: "sensitive-invalid-value"}
	opts := manager.buildOpts(&models.MediaFile{}, artifact)
	if opts.ToneMapSourceRevision.MediaFileID != -1 {
		t.Fatalf("fallback source revision = %#v", opts.ToneMapSourceRevision)
	}
	output := logs.String()
	if !strings.Contains(output, "artifact-invalid-revision") || !strings.Contains(output, `"source_revision_length":23`) {
		t.Fatalf("warning = %s", output)
	}
	if strings.Contains(output, artifact.ToneMapSourceRevision) {
		t.Fatalf("warning exposed the stored source revision: %s", output)
	}
}

func TestValidateArtifactToneMapRevisionRejectsCatalogProbeDrift(t *testing.T) {
	file := hdrDownloadTestFile()
	revision := tonemap.RevisionForFile(file)
	artifact := &Artifact{ToneMapMode: tonemap.ModeSoftware, ToneMapSourceRevision: revision.Encode()}
	if err := validateArtifactToneMapRevision(file, artifact); err != nil {
		t.Fatalf("validateArtifactToneMapRevision(original) error = %v", err)
	}

	reprobed := *file
	reprobed.VideoTracks = append([]models.VideoTrack(nil), file.VideoTracks...)
	reprobed.VideoTracks[0].BitDepth++
	if err := validateArtifactToneMapRevision(&reprobed, artifact); err == nil {
		t.Fatal("validateArtifactToneMapRevision accepted changed probe metadata")
	}
}

func TestArtifactReadyStatusIncludesFencedToneMapRows(t *testing.T) {
	if !artifactReady(&Artifact{Status: ArtifactReady}) {
		t.Fatal("ordinary ready artifact was not ready")
	}
	if !artifactReady(&Artifact{Status: ArtifactToneMapReady}) {
		t.Fatal("fenced tone-map ready artifact was not ready")
	}
	if artifactReady(&Artifact{Status: ArtifactToneMapRunning}) {
		t.Fatal("running tone-map artifact was ready")
	}
}

func TestToneMapArtifactExecutionFingerprintRejectsRefreshedPathOrDuration(t *testing.T) {
	manager := &ArtifactManager{}
	file := hdrDownloadTestFile()
	file.FilePath = "/media/original.mkv"
	file.Duration = 3600
	artifact := &Artifact{
		ID: "artifact-1", CodecVideo: "h264", CodecAudio: "aac", ToneMapMode: tonemap.ModeSoftware,
		ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapRecipeVersion:  playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.RevisionForFile(file).Encode(),
	}
	original := manager.buildOpts(file, artifact)
	artifact.ParamsHash = downloadprepare.NewRequest(artifact.ID, original).ExecutionFingerprint()
	if !toneMapArtifactExecutionFingerprintMatches(artifact, original) {
		t.Fatal("frozen execution request did not match its artifact hash")
	}
	changedPath := original
	changedPath.InputPath = "/media/replaced.mkv"
	if toneMapArtifactExecutionFingerprintMatches(artifact, changedPath) {
		t.Fatal("changed input path reused the frozen artifact hash")
	}
	changedDuration := original
	changedDuration.TotalDuration++
	if toneMapArtifactExecutionFingerprintMatches(artifact, changedDuration) {
		t.Fatal("changed duration reused the frozen artifact hash")
	}
}

func TestResolveToneMapTargetRejectsHDRTranscodeWhenToneMapPolicyUnavailable(t *testing.T) {
	file := hdrDownloadTestFile()
	target := playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"}
	for _, test := range []struct {
		name     string
		settings SettingsReader
	}{
		{name: "settings unavailable"},
		{name: "policy disabled", settings: staticDownloadSettings{
			config.Allow4KTranscodeSettingKey:                 "false",
			config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &ArtifactManager{settings: test.settings}
			_, err := manager.resolveToneMapTarget(context.Background(), file, target)
			if !errors.Is(err, ErrQualityUnavailable) {
				t.Fatalf("resolveToneMapTarget() error = %v, want ErrQualityUnavailable", err)
			}
		})
	}
}

func TestResolveToneMapTargetRetainsFourKRestrictionWhenPolicyDisabled(t *testing.T) {
	file := hdrDownloadTestFile()
	file.VideoTracks[0].Height = 2160
	manager := &ArtifactManager{settings: staticDownloadSettings{
		config.Allow4KTranscodeSettingKey:                 "false",
		config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
	}}
	_, err := manager.resolveToneMapTarget(context.Background(), file, playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"})
	if !errors.Is(err, ErrQualityUnavailable) || !strings.Contains(err.Error(), "4K transcoding is disabled") {
		t.Fatalf("resolveToneMapTarget() error = %v, want disabled 4K ErrQualityUnavailable", err)
	}
}

func TestResolveToneMapTargetFailsClosedForFourKWhenSettingsUnavailable(t *testing.T) {
	file := hdrDownloadTestFile()
	file.VideoTracks[0].Height = 2160
	manager := &ArtifactManager{}

	_, err := manager.resolveToneMapTarget(context.Background(), file, playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"})
	if !errors.Is(err, ErrQualityUnavailable) || !strings.Contains(err.Error(), "settings are unavailable") {
		t.Fatalf("resolveToneMapTarget() error = %v, want unavailable-settings ErrQualityUnavailable", err)
	}
}

func TestResolveToneMapTargetClassifiesSettingsReadFailureAsCapabilityUnavailable(t *testing.T) {
	manager := &ArtifactManager{settings: failingDownloadSettings{err: context.DeadlineExceeded}}

	_, err := manager.resolveToneMapTarget(context.Background(), hdrDownloadTestFile(), playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCapabilityUnavailable) || errors.Is(err, ErrQualityUnavailable) {
		t.Fatalf("resolveToneMapTarget() error = %v, want retryable settings capability failure", err)
	}
}

func TestToneMapPlanningTimeoutAllowsColdProbeWithoutLocalFallback(t *testing.T) {
	provider := capacityAwareToneMapPreparer{}
	if got := toneMapPlanningTimeout(provider, true); got != toneMapPlanTimeout {
		t.Fatalf("planning timeout with local fallback = %s, want %s", got, toneMapPlanTimeout)
	}
	if got := toneMapPlanningTimeout(provider, false); got != remoteOnlyToneMapPlanTimeout {
		t.Fatalf("remote-only planning timeout = %s, want %s", got, remoteOnlyToneMapPlanTimeout)
	}
}

func TestResolveToneMapTargetAppliesFourKRestrictionBeforeDynamicRangeHandling(t *testing.T) {
	target := playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"}
	settings := staticDownloadSettings{
		config.Allow4KTranscodeSettingKey:                 "false",
		config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
	}
	for _, test := range []struct {
		name  string
		track models.VideoTrack
	}{
		{name: "SDR", track: models.VideoTrack{Height: 2160, VideoRange: "SDR"}},
		{name: "unknown dynamic range", track: models.VideoTrack{Height: 2160}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &ArtifactManager{settings: settings}
			file := &models.MediaFile{VideoTracks: []models.VideoTrack{test.track}}
			_, err := manager.resolveToneMapTarget(context.Background(), file, target)
			if !errors.Is(err, ErrQualityUnavailable) || !strings.Contains(err.Error(), "4K transcoding is disabled") {
				t.Fatalf("resolveToneMapTarget() error = %v, want disabled 4K ErrQualityUnavailable", err)
			}
		})
	}
}

func TestResolveToneMapTargetRejectsUnclassifiableHDRWhenPolicyDisabled(t *testing.T) {
	manager := &ArtifactManager{settings: staticDownloadSettings{
		config.Allow4KTranscodeSettingKey:                 "true",
		config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
	}}
	file := &models.MediaFile{HDR: true, VideoTracks: []models.VideoTrack{{
		Codec: "hevc", DolbyVision: "Profile 5", DVProfile: 5, VideoRange: "DolbyVision",
	}}}
	target := playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"}
	_, err := manager.resolveToneMapTarget(context.Background(), file, target)
	if !errors.Is(err, ErrQualityUnavailable) {
		t.Fatalf("resolveToneMapTarget() error = %v, want ErrQualityUnavailable", err)
	}
}

func TestResolveToneMapTargetReturnsCancellationWhenExecutorInventoryIsEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := &ArtifactManager{settings: staticDownloadSettings{
		config.Allow4KTranscodeSettingKey:                 "true",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	_, err := manager.resolveToneMapTarget(ctx, hdrDownloadTestFile(), playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveToneMapTarget() error = %v, want context.Canceled", err)
	}
}

type unavailableToneMapPreparer struct{}

func (unavailableToneMapPreparer) PrepareFile(context.Context, string, playback.TranscodeOpts, string) (PreparedArtifact, error) {
	return PreparedArtifact{}, nil
}

func (unavailableToneMapPreparer) ToneMapCapabilities(context.Context) (tonemap.Capabilities, error) {
	return nil, context.DeadlineExceeded
}

func (unavailableToneMapPreparer) LocalFallbackAllowed(context.Context) bool { return false }

type capacityAwareToneMapPreparer struct {
	capabilities tonemap.Capabilities
	available    map[tonemap.Mode]bool
	capacityErr  map[tonemap.Mode]error
}

func (p capacityAwareToneMapPreparer) PrepareFile(context.Context, string, playback.TranscodeOpts, string) (PreparedArtifact, error) {
	return PreparedArtifact{}, nil
}

func (p capacityAwareToneMapPreparer) ToneMapCapabilities(context.Context) (tonemap.Capabilities, error) {
	return p.capabilities, nil
}

func (p capacityAwareToneMapPreparer) LocalFallbackAllowed(context.Context) bool { return false }

func (p capacityAwareToneMapPreparer) ToneMapModeAvailable(_ context.Context, mode tonemap.Mode, _ tonemap.SourceKind) (bool, error) {
	return p.available[mode], p.capacityErr[mode]
}

func TestResolveToneMapTargetDistinguishesSaturatedExecutorsFromUnavailableQuality(t *testing.T) {
	tests := []struct {
		name         string
		hardware     bool
		software     bool
		capabilities tonemap.Capabilities
	}{
		{
			name:     "hardware only",
			hardware: true,
			capabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
		},
		{
			name:     "software only",
			software: true,
			capabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
		},
		{
			name:     "hardware then software",
			hardware: true,
			software: true,
			capabilities: tonemap.Capabilities{
				{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
				{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &ArtifactManager{
				preparer: capacityAwareToneMapPreparer{capabilities: test.capabilities, available: map[tonemap.Mode]bool{}},
				settings: staticDownloadSettings{
					config.Allow4KTranscodeSettingKey:                 "true",
					config.PlaybackTranscodeHardwareToneMapSettingKey: strconv.FormatBool(test.hardware),
					config.PlaybackTranscodeSoftwareToneMapSettingKey: strconv.FormatBool(test.software),
				},
			}
			target := playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"}
			got, err := manager.resolveToneMapTarget(context.Background(), hdrDownloadTestFile(), target)
			if err == nil {
				t.Fatal("resolveToneMapTarget() error = nil, want retryable capacity exhaustion")
			}
			if !errors.Is(err, ErrCapacityUnavailable) || errors.Is(err, ErrQualityUnavailable) {
				t.Fatalf("resolveToneMapTarget() error = %v, want ErrCapacityUnavailable distinct from ErrQualityUnavailable", err)
			}
			if got != target {
				t.Fatalf("target = %#v, want unfrozen %#v", got, target)
			}
		})
	}
}

func TestResolveToneMapTargetKeepsPermanentCapabilityAndPartialInventoryOutcomes(t *testing.T) {
	t.Run("incompatible permitted mode remains quality unavailable", func(t *testing.T) {
		manager := &ArtifactManager{
			preparer: capacityAwareToneMapPreparer{
				capabilities: tonemap.Capabilities{{
					Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
				}},
				available: map[tonemap.Mode]bool{tonemap.ModeSoftware: true},
			},
			settings: staticDownloadSettings{
				config.Allow4KTranscodeSettingKey:                 "true",
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			},
		}
		_, err := manager.resolveToneMapTarget(context.Background(), hdrDownloadTestFile(), playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"})
		if !errors.Is(err, ErrQualityUnavailable) {
			t.Fatalf("resolveToneMapTarget() error = %v, want permanent ErrQualityUnavailable", err)
		}
	})

	t.Run("partial inventory error remains retryable when capacity is unknown", func(t *testing.T) {
		manager := &ArtifactManager{
			preparer: capacityAwareToneMapPreparer{
				capabilities: tonemap.Capabilities{{
					Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
				}},
				available:   map[tonemap.Mode]bool{},
				capacityErr: map[tonemap.Mode]error{tonemap.ModeHardware: context.DeadlineExceeded},
			},
			settings: staticDownloadSettings{
				config.Allow4KTranscodeSettingKey:                 "true",
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			},
		}
		_, err := manager.resolveToneMapTarget(context.Background(), hdrDownloadTestFile(), playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCapabilityUnavailable) || errors.Is(err, ErrCapacityUnavailable) || errors.Is(err, ErrQualityUnavailable) {
			t.Fatalf("resolveToneMapTarget() error = %v, want retryable capability error", err)
		}
	})
}

func TestResolveToneMapTargetHashesSoftwareWhenHardwareHasNoCapacity(t *testing.T) {
	preparer := capacityAwareToneMapPreparer{
		capabilities: tonemap.Capabilities{
			{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
			{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
		},
		available: map[tonemap.Mode]bool{tonemap.ModeSoftware: true},
	}
	manager := &ArtifactManager{
		preparer: preparer,
		settings: staticDownloadSettings{
			config.Allow4KTranscodeSettingKey:                 "true",
			config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
		},
	}
	target, err := manager.resolveToneMapTarget(context.Background(), hdrDownloadTestFile(), playback.PrepareTarget{
		Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", AudioTrackIndex: -1, TargetBitrateKbps: 10000,
	})
	if err != nil {
		t.Fatalf("resolveToneMapTarget() error = %v", err)
	}
	hashForMode := func(mode tonemap.Mode) string {
		return paramsHashWithToneMapRevision(paramsHashParams{
			format: "transcode", container: target.Container, codecVideo: target.CodecVideo, codecAudio: target.CodecAudio, resolution: target.Resolution,
			audioTrackIndex: target.AudioTrackIndex, targetBitrateKbps: target.TargetBitrateKbps,
			policy: target.ToneMapPolicy, mode: mode, sourceKind: target.ToneMapSourceKind,
			recipeVersion: target.ToneMapRecipeVersion, preflightRequired: target.ToneMapPreflightRequired, sourceRevision: target.ToneMapSourceRevision,
		})
	}
	gotHash := hashForMode(target.ToneMapMode)
	softwareHash := hashForMode(tonemap.ModeSoftware)
	hardwareHash := hashForMode(tonemap.ModeHardware)
	if target.ToneMapMode != tonemap.ModeSoftware || gotHash != softwareHash || gotHash == hardwareHash {
		t.Fatalf("tone-map target = mode %q hash %q; want software hash %q and not hardware hash %q", target.ToneMapMode, gotHash, softwareHash, hardwareHash)
	}
}

func TestResolveToneMapTargetReturnsTransientCapabilityErrorBeforeFreezingRecipe(t *testing.T) {
	manager := &ArtifactManager{
		preparer: unavailableToneMapPreparer{},
		settings: staticDownloadSettings{
			config.Allow4KTranscodeSettingKey:                 "true",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
		},
	}
	target := playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"}
	got, err := manager.resolveToneMapTarget(context.Background(), hdrDownloadTestFile(), target)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolveToneMapTarget() error = %v, want transient capability deadline", err)
	}
	if got != target {
		t.Fatalf("target = %#v, want unfrozen %#v", got, target)
	}
}

func hdrDownloadTestFile() *models.MediaFile {
	return &models.MediaFile{
		HDR: true,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Height: 1080, BitDepth: 10, VideoRange: "HDR10",
			ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		}},
	}
}

func TestArtifactOutputPathDeterministic(t *testing.T) {
	p1 := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef")
	p2 := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef")
	if p1 != p2 {
		t.Fatalf("output path not deterministic: %q != %q", p1, p2)
	}
	if !strings.HasPrefix(p1, "/var/artifacts/") || !strings.HasSuffix(p1, ".mp4") {
		t.Fatalf("unexpected output path %q", p1)
	}
	if !strings.Contains(p1, "42_transcode_") {
		t.Fatalf("output path missing identity components: %q", p1)
	}
}

type lifecycleTestPreparer struct {
	deleted       string
	prepared      PreparedArtifact
	resolveErr    error
	resolvedNode  int
	resolvedURL   string
	resolvedGroup string
	stat          downloadprepare.Result
	statError     error
	statIDs       []string
	statStarted   atomic.Int32
	statWait      bool
	deleteErr     error
	deleteStarted chan struct{}
	deleteWait    bool
}

func (p *lifecycleTestPreparer) PrepareFile(context.Context, string, playback.TranscodeOpts, string) (PreparedArtifact, error) {
	return p.prepared, nil
}
func (p *lifecycleTestPreparer) ResolveArtifact(_ context.Context, artifact *Artifact) error {
	if p.resolvedNode != 0 {
		artifact.OriginNodeID = p.resolvedNode
	}
	if p.resolvedURL != "" {
		artifact.OriginNodeURL = p.resolvedURL
		artifact.OriginNodeGroup = p.resolvedGroup
	}
	return p.resolveErr
}
func (p *lifecycleTestPreparer) StatArtifact(ctx context.Context, artifact *Artifact) (downloadprepare.Result, error) {
	if p.statWait {
		p.statStarted.Add(1)
		select {
		case <-ctx.Done():
			return downloadprepare.Result{}, ctx.Err()
		case <-time.After(2 * time.Second):
			return downloadprepare.Result{}, context.DeadlineExceeded
		}
	}
	p.statIDs = append(p.statIDs, artifact.ID)
	return p.stat, p.statError
}
func (p *lifecycleTestPreparer) DeleteArtifact(ctx context.Context, artifact *Artifact) error {
	p.deleted = artifact.OriginArtifactID
	if p.deleteStarted != nil {
		close(p.deleteStarted)
	}
	if p.deleteWait {
		<-ctx.Done()
		return ctx.Err()
	}
	return p.deleteErr
}

func TestArtifactManagerDeletesRemoteBytesThroughOwningNode(t *testing.T) {
	preparer := &lifecycleTestPreparer{}
	m := &ArtifactManager{preparer: preparer}
	artifact := &Artifact{ID: "row-1", OriginNodeURL: "http://transcode", OriginArtifactID: "opaque-1"}
	if !m.deleteArtifactBytes(context.Background(), artifact) {
		t.Fatal("remote cleanup failed")
	}
	if preparer.deleted != "opaque-1" {
		t.Fatalf("deleted = %q", preparer.deleted)
	}
}

func TestRemoteArtifactRequeueRejectsNonPositiveNodeID(t *testing.T) {
	manager := &ArtifactManager{repo: &ArtifactRepository{}}
	_, err := manager.requeueRemoteArtifactNow(context.Background(), &Artifact{
		ID: "row-1", OriginNodeID: -1, OriginNodeURL: "http://transcode", OriginArtifactID: "opaque-1",
	}, "invalid node")
	if err == nil {
		t.Fatal("expected invalid remote locator error")
	}
}

func TestEffectiveArtifactDir(t *testing.T) {
	// Explicit config wins verbatim.
	if got := effectiveArtifactDir("/data/artifacts", "/tmp/silo-transcode"); got != "/data/artifacts" {
		t.Fatalf("explicit dir = %q, want /data/artifacts", got)
	}
	// Unset: a sibling of the transcode dir, never inside it (the transcode
	// cleanup sweep would otherwise delete a nested artifact dir) and never the
	// process cwd (a relative/empty path).
	got := effectiveArtifactDir("", "/var/lib/silo/transcode")
	if got != "/var/lib/silo/silo-download-artifacts" {
		t.Fatalf("default dir = %q, want sibling of transcode dir", got)
	}
	if strings.HasPrefix(got, "/var/lib/silo/transcode/") {
		t.Fatalf("default dir %q is nested under the transcode dir", got)
	}
	// Unset transcode dir falls back to the absolute default root, not "".
	fallback := effectiveArtifactDir("", "")
	if !strings.HasPrefix(fallback, "/") {
		t.Fatalf("fallback dir %q is not absolute", fallback)
	}
}

type fakeUserRepo struct{ user *models.User }

func (f fakeUserRepo) GetByID(context.Context, int) (*models.User, error) { return f.user, nil }

func TestCapabilityQualityPresetsGating(t *testing.T) {
	newSvc := func(user *models.User, transcodeEnabled bool) *Service {
		cfg := config.DownloadConfig{Enabled: true, TranscodeEnabled: transcodeEnabled}
		return NewService(nil, nil, nil, nil, nil, nil, fakeUserRepo{user}, nil, nil, &cfg)
	}
	allowAll := &models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(true)}

	// No artifact pipeline wired → only original is fulfillable.
	svc := newSvc(allowAll, true)
	capInfo, err := svc.Capability(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original" {
		t.Fatalf("quality presets without pipeline = %q, want original", got)
	}

	// Pipeline wired + transcode server/user gates open → full bitrate ladder.
	svc = newSvc(allowAll, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original,20mbps,10mbps,5mbps,2mbps,1mbps" {
		t.Fatalf("quality presets with pipeline = %q, want full ladder", got)
	}

	// Transcode gated off (user flag) → original only.
	svc = newSvc(&models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(false)}, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original" {
		t.Fatalf("quality presets with transcode gated = %q, want original", got)
	}

	// Download permission revoked → an EMPTY array, never nil: the capability
	// contract documents quality_presets as an array, and a nil slice would
	// serialize as JSON null and break typed clients.
	svc = newSvc(&models.User{DownloadAllowed: ptrBool(false)}, true)
	capInfo, _ = svc.Capability(context.Background(), 1)
	if capInfo.QualityPresets == nil {
		t.Fatal("quality presets for a denied user must be an empty array, not nil")
	}
	if len(capInfo.QualityPresets) != 0 {
		t.Fatalf("quality presets for a denied user = %v, want empty", capInfo.QualityPresets)
	}
	if b, err := json.Marshal(capInfo.QualityPresets); err != nil || string(b) != "[]" {
		t.Fatalf("quality presets serialize to %s (%v), want []", b, err)
	}
}

// TestTriggerDrainDoesNotBlockCaller pins the async-dispatch contract:
// Ensure runs on request goroutines and the kick drains the whole encode
// queue (ffmpeg included), so triggerDrain must return without waiting on it.
func TestTriggerDrainDoesNotBlockCaller(t *testing.T) {
	m := &ArtifactManager{}
	started := make(chan struct{})
	release := make(chan struct{})
	m.SetKick(func() {
		close(started)
		<-release
	})

	done := make(chan struct{})
	go func() {
		m.triggerDrain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerDrain blocked on the kick; it must dispatch asynchronously")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("kick was never invoked")
	}
	close(release)
}

func ptrBool(value bool) *bool { return &value }

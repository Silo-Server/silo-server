package downloads

import (
	"context"
	"encoding/json"
	"errors"
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
	params := paramsHashParams{
		format: "transcode", container: "mp4", codecVideo: "h264", codecAudio: "aac", resolution: "1080p",
		audioTrackIndex: -1, targetBitrateKbps: 10000, policy: tonemap.PolicyNone,
	}
	base := paramsHashWithToneMapRevision(params)
	if again := paramsHashWithToneMapRevision(params); again != base {
		t.Fatalf("params hash not stable: %q != %q", base, again)
	}
	for name, change := range map[string]func(*paramsHashParams){
		"format":           func(p *paramsHashParams) { p.format = "remux" },
		"video codec":      func(p *paramsHashParams) { p.codecVideo = "hevc" },
		"resolution":       func(p *paramsHashParams) { p.resolution = "720p" },
		"audio track":      func(p *paramsHashParams) { p.audioTrackIndex = 1 },
		"bitrate":          func(p *paramsHashParams) { p.targetBitrateKbps = 5000 },
		"subtitle burn-in": func(p *paramsHashParams) { p.subtitleBurnIn = true },
	} {
		other := params
		change(&other)
		if paramsHashWithToneMapRevision(other) == base {
			t.Fatalf("%s did not change the artifact hash", name)
		}
	}
}

// TestParamsHashIncludesFrozenToneMapRecipe verifies recipe changes produce distinct artifacts.
func TestParamsHashIncludesFrozenToneMapRecipe(t *testing.T) {
	hashWithRevision := func(policy tonemap.Policy, mode tonemap.Mode, sourceKind tonemap.SourceKind, recipeVersion string, preflightRequired bool, sourceRevision tonemap.SourceRevision) string {
		return paramsHashWithToneMapRevision(paramsHashParams{
			format: "transcode", container: "mp4", codecVideo: "h264", codecAudio: "aac", resolution: "1080p",
			audioTrackIndex: -1, targetBitrateKbps: 10000,
			policy: policy, mode: mode, sourceKind: sourceKind, recipeVersion: recipeVersion,
			preflightRequired: preflightRequired, sourceRevision: sourceRevision,
		})
	}
	base := hashWithRevision(tonemap.PolicyNone, "", "", "", false, tonemap.SourceRevision{})
	toneMapped := hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3, false, tonemap.SourceRevision{})
	for name, other := range map[string]string{
		"non-tone-mapped": base,
		"source kind":     hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourceHLG, playback.TransformationHDRToSDRToneMapRecipeVersionV3, false, tonemap.SourceRevision{}),
		"recipe version":  hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeHardware, tonemap.SourcePQ, "2", false, tonemap.SourceRevision{}),
		"execution mode":  hashWithRevision(tonemap.PolicyHardwareThenSoftware, tonemap.ModeSoftware, tonemap.SourcePQ, playback.TransformationHDRToSDRToneMapRecipeVersionV3, false, tonemap.SourceRevision{}),
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

func TestArtifactReadyStatusIncludesFencedRecipeRows(t *testing.T) {
	if !artifactReady(&Artifact{Status: ArtifactReady}) {
		t.Fatal("ordinary ready artifact was not ready")
	}
	if !artifactReady(&Artifact{Status: ArtifactToneMapReady}) {
		t.Fatal("fenced tone-map ready artifact was not ready")
	}
	if !artifactReady(&Artifact{Status: ArtifactAudioV2Ready}) {
		t.Fatal("fenced audio-v2 ready artifact was not ready")
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
	if !artifactExecutionFingerprintMatches(artifact, original) {
		t.Fatal("frozen execution request did not match its artifact hash")
	}
	changedPath := original
	changedPath.InputPath = "/media/replaced.mkv"
	if artifactExecutionFingerprintMatches(artifact, changedPath) {
		t.Fatal("changed input path reused the frozen artifact hash")
	}
	changedDuration := original
	changedDuration.TotalDuration++
	if artifactExecutionFingerprintMatches(artifact, changedDuration) {
		t.Fatal("changed duration reused the frozen artifact hash")
	}
}

func TestPreparedAudioBoostFreezesSelectedSourceChannelsInExecutionFingerprint(t *testing.T) {
	manager := &ArtifactManager{}
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Duration: 3600,
		AudioTracks: []models.AudioTrack{{Channels: 2}, {Channels: 6}},
	}
	artifact := &Artifact{
		ID: "artifact-audio", MediaFileID: file.ID, Format: "transcode",
		Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", AudioTrackIndex: 1,
	}
	artifact.ParamsHash = "legacy-parameter-hash"
	opts := manager.buildOpts(file, artifact)
	if opts.SourceAudioChannels != 6 {
		t.Fatalf("SourceAudioChannels = %d, want selected surround track", opts.SourceAudioChannels)
	}
	if artifactUsesExecutionFingerprint(artifact) {
		t.Fatal("legacy parameter hash was misclassified as a frozen execution fingerprint")
	}

	artifact.AudioRecipeVersion = playback.TransformationAudioToAACRecipeVersionV3
	artifact.ParamsHash = downloadprepare.NewRequest(artifact.ID, opts).ExecutionFingerprint()
	if !artifactUsesExecutionFingerprint(artifact) || !artifactExecutionFingerprintMatches(artifact, opts) {
		t.Fatal("source-sensitive audio recipe was not protected by its execution fingerprint")
	}
	changed := opts
	changed.SourceAudioChannels = 8
	if artifactExecutionFingerprintMatches(artifact, changed) {
		t.Fatal("changed selected source channels reused the frozen audio artifact")
	}
}

func TestPreparedTracksFreezeStreamLayoutInExecutionFingerprint(t *testing.T) {
	manager := &ArtifactManager{}
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Duration: 3600, CodecAudio: "ac3",
		AudioTracks:    []models.AudioTrack{{Codec: "ac3", Channels: 6}, {Codec: "aac", Channels: 2, Language: "ja"}},
		SubtitleTracks: []models.SubtitleTrack{{Codec: "subrip", Language: "en"}},
	}
	artifact := &Artifact{
		ID: "artifact-tracks", MediaFileID: file.ID, Format: "transcode",
		Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", AudioTrackIndex: -1,
		TrackRecipeVersion: playback.PreparedTracksRecipeVersion,
	}
	opts := manager.buildOpts(file, artifact)
	if opts.PreparedTracks == nil || len(opts.PreparedTracks.Audio) != 2 || len(opts.PreparedTracks.Subtitles) != 1 {
		t.Fatalf("PreparedTracks = %+v, want every audio track and the text subtitle", opts.PreparedTracks)
	}
	if opts.SourceAudioChannels != 0 {
		t.Fatalf("SourceAudioChannels = %d, want the per-track layout to own downmix facts", opts.SourceAudioChannels)
	}
	artifact.ParamsHash = downloadprepare.NewRequest(artifact.ID, opts).ExecutionFingerprint()
	if !artifactUsesExecutionFingerprint(artifact) || !artifactExecutionFingerprintMatches(artifact, opts) {
		t.Fatal("track recipe was not protected by its execution fingerprint")
	}
	// A rescan that changes the source track list must not reuse the frozen file.
	rescanned := *file
	rescanned.AudioTracks = append(rescanned.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2})
	if artifactExecutionFingerprintMatches(artifact, manager.buildOpts(&rescanned, artifact)) {
		t.Fatal("changed source tracks reused the frozen multi-track artifact")
	}

	legacy := *artifact
	legacy.TrackRecipeVersion = ""
	if opts := manager.buildOpts(file, &legacy); opts.PreparedTracks != nil {
		t.Fatalf("legacy artifact gained a multi-track layout: %+v", opts.PreparedTracks)
	}
}

func TestPreparedSourceAudioChannelsRequiresAACSurroundToDefaultStereo(t *testing.T) {
	file := &models.MediaFile{AudioTracks: []models.AudioTrack{{Channels: 2}, {Channels: 6}}}
	for _, test := range []struct {
		name       string
		trackIndex int
		codec      string
		want       int
	}{
		{name: "AAC surround", trackIndex: 1, codec: "aac", want: 6},
		{name: "AAC stereo", trackIndex: 0, codec: "aac"},
		{name: "EAC3 surround", trackIndex: 1, codec: "eac3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := preparedSourceAudioChannels(file, test.trackIndex, test.codec); got != test.want {
				t.Fatalf("preparedSourceAudioChannels() = %d, want %d", got, test.want)
			}
		})
	}
}

// resolveToneMapRefusalCase is one TestResolveToneMapTargetRefusals case.
type resolveToneMapRefusalCase struct {
	name     string
	file     *models.MediaFile
	preparer EncodePreparer
	settings SettingsReader
	canceled bool
	wantErr  func(error) bool
	// unfrozen asserts the returned target is the one passed in.
	unfrozen bool
}

var resolveToneMapTargetRefusalsCases = func() []resolveToneMapRefusalCase {
	policyOff := staticDownloadSettings{
		config.Allow4KTranscodeSettingKey:                 "false",
		config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
	}
	softwareOn := staticDownloadSettings{
		config.Allow4KTranscodeSettingKey:                 "true",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}
	hardwarePQ := tonemap.Capability{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}
	softwarePQ := softwarePQToneMap()[0]
	fourK := hdrDownloadTestFile()
	fourK.VideoTracks[0].Height = 2160
	return []resolveToneMapRefusalCase{
		{name: "settings unavailable", wantErr: func(err error) bool { return errors.Is(err, ErrQualityUnavailable) }},
		{name: "policy disabled", settings: policyOff, wantErr: func(err error) bool { return errors.Is(err, ErrQualityUnavailable) }},
		{name: "4K fails closed when settings are unavailable", file: fourK, wantErr: func(err error) bool {
			return errors.Is(err, ErrQualityUnavailable) && strings.Contains(err.Error(), "settings are unavailable")
		}},
		{name: "settings read failure is a retryable capability failure", settings: failingDownloadSettings{err: context.DeadlineExceeded}, wantErr: func(err error) bool {
			return errors.Is(err, context.DeadlineExceeded) && errors.Is(err, ErrCapabilityUnavailable) && !errors.Is(err, ErrQualityUnavailable)
		}},
		// The 4K restriction applies before dynamic-range handling.
		{name: "4K SDR", file: &models.MediaFile{VideoTracks: []models.VideoTrack{{Height: 2160, VideoRange: "SDR"}}}, settings: policyOff, wantErr: func(err error) bool {
			return errors.Is(err, ErrQualityUnavailable) && strings.Contains(err.Error(), "4K transcoding is disabled")
		}},
		{name: "4K unknown dynamic range", file: &models.MediaFile{VideoTracks: []models.VideoTrack{{Height: 2160}}}, settings: policyOff, wantErr: func(err error) bool {
			return errors.Is(err, ErrQualityUnavailable) && strings.Contains(err.Error(), "4K transcoding is disabled")
		}},
		{name: "cancellation with an empty executor inventory", settings: softwareOn, canceled: true, wantErr: func(err error) bool { return errors.Is(err, context.Canceled) }},
		{
			name:     "saturated executors are capacity, not quality",
			preparer: capacityAwareToneMapPreparer{capabilities: tonemap.Capabilities{hardwarePQ, softwarePQ}, available: map[tonemap.Mode]bool{}},
			settings: staticDownloadSettings{
				config.Allow4KTranscodeSettingKey:                 "true",
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
				config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
			},
			wantErr: func(err error) bool {
				return err != nil && errors.Is(err, ErrCapacityUnavailable) && !errors.Is(err, ErrQualityUnavailable)
			},
			unfrozen: true,
		},
		{
			name: "incompatible permitted mode remains quality unavailable",
			preparer: capacityAwareToneMapPreparer{
				capabilities: tonemap.Capabilities{softwarePQ},
				available:    map[tonemap.Mode]bool{tonemap.ModeSoftware: true},
			},
			settings: staticDownloadSettings{
				config.Allow4KTranscodeSettingKey:                 "true",
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
				config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
			},
			wantErr: func(err error) bool { return errors.Is(err, ErrQualityUnavailable) },
		},
		{
			name: "partial inventory error remains retryable when capacity is unknown",
			preparer: capacityAwareToneMapPreparer{
				capabilities: tonemap.Capabilities{hardwarePQ},
				available:    map[tonemap.Mode]bool{},
				capacityErr:  map[tonemap.Mode]error{tonemap.ModeHardware: context.DeadlineExceeded},
			},
			settings: staticDownloadSettings{
				config.Allow4KTranscodeSettingKey:                 "true",
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			},
			wantErr: func(err error) bool {
				return errors.Is(err, context.DeadlineExceeded) && errors.Is(err, ErrCapabilityUnavailable) && !errors.Is(err, ErrCapacityUnavailable) && !errors.Is(err, ErrQualityUnavailable)
			},
		},
		{
			name: "transient capability error before freezing the recipe", preparer: unavailableToneMapPreparer{}, settings: softwareOn,
			wantErr: func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }, unfrozen: true,
		},
	}
}()

// Every way resolveToneMapTarget refuses an HDR download: the error class
// decides whether the download fails for good, retries later, or waits for
// capacity, and a refused target is never frozen to a recipe.
func TestResolveToneMapTargetRefusals(t *testing.T) {
	for _, test := range resolveToneMapTargetRefusalsCases {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.canceled {
				cancel()
			}
			file := test.file
			if file == nil {
				file = hdrDownloadTestFile()
			}
			manager := &ArtifactManager{preparer: test.preparer, settings: test.settings}
			target := playback.PrepareTarget{CodecVideo: "h264", Resolution: "1080p"}
			got, err := manager.resolveToneMapTarget(ctx, file, target)
			if !test.wantErr(err) {
				t.Fatalf("resolveToneMapTarget() error = %v", err)
			}
			if test.unfrozen && got != target {
				t.Fatalf("target = %#v, want unfrozen %#v", got, target)
			}
		})
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
	if target.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("tone-map mode = %q, want software", target.ToneMapMode)
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
	p1 := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef", "art-1")
	p2 := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef", "art-1")
	if p1 != p2 {
		t.Fatalf("output path not deterministic: %q != %q", p1, p2)
	}
	// A later job for the same recipe, such as one created after a cancel,
	// never shares a path with the job it replaces.
	if other := artifactOutputPath("/var/artifacts", 42, "transcode", "abcdef0123456789deadbeef", "art-2"); other == p1 {
		t.Fatalf("two jobs share output path %q", p1)
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

	// An admin with no overrides gets the full ladder (#1403); an ungrouped
	// regular account with no overrides keeps the no-group deny.
	svc = newSvc(&models.User{Role: models.RoleAdmin}, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original,20mbps,10mbps,5mbps,2mbps,1mbps" {
		t.Fatalf("quality presets for an admin without overrides = %q, want full ladder", got)
	}
	svc = newSvc(&models.User{Role: models.RoleUser}, true)
	svc.SetArtifactManager(&ArtifactManager{})
	capInfo, _ = svc.Capability(context.Background(), 1)
	if got := strings.Join(capInfo.QualityPresets, ","); got != "original" {
		t.Fatalf("quality presets for an ungrouped user without overrides = %q, want original", got)
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

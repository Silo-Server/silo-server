package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// enumeratingNodePlannerV3 is a SessionPlanner stub that also exposes pooled
// transcode node URLs, matching *nodepool.Planner's production shape.
type enumeratingNodePlannerV3 struct {
	staticNodePlannerV3
	urls      []string
	proxyURLs []string
}

func (p enumeratingNodePlannerV3) TranscodeNodeURLs() []string { return p.urls }
func (p enumeratingNodePlannerV3) ProxyNodeURLs() []string     { return p.proxyURLs }

// presetLocalRegistryV3 pins the handler's local transformation registry so
// tests never probe the machine's real ffmpeg.
func presetLocalRegistryV3(h *PlaybackHandler, registry *playback.TransformationRegistryV3) {
	h.v3Registry = registry
}

func TestTransformationRegistryV3RetriesIncompleteProbe(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	failed := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: "partial", Available: true}})
	succeeded := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: "complete", Available: true}})
	var calls int
	handler.v3RegistryProbe = func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
		calls++
		if calls == 1 {
			return failed, context.DeadlineExceeded
		}
		return succeeded, nil
	}

	if got := handler.transformationRegistryV3(context.Background()); got != failed {
		t.Fatalf("failed probe registry = %p, want current partial result %p", got, failed)
	}
	if got := handler.transformationRegistryV3(context.Background()); got != succeeded {
		t.Fatalf("retry registry = %p, want successful result %p", got, succeeded)
	}
	if got := handler.transformationRegistryV3(context.Background()); got != succeeded || calls != 2 {
		t.Fatalf("cached registry = %p after %d probes, want successful registry after two probes", got, calls)
	}
}

// stableToneMapTransportFileV3 returns an HDR source whose filesystem and
// catalog revision facts agree, allowing transport tests to exercise the
// executor gate without weakening the production source-revision check.
func stableToneMapTransportFileV3(t *testing.T) *models.MediaFile {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	info, err := os.Stat(file.FilePath)
	if err != nil {
		t.Fatalf("stat tone-map fixture: %v", err)
	}
	modifiedAt := info.ModTime()
	probeUpdatedAt := time.Now().UTC()
	file.FileSize = info.Size()
	file.FileModifiedAt = &modifiedAt
	file.FileHash = "tone-map-fixture"
	file.ProbeUpdatedAt = &probeUpdatedAt
	file.CodecVideo = "hevc"
	file.Resolution = "2160p"
	file.Bitrate = 32_000
	file.HDR = true
	file.VideoTracks[0] = models.VideoTrack{
		Codec: "hevc", Profile: "main 10", Level: 51, Width: 3840, Height: 2160,
		FrameRate: "23.976", Bitrate: 32_000, BitDepth: 10, PixelFormat: "yuv420p10le",
		VideoRange: "DolbyVision", VideoRangeType: "DOVIWithHDR10", DVProfile: 7, DVBLCompatID: 6,
		DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}
	return file
}

func writePlaybackArgsRecordingFFmpegV3(t *testing.T, argsPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + argsPath + "\"\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write argument-recording fake ffmpeg: %v", err)
	}
	return path
}

// TestLocalToneMapCapabilitiesV3DoesNotSerializeCallers verifies that a
// canceled request is not trapped behind another request's slow probe.
func TestLocalToneMapCapabilitiesV3DoesNotSerializeCallers(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	handler.v3ToneMapProbe = func(ctx context.Context, _, _, _ string) (tonemap.Capabilities, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return nil, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	firstDone := make(chan struct{})
	go func() {
		_, _ = handler.localToneMapCapabilitiesV3(context.Background())
		close(firstDone)
	}()
	<-started

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	secondDone := make(chan struct{})
	go func() {
		_, _ = handler.localToneMapCapabilitiesV3(canceled)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("canceled capability caller waited behind the in-progress probe")
	}
	select {
	case <-firstDone:
		close(release)
		t.Fatal("the first probe unexpectedly completed before release")
	default:
	}
	close(release)
	<-firstDone
}

func TestRemoteTransformationsV3FailureCacheSplit(t *testing.T) {
	hits := 0
	fail := true
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"}}})
	}))
	defer remote.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	if _, err := handler.lookupRemoteTransformationsV3(context.Background(), nodeauth.TargetTranscode, remote.URL, true); err == nil {
		t.Fatal("fetch against a failing node must error")
	}
	if _, err := handler.lookupRemoteTransformationsV3(context.Background(), nodeauth.TargetTranscode, remote.URL, true); err == nil {
		t.Fatal("planning lookups must surface the memoized failure")
	}
	if hits != 1 {
		t.Fatalf("failing node was fetched %d times; planning must memoize the failure", hits)
	}

	// The transport path must fetch through the memoized failure: it may
	// have been produced by a planning deadline far shorter than this
	// path's budget, and rejecting the already-selected node on it would
	// fail a start a fresh fetch could still validate.
	fail = false
	transformations, err := handler.remoteTransformationsV3(context.Background(), nodeauth.TargetTranscode, remote.URL)
	if err != nil || len(transformations) != 1 {
		t.Fatalf("transport lookup must refetch through a memoized failure: %v %#v", err, transformations)
	}
	if hits != 2 {
		t.Fatalf("transport lookup fetched %d times, want 2", hits)
	}
	// The refetched success replaces the failure for planning too.
	if _, err := handler.lookupRemoteTransformationsV3(context.Background(), nodeauth.TargetTranscode, remote.URL, true); err != nil {
		t.Fatalf("planning lookup after transport success: %v", err)
	}
	if hits != 2 {
		t.Fatalf("cached success was refetched (%d hits)", hits)
	}
}

func TestRemoteTransformationsPlanningV3ServesStaleSuccessWhileRefreshing(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			close(requestStarted)
		}
		<-releaseRequest
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: "2"}}})
	}))
	defer remote.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	handler.v3NodeCapabilities = map[v3NodeCapabilityKey]v3NodeCapabilityCache{
		transcodeCapabilityKey(remote.URL): {
			transformations: []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"}},
			expiresAt:       time.Now().Add(-time.Second),
		},
	}
	result := make(chan []playback.TransformationV3, 1)
	go func() {
		transformations, _ := handler.lookupRemoteTransformationsV3(context.Background(), nodeauth.TargetTranscode, remote.URL, true)
		result <- transformations
	}()
	select {
	case transformations := <-result:
		if len(transformations) != 1 || transformations[0].Name != playback.TransformationAudioToAACV3 {
			t.Fatalf("stale planning transformations = %#v", transformations)
		}
	case <-time.After(time.Second):
		close(releaseRequest)
		t.Fatal("planning waited for the stale capability refresh")
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		close(releaseRequest)
		t.Fatal("background capability refresh did not start")
	}
	if _, err := handler.lookupRemoteTransformationsV3(context.Background(), nodeauth.TargetTranscode, remote.URL, true); err != nil {
		close(releaseRequest)
		t.Fatalf("second stale planning lookup: %v", err)
	}
	if got := hits.Load(); got != 1 {
		close(releaseRequest)
		t.Fatalf("concurrent background refresh requests = %d, want 1", got)
	}
	close(releaseRequest)
}

func TestLookupRemoteCapabilitiesV3PreservesConcurrentFreshSuccessOnRefetchFailure(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer remote.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	handler.v3NodeCapabilities = make(map[v3NodeCapabilityKey]v3NodeCapabilityCache)
	handler.v3NodeCapabilities[transcodeCapabilityKey(remote.URL)] = v3NodeCapabilityCache{
		expiresAt: time.Now().Add(-time.Second),
	}
	handler.v3NodeProbeBudgets = map[string]time.Duration{remote.URL: time.Second}
	type lookupResult struct {
		entry v3NodeCapabilityCache
		err   error
	}
	resultCh := make(chan lookupResult, 1)
	go func() {
		entry, err := handler.lookupRemoteCapabilitiesV3(context.Background(), nodeauth.TargetTranscode, remote.URL, false)
		resultCh <- lookupResult{entry: entry, err: err}
	}()
	<-requestStarted

	fresh := v3NodeCapabilityCache{
		transformations: []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}},
		expiresAt:       time.Now().Add(time.Minute),
	}
	handler.v3NodeCapabilitiesMu.Lock()
	handler.v3NodeCapabilities[transcodeCapabilityKey(remote.URL)] = fresh
	handler.v3NodeCapabilitiesMu.Unlock()
	close(releaseRequest)

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("lookup error = %v, want concurrent fresh success", result.err)
	}
	if len(result.entry.transformations) != 1 || result.entry.transformations[0].Name != playback.TransformationAudioToAACV3 {
		t.Fatalf("lookup entry = %#v, want concurrent fresh success", result.entry)
	}
}

func TestNVENCSDRBaseInitialAndThawedRecipeUseIdenticalFFmpegArgs(t *testing.T) {
	file := stableToneMapTransportFileV3(t)
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 2
	file.VideoTracks[0].ColorPrimaries = "bt709"
	file.VideoTracks[0].ColorTransfer = "bt709"
	file.VideoTracks[0].ColorSpace = "bt709"

	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{{
			Mode: tonemap.ModeHardware, Backend: tonemap.BackendNVENC, Filter: tonemap.HardwareFilterCUDA,
			SourceKinds: []tonemap.SourceKind{tonemap.SourceSDRBT709},
		}}, nil
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}))
	plan := &playback.PlanV3{
		PlanID: "plan:nvenc-sdr-base-thaw", Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
		},
	}
	initial := playback.PlannerResultV3{
		Plan: plan, PlayMethod: playback.PlayTranscode,
		TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "1080p", TargetBitrateKbps: 10_000,
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourceSDRBT709,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
	}
	liveTrack := file.VideoTracks[0]
	session, err := manager.StartSession(7, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start playback session: %v", err)
	}
	transcodeDir := t.TempDir()
	run := func(name string, result playback.PlannerResultV3) string {
		t.Helper()
		argsPath := filepath.Join(t.TempDir(), name+"-args.txt")
		ffmpegPath := writePlaybackArgsRecordingFFmpegV3(t, argsPath)
		writePlaybackToneMapFFprobe(t, ffmpegPath, liveTrack)
		handler.PlaybackConfig = func() config.PlaybackConfig {
			return config.PlaybackConfig{
				FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true,
				HWAccel: tonemap.BackendNVENC, HWDevice: "0",
			}
		}
		transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), session, file, result, mediaAuthModeV3{})
		if transportErr != nil {
			t.Fatalf("prepare %s NVENC transport: %v", name, transportErr)
		}
		args, readErr := os.ReadFile(argsPath)
		transport.rollback()
		if readErr != nil {
			t.Fatalf("read %s FFmpeg args: %v", name, readErr)
		}
		return string(args)
	}

	initialArgs := run("initial", initial)
	recipe, err := handler.freezeExecutableRecipeV3(context.Background(), file, initial)
	if err != nil {
		t.Fatalf("freeze initial recipe: %v", err)
	}
	file.VideoTracks[0].Profile = "main"
	file.VideoTracks[0].BitDepth = 8
	thawedArgs := run("thawed", recipe.PlannerResult(plan))

	normalizeOutputGeneration := func(args string) string {
		lines := strings.Split(args, "\n")
		for index, line := range lines {
			if strings.HasPrefix(line, transcodeDir+string(os.PathSeparator)) {
				lines[index] = filepath.Join(transcodeDir, "<generation>", filepath.Base(line))
			}
		}
		return strings.Join(lines, "\n")
	}
	if normalizeOutputGeneration(initialArgs) != normalizeOutputGeneration(thawedArgs) {
		t.Fatalf("thawed NVENC FFmpeg args changed from initial execution:\ninitial:\n%s\nthawed:\n%s", initialArgs, thawedArgs)
	}
	if !strings.Contains(initialArgs, "hwdownload,format=p010le") {
		t.Fatalf("10-bit NVENC SDR-base args did not preserve p010le: %s", initialArgs)
	}
}

func TestProgressivePlanningCapabilitiesV3RequireRelayAndAuthorityForTranscodeExecutor(t *testing.T) {
	transcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{
			TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxExecutionV1},
			Transformations: []playback.TransformationV3{{
				Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
				RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
			}},
		})
	}))
	t.Cleanup(transcode.Close)

	local := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}})
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{transcode.URL}}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	registry := handler.progressiveRemuxPlanningRegistryWithInputsV3(t.Context(), local, true)
	if registry.Available(playback.TransformationAudioToAACV3) {
		t.Fatal("transcode capability without a relay-capable proxy widened progressive planning")
	}

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{
			TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxRelayV1},
		})
	}))
	t.Cleanup(relay.Close)
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{transcode.URL}, proxyURLs: []string{relay.URL}}
	registry = handler.progressiveRemuxPlanningRegistryWithInputsV3(t.Context(), local, true)
	if registry.Available(playback.TransformationAudioToAACV3) {
		t.Fatal("transcode capability without a durable authority store widened progressive planning")
	}

	handler.NodeRecipeStore = &recordingRecipeCardStoreV3{}
	registry = handler.progressiveRemuxPlanningRegistryWithInputsV3(t.Context(), local, true)
	if !registry.Available(playback.TransformationAudioToAACV3) {
		t.Fatal("eligible transcode capability with relay and authority store was omitted")
	}
}

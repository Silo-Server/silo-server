package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// enumeratingNodePlannerV3 is a SessionPlanner stub that also exposes pooled
// transcode node URLs, matching *nodepool.Planner's production shape.
type enumeratingNodePlannerV3 struct {
	staticNodePlannerV3
	urls []string
}

func (p enumeratingNodePlannerV3) TranscodeNodeURLs() []string { return p.urls }

// presetLocalRegistryV3 pins the handler's local transformation registry so
// tests never probe the machine's real ffmpeg.
func presetLocalRegistryV3(h *PlaybackHandler, registry *playback.TransformationRegistryV3) {
	h.v3Registry = registry
}

func TestTransformationRegistryV3RetriesIncompleteProbe(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
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

func TestHLSPlanningRegistryV3UnionsPooledNodeCapabilities(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		}})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "2"},
		{Name: "audio_to_aac", RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		{Name: "server_dv7_to_hdr10", RecipeVersion: "1"},
	}))
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{remote.URL}}

	registry := handler.hlsPlanningRegistryV3(context.Background())
	if !registry.Available("video_to_h264") || !registry.Available("audio_to_aac") {
		t.Fatal("pooled node capabilities must widen the HLS planning registry")
	}
	if registry.Available("server_dv7_to_hdr10") {
		t.Fatal("transformations no node advertises must stay unavailable")
	}
	if handler.transformationRegistryV3(context.Background()).Available("video_to_h264") {
		t.Fatal("the local registry must not be widened by node capabilities")
	}
}

func TestHLSPlanningRegistryV3WithoutEnumeratorIsLocal(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	local := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "2", Available: true}})
	presetLocalRegistryV3(handler, local)
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{}}

	if registry := handler.hlsPlanningRegistryV3(context.Background()); registry != local {
		t.Fatal("a planner without node enumeration must plan from the local registry")
	}
}

// TestHLSPlanningRegistryV3EnablesValidatedLocalToneMapWithoutRestart verifies live policy changes affect planning.
func TestHLSPlanningRegistryV3EnablesValidatedLocalToneMapWithoutRestart(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	local := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
	}})
	presetLocalRegistryV3(handler, local)
	capabilities := tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return capabilities, nil
	}
	settings := &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.SettingsRepo = settings

	if handler.hlsPlanningRegistryV3(context.Background()).Available(playback.TransformationHDRToSDRToneMapV3) {
		t.Fatal("disabled tone-map policy widened the local transformation registry")
	}
	settings.values["playback.transcode_software_tone_map_enabled"] = "true"
	if !handler.hlsPlanningRegistryV3(context.Background()).Available(playback.TransformationHDRToSDRToneMapV3) {
		t.Fatal("enabled validated tone-map executor was not available without restart")
	}
}

// TestLocalToneMapCapabilitiesV3UsesLivePlaybackHardware verifies that the
// handler always delegates cache decisions to the shared capability probe.
func TestLocalToneMapCapabilitiesV3UsesLivePlaybackHardware(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	cfg := config.PlaybackConfig{FFmpegPath: "/opt/ffmpeg-a", HWAccel: "qsv", HWDevice: "/dev/dri/renderD128"}
	handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	var calls []string
	handler.v3ToneMapProbe = func(_ context.Context, ffmpegPath, backend, device string) (tonemap.Capabilities, error) {
		calls = append(calls, ffmpegPath+"|"+backend+"|"+device)
		filter := tonemap.HardwareFilterVAAPI
		if backend == tonemap.BackendQSV {
			filter = tonemap.HardwareFilterOpenCL
		}
		return tonemap.Capabilities{{Mode: tonemap.ModeHardware, Backend: backend, Filter: filter, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}}, nil
	}

	for i := 0; i < 2; i++ {
		if got, err := handler.localToneMapCapabilitiesV3(context.Background()); err != nil || len(got) != 1 || got[0].Backend != "qsv" {
			t.Fatalf("initial capabilities = %#v", got)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("live probe calls = %d, want 2", len(calls))
	}

	cfg.FFmpegPath = "/opt/ffmpeg-b"
	cfg.HWAccel = "vaapi"
	cfg.HWDevice = "/dev/dri/renderD129"
	if got, err := handler.localToneMapCapabilitiesV3(context.Background()); err != nil || len(got) != 1 || got[0].Backend != "vaapi" {
		t.Fatalf("updated capabilities = %#v", got)
	}
	if len(calls) != 3 || calls[1] == calls[2] {
		t.Fatalf("probe inputs = %v, want the live config change on the next call", calls)
	}
}

// TestLocalToneMapCapabilitiesV3DoesNotSerializeCallers verifies that a
// canceled request is not trapped behind another request's slow probe.
func TestLocalToneMapCapabilitiesV3DoesNotSerializeCallers(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
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

// TestHLSToneMapCapabilitiesV3FetchesNodesConcurrently verifies pooled node lookups overlap.
func TestHLSToneMapCapabilitiesV3FetchesNodesConcurrently(t *testing.T) {
	var active atomic.Int32
	var startedOnce sync.Once
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	serve := func(w http.ResponseWriter, _ *http.Request) {
		if active.Add(1) == 2 {
			startedOnce.Do(func() { close(bothStarted) })
		}
		<-release
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}})
	}
	first := httptest.NewServer(http.HandlerFunc(serve))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(serve))
	defer second.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{first.URL, second.URL}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.PlaybackLocalTranscodeFallbackSettingKey: "false"}}
	result := make(chan tonemap.Capabilities, 1)
	go func() { result <- handler.hlsToneMapCapabilitiesV3(context.Background()) }()
	select {
	case <-bothStarted:
		close(release)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("node capability probes did not overlap")
	}
	if got := <-result; len(got) != 2 {
		t.Fatalf("aggregated capabilities = %#v, want both nodes", got)
	}
}

func TestHLSToneMapCapabilityInventoryV3StartsLocalAndRemoteConcurrently(t *testing.T) {
	localStarted := make(chan struct{})
	remoteStarted := make(chan struct{})
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(remoteStarted)
		<-release
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{remote.URL}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		close(localStarted)
		<-release
		return nil, nil
	}
	type inventoryResult struct {
		inventory hlsToneMapCapabilityInventoryV3
		err       error
	}
	result := make(chan inventoryResult, 1)
	go func() {
		inventory, err := handler.hlsToneMapCapabilityInventoryV3(context.Background())
		result <- inventoryResult{inventory: inventory, err: err}
	}()
	select {
	case <-localStarted:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("local capability probe did not start")
	}
	select {
	case <-remoteStarted:
		close(release)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("remote capability fetch waited for the local probe")
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("capability inventory error = %v", got.err)
	}
	if !got.inventory.union.Supports(tonemap.ModeSoftware, tonemap.SourcePQ) {
		t.Fatalf("capability inventory = %#v, want remote executor retained", got.inventory)
	}
}

func TestIncompleteToneMapPlanningBecomesRetryableStartFailure(t *testing.T) {
	for _, reason := range []string{
		playback.TerminalHDRTranscodeUnsupportedV3,
		terminalSubtitleConversionUnsupportedV3,
	} {
		result := playback.PlannerResultV3{Terminal: &playback.TerminalV3{Reason: reason}}

		result = retryIncompleteToneMapPlanningV3(result, context.DeadlineExceeded)

		if result.Terminal == nil || result.Terminal.Reason != transcodeStartFailedReasonV3 || !result.Terminal.Retryable {
			t.Fatalf("terminal for %q = %#v, want retryable %q", reason, result.Terminal, transcodeStartFailedReasonV3)
		}
	}
}

func TestIncompletePlaybackSettingsMakePolicyTerminalsRetryable(t *testing.T) {
	tests := []playback.TerminalV3{
		{Reason: playback.TerminalHDRTranscodeUnsupportedV3, Message: "HDR unavailable"},
		{Reason: terminalSubtitleConversionUnsupportedV3, Message: "The selected subtitle must be burned into the video, but this HDR source cannot be re-encoded."},
		{Reason: terminalSubtitleConversionUnsupportedV3, Message: "The selected subtitle must be burned into the video, but 4K transcoding is disabled."},
		{Reason: terminalNoAlternateVersionV3, Message: playback.TerminalMessage4KTranscodeDisabledV3},
	}
	for _, terminal := range tests {
		result := retryIncompletePlaybackSettingsV3(playback.PlannerResultV3{Terminal: &terminal}, context.DeadlineExceeded)
		if result.Terminal == nil || result.Terminal.Reason != transcodeStartFailedReasonV3 || !result.Terminal.Retryable {
			t.Fatalf("settings terminal %q = %#v, want retryable %q", terminal.Reason, result.Terminal, transcodeStartFailedReasonV3)
		}
	}

	direct := playback.PlannerResultV3{Plan: &playback.PlanV3{Delivery: playback.DeliveryOriginalHTTPV3}}
	if got := retryIncompletePlaybackSettingsV3(direct, context.DeadlineExceeded); got.Plan == nil || got.Terminal != nil {
		t.Fatalf("direct HDR-capable route was blocked by unrelated settings failure: %#v", got)
	}
}

func TestUnusedToneMapPlanningSnapshotDoesNotInventCapabilityFailure(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	var probes atomic.Int32
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		probes.Add(1)
		return nil, context.DeadlineExceeded
	}
	snapshot := &hlsPlanningSnapshotV3{
		handler: handler,
		ctx:     context.Background(),
		settings: playback.PlannerSettingsV3{
			SoftwareToneMapEnabled: true,
		},
	}

	if err := snapshot.capabilityError(); err != nil {
		t.Fatalf("unused snapshot error = %v", err)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("unused snapshot probes = %d, want none", got)
	}
}

func TestHandlePlaybackCapabilityV3OmitsToneMapWhenProbeIsIncomplete(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return nil, context.DeadlineExceeded
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/playback/capability", nil).WithContext(newAuthorizedPlaybackContext())
	handler.HandlePlaybackCapabilityV3(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response playback.CapabilityResponseV3
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, transformation := range response.Transformations {
		if transformation.Name == playback.TransformationHDRToSDRToneMapV3 {
			t.Fatal("tone-map transformation was advertised after capability discovery failed")
		}
	}
}

func TestHandlePlaybackCapabilityV3UsesRemoteExecutorWhenLocalProbeIsIncomplete(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{
			Transformations: []playback.TransformationV3{{
				Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3,
				RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
			}},
			ToneMapCapabilities: tonemap.Capabilities{{
				Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
				SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
			}},
		})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{remote.URL}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return nil, context.DeadlineExceeded
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
	}}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/playback/capability", nil).WithContext(newAuthorizedPlaybackContext())
	handler.HandlePlaybackCapabilityV3(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response playback.CapabilityResponseV3
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, transformation := range response.Transformations {
		if transformation.Name == playback.TransformationHDRToSDRToneMapV3 {
			return
		}
	}
	t.Fatal("remote tone-map transformation was not advertised")
}

// TestHLSToneMapCapabilitiesV3HonorsSharedDeadline verifies slow nodes cannot exceed the planning deadline.
func TestHLSToneMapCapabilitiesV3HonorsSharedDeadline(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}})
	}))
	defer fast.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{slow.URL, slow.URL, fast.URL}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.PlaybackLocalTranscodeFallbackSettingKey: "false"}}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	got := handler.hlsToneMapCapabilitiesV3(ctx)
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("capability aggregation took %s, want shared caller deadline", elapsed)
	}
	if len(got) != 1 || !got.Supports(tonemap.ModeSoftware, tonemap.SourcePQ) {
		t.Fatalf("aggregated capabilities = %#v, want the successful node retained", got)
	}
}

func TestHLSToneMapCapabilityInventoryV3RedactsNodeURLSecrets(t *testing.T) {
	const (
		username       = "probe-operator"
		password       = "node-password"
		querySecret    = "query-secret"
		fragmentSecret = "fragment-secret"
	)
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	remote.Close()
	nodeURL := strings.Replace(remote.URL, "http://", "http://"+username+":"+password+"@", 1) +
		"?access_token=" + querySecret + "#" + fragmentSecret

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "jwt-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{nodeURL}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.PlaybackLocalTranscodeFallbackSettingKey: "false"}}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	_, err := handler.hlsToneMapCapabilityInventoryV3(context.Background())
	if err == nil {
		t.Fatal("capability inventory returned no error")
	}
	diagnostics := logs.String() + "\n" + err.Error()
	for _, secret := range []string{username, password, querySecret, fragmentSecret} {
		if strings.Contains(diagnostics, secret) {
			t.Fatalf("capability diagnostics contain %q: %q", secret, diagnostics)
		}
	}
	if !strings.Contains(diagnostics, remote.URL) {
		t.Fatalf("capability diagnostics lost sanitized node origin: %q", diagnostics)
	}
}

func TestHandlePlaybackCapabilityV3ReusesResolvedToneMapInputs(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
	}}))
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}, nil
	}
	settings := &mutablePlaybackSettingsV3{
		values: map[string]string{
			config.Allow4KTranscodeSettingKey:                 "true",
			config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
		},
		getCalls: make(map[string]int),
	}
	handler.SettingsRepo = settings

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/playback/capability", nil).WithContext(newAuthorizedPlaybackContext())
	handler.HandlePlaybackCapabilityV3(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, key := range []string{
		config.Allow4KTranscodeSettingKey,
		config.PlaybackTranscodeHardwareToneMapSettingKey,
		config.PlaybackTranscodeSoftwareToneMapSettingKey,
	} {
		if got := settings.getCalls[key]; got != 1 {
			t.Fatalf("settings lookup %q = %d, want exactly one", key, got)
		}
	}
}

func TestHandlePlaybackCapabilityV3AdvertisesPooledTransformationsWhenToneMapDisabled(t *testing.T) {
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits.Add(1)
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
		}})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{remote.URL}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.PlaybackLocalTranscodeFallbackSettingKey: "false"}}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/playback/capability", nil).WithContext(newAuthorizedPlaybackContext())
	handler.HandlePlaybackCapabilityV3(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response playback.CapabilityResponseV3
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	advertised := make(map[string]bool, len(response.Transformations))
	for _, transformation := range response.Transformations {
		advertised[transformation.Name] = true
	}
	if !advertised[playback.TransformationAudioToAACV3] {
		t.Fatal("pooled-only audio transformation was omitted while tone mapping was disabled")
	}
	if advertised[playback.TransformationHDRToSDRToneMapV3] {
		t.Fatal("tone-map transformation was advertised while policy was disabled")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("node capability requests = %d, want one transformation inventory request without a tone-map probe", got)
	}
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

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	if _, err := handler.remoteTransformationsPlanningV3(context.Background(), remote.URL); err == nil {
		t.Fatal("fetch against a failing node must error")
	}
	if _, err := handler.remoteTransformationsPlanningV3(context.Background(), remote.URL); err == nil {
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
	transformations, err := handler.remoteTransformationsV3(context.Background(), remote.URL)
	if err != nil || len(transformations) != 1 {
		t.Fatalf("transport lookup must refetch through a memoized failure: %v %#v", err, transformations)
	}
	if hits != 2 {
		t.Fatalf("transport lookup fetched %d times, want 2", hits)
	}
	// The refetched success replaces the failure for planning too.
	if _, err := handler.remoteTransformationsPlanningV3(context.Background(), remote.URL); err != nil {
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

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.v3NodeCapabilities = map[string]v3NodeCapabilityCache{
		remote.URL: {
			transformations: []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"}},
			expiresAt:       time.Now().Add(-time.Second),
		},
	}
	result := make(chan []playback.TransformationV3, 1)
	go func() {
		transformations, _ := handler.remoteTransformationsPlanningV3(context.Background(), remote.URL)
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
	if _, err := handler.remoteTransformationsPlanningV3(context.Background(), remote.URL); err != nil {
		close(releaseRequest)
		t.Fatalf("second stale planning lookup: %v", err)
	}
	if got := hits.Load(); got != 1 {
		close(releaseRequest)
		t.Fatalf("concurrent background refresh requests = %d, want 1", got)
	}
	close(releaseRequest)
}

func TestWarmPlaybackCapabilitiesV3PopulatesNodeCache(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"}}})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{remote.URL}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{HWAccel: playback.HWAccelNone} }
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.warmPlaybackCapabilitiesV3(context.Background())

	handler.v3NodeCapabilitiesMu.Lock()
	entry, ok := handler.v3NodeCapabilities[remote.URL]
	handler.v3NodeCapabilitiesMu.Unlock()
	if !ok || entry.err != nil || len(entry.transformations) != 1 || entry.transformations[0].Name != playback.TransformationAudioToAACV3 {
		t.Fatalf("warmed node capability = %#v, found=%t", entry, ok)
	}
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

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
	handler.v3NodeCapabilities[remote.URL] = v3NodeCapabilityCache{
		expiresAt: time.Now().Add(-time.Second),
	}
	handler.v3NodeProbeBudgets = map[string]time.Duration{remote.URL: time.Second}
	type lookupResult struct {
		entry v3NodeCapabilityCache
		err   error
	}
	resultCh := make(chan lookupResult, 1)
	go func() {
		entry, err := handler.lookupRemoteCapabilitiesV3(context.Background(), remote.URL, false)
		resultCh <- lookupResult{entry: entry, err: err}
	}()
	<-requestStarted

	fresh := v3NodeCapabilityCache{
		transformations: []playback.TransformationV3{{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}},
		expiresAt:       time.Now().Add(time.Minute),
	}
	handler.v3NodeCapabilitiesMu.Lock()
	handler.v3NodeCapabilities[remote.URL] = fresh
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

// In a heterogeneous pool, a plan that needs server transformations must be
// placed on a node advertising them even when load balancing prefers an
// incapable node, while transformation-free plans keep load-based selection.
func TestPlanNodeSessionV3PrefersCapabilityMatchingNode(t *testing.T) {
	capable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
		}})
	}))
	defer capable.Close()
	incapable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
	}))
	defer incapable.Close()

	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, Name: "incapable", Type: nodepool.NodeTypeTranscode, URL: incapable.URL, Enabled: true, Healthy: true, ActiveJobs: 0},
		{ID: 2, Name: "capable", Type: nodepool.NodeTypeTranscode, URL: capable.URL, Enabled: true, Healthy: true, ActiveJobs: 5},
	})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)

	plan := &playback.PlanV3{
		PlanID:   "plan:heterogeneous",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
		},
	}
	selected := handler.planNodeSessionV3(context.Background(), &playback.Session{ID: "session-hetero"}, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode}, false)
	if selected.TranscodeNode == nil || selected.TranscodeNode.URL != capable.URL {
		t.Fatalf("capability-requiring plan selected %+v, want the capable node", selected.TranscodeNode)
	}

	free := &playback.PlanV3{PlanID: "plan:copy", Delivery: playback.DeliveryRemuxHLSV3, Transformations: []playback.TransformationV3{}}
	loadBased := handler.planNodeSessionV3(context.Background(), &playback.Session{ID: "session-copy"}, playback.PlannerResultV3{Plan: free, PlayMethod: playback.PlayRemux}, false)
	if loadBased.TranscodeNode == nil || loadBased.TranscodeNode.URL != incapable.URL {
		t.Fatalf("transformation-free plan selected %+v, want load-based selection", loadBased.TranscodeNode)
	}
}

func TestPrepareTransportV3LocalFallbackRejectsUnavailableTransformations(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "2"},
		{Name: "audio_to_aac", RecipeVersion: "2"},
	}))
	plan := &playback.PlanV3{
		PlanID:   "plan:local-capability",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	_, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-local-capability"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr == nil || transportErr.reason != "transcode_node_capability_unavailable" || !transportErr.retryable {
		t.Fatalf("transport error = %#v", transportErr)
	}
}

// TestPrepareTransportV3AcceptsEveryValidatedLocalToneMapExecutor verifies each advertised executor starts locally.
func TestPrepareTransportV3AcceptsEveryValidatedLocalToneMapExecutor(t *testing.T) {
	tests := []struct {
		name           string
		mode           tonemap.Mode
		backend        string
		filter         string
		policy         tonemap.Policy
		settingKey     string
		configuredHW   string
		hardwareDevice string
	}{
		{name: "QSV", mode: tonemap.ModeHardware, backend: tonemap.BackendQSV, filter: tonemap.HardwareFilterOpenCL, policy: tonemap.PolicyHardwareOnly, settingKey: config.PlaybackTranscodeHardwareToneMapSettingKey, configuredHW: tonemap.BackendQSV, hardwareDevice: "/dev/dri/renderD128"},
		{name: "VAAPI", mode: tonemap.ModeHardware, backend: tonemap.BackendVAAPI, filter: tonemap.HardwareFilterVAAPI, policy: tonemap.PolicyHardwareOnly, settingKey: config.PlaybackTranscodeHardwareToneMapSettingKey, configuredHW: tonemap.BackendVAAPI, hardwareDevice: "/dev/dri/renderD128"},
		{name: "NVENC", mode: tonemap.ModeHardware, backend: tonemap.BackendNVENC, filter: tonemap.HardwareFilterCUDA, policy: tonemap.PolicyHardwareOnly, settingKey: config.PlaybackTranscodeHardwareToneMapSettingKey, configuredHW: tonemap.BackendNVENC, hardwareDevice: "0"},
		{name: "software", mode: tonemap.ModeSoftware, backend: tonemap.BackendSoftware, filter: tonemap.SoftwareFilterBT2390, policy: tonemap.PolicySoftwareOnly, settingKey: config.PlaybackTranscodeSoftwareToneMapSettingKey, configuredHW: playback.HWAccelNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := stableToneMapTransportFileV3(t)
			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager)
			ffmpegPath := writePlaybackTestFFmpeg(t)
			writePlaybackToneMapFFprobe(t, ffmpegPath, file.VideoTracks[0])
			transcodeDir := t.TempDir()
			handler.PlaybackConfig = func() config.PlaybackConfig {
				return config.PlaybackConfig{
					FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true,
					HWAccel: test.configuredHW, HWDevice: test.hardwareDevice,
				}
			}
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{test.settingKey: "true"}}
			handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
				return tonemap.Capabilities{{
					Mode: test.mode, Backend: test.backend, Filter: test.filter,
					SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
				}}, nil
			}
			presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
				{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
				{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
				{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
			}))
			plan := &playback.PlanV3{
				PlanID: "plan:local-tone-map:" + test.backend, Delivery: playback.DeliveryTranscodeHLSV3,
				Transformations: []playback.TransformationV3{
					{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
					{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
					{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
				},
			}
			result := playback.PlannerResultV3{
				Plan: plan, PlayMethod: playback.PlayTranscode,
				TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "2160p", TargetBitrateKbps: 32_000,
				SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
				ToneMapPolicy: test.policy, ToneMapMode: test.mode, ToneMapSourceKind: tonemap.SourcePQ,
				ToneMapRecipeVersion:  playback.TransformationHDRToSDRToneMapRecipeVersionV3,
				ToneMapSourceRevision: tonemap.RevisionForFile(file),
			}
			session, err := manager.StartSession(7, "profile-1", file.ID, playback.PlayTranscode, true)
			if err != nil {
				t.Fatalf("start playback session: %v", err)
			}
			transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), session, file, result, mediaAuthModeV3{})
			if transportErr != nil {
				t.Fatalf("prepare %s tone-map transport: %v", test.name, transportErr)
			}
			if transport.hwAccel != test.configuredHW || transport.toneMapMode != test.mode {
				t.Fatalf("prepared execution facts = hw %q tone_map %q, want %q %q", transport.hwAccel, transport.toneMapMode, test.configuredHW, test.mode)
			}
			if err := handler.updateV3SessionState(context.Background(), session, file, result, transport, mediaAuthModeV3{}); err != nil {
				t.Fatalf("update session state: %v", err)
			}
			transport.commit()
			t.Cleanup(func() { handler.tm.CloseTranscodeSession(session.ID, "") })
			live := handler.tm.GetTranscodeSession(session.ID)
			if live == nil {
				t.Fatal("validated local tone-map transport was not registered")
			}
			opts := live.Opts()
			if opts.ToneMapMode != test.mode || opts.ToneMapFilter != test.filter || opts.HWAccel != test.configuredHW {
				t.Fatalf("executor opts = mode %q filter %q hw %q, want %q %q %q", opts.ToneMapMode, opts.ToneMapFilter, opts.HWAccel, test.mode, test.filter, test.configuredHW)
			}
			updated, err := manager.GetSession(session.ID)
			if err != nil {
				t.Fatalf("load updated session: %v", err)
			}
			if updated.TranscodeHWAccel != test.configuredHW || updated.ToneMapMode != test.mode {
				t.Fatalf("session execution facts = hw %q tone_map %q, want %q %q", updated.TranscodeHWAccel, updated.ToneMapMode, test.configuredHW, test.mode)
			}
			reused := reusedHLSTransportV3(updated, transport.url)
			if reused.hwAccel != test.configuredHW || reused.toneMapMode != test.mode {
				t.Fatalf("reused execution facts = hw %q tone_map %q, want %q %q", reused.hwAccel, reused.toneMapMode, test.configuredHW, test.mode)
			}
		})
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
	handler := NewPlaybackHandler(manager)
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

func TestPrepareTransportV3PrefersLocalHardwareBeforeSoftwareFallback(t *testing.T) {
	ffmpegPath := writePlaybackTestFFmpeg(t)
	file := stableToneMapTransportFileV3(t)
	writePlaybackToneMapFFprobe(t, ffmpegPath, file.VideoTracks[0])
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	handler.NodePlanner = staticNodePlannerV3{}
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{
			FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true,
			HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128",
		}
	}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackLocalTranscodeFallbackSettingKey:   "true",
		config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{
			{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
			{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
		}, nil
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}))
	result := playback.PlannerResultV3{
		Plan: &playback.PlanV3{PlanID: "plan:local-hardware-first", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
		}},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "1080p", TargetBitrateKbps: 6_000,
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
	}
	session, err := manager.StartSession(7, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start playback session: %v", err)
	}
	transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), session, file, result, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare local hardware transport: %v", transportErr)
	}
	defer transport.rollback()
	if transport.hwAccel != tonemap.BackendQSV || transport.toneMapMode != tonemap.ModeHardware {
		t.Fatalf("execution facts = hw %q tone_map %q, want qsv and hardware", transport.hwAccel, transport.toneMapMode)
	}
}

// TestPrepareTransportV3ReportsSoftwareToneMapFallback verifies failed hardware output is discarded before retry.
func TestPrepareTransportV3ReportsSoftwareToneMapFallback(t *testing.T) {
	baseFFmpeg := writePlaybackTestFFmpeg(t)
	ffmpegPath := filepath.Join(t.TempDir(), "hardware-failing-ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) out=\"$(dirname \"$arg\")\";; esac; done\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in *tonemap_opencl*) mkdir -p \"$out\"; printf partial > \"$out/hardware-partial.marker\"; echo 'hardware tone map failed' >&2; exit 1;; esac\n" +
		"done\n" +
		"exec \"" + baseFFmpeg + "\" \"$@\"\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write hardware-failing ffmpeg: %v", err)
	}

	file := stableToneMapTransportFileV3(t)
	writePlaybackToneMapFFprobe(t, ffmpegPath, file.VideoTracks[0])
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true, HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128"}
	}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{
			{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
			{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
		}, nil
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}))
	result := playback.PlannerResultV3{
		Plan: &playback.PlanV3{PlanID: "plan:local-tone-map-fallback", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
		}},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "2160p", TargetBitrateKbps: 32_000,
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
	}
	session, err := manager.StartSession(7, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start playback session: %v", err)
	}
	transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), session, file, result, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare fallback transport: %v", transportErr)
	}
	defer transport.rollback()
	if transport.hwAccel != playback.HWAccelNone || transport.toneMapMode != tonemap.ModeSoftware {
		t.Fatalf("fallback execution facts = hw %q tone_map %q, want none and software", transport.hwAccel, transport.toneMapMode)
	}
	expectedGeneration := transportGenerationV3(session.ID, result.Plan.PlanID)
	generationPrefix := expectedGeneration[:strings.LastIndexByte(expectedGeneration, '-')+1]
	generationDirs, err := filepath.Glob(filepath.Join(transcodeDir, generationPrefix+"*"))
	if err != nil {
		t.Fatalf("find local transport generation: %v", err)
	}
	if len(generationDirs) != 1 {
		t.Fatalf("local transport generations = %v, want exactly one", generationDirs)
	}
	markerPath := filepath.Join(generationDirs[0], "hardware-partial.marker")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("failed hardware output survived software fallback: %v", err)
	}
}

func TestPrepareSoftwareToneMapFallbackV3ValidatesLocalRegistry(t *testing.T) {
	file := stableToneMapTransportFileV3(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodePlanner = staticNodePlannerV3{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.v3ToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}, nil
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}))
	result := playback.PlannerResultV3{
		Plan: &playback.PlanV3{PlanID: "plan:invalid-local-fallback", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
		}},
		PlayMethod: playback.PlayTranscode, ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
		ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
	}

	_, attempted, transportErr := handler.prepareSoftwareToneMapFallbackV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-invalid-local-fallback", UserID: 7, ProfileID: "profile-1"},
		file, result, preparedTimelineV3{}, mediaAuthModeV3{},
	)
	if !attempted {
		t.Fatal("software fallback was not attempted")
	}
	if transportErr == nil || transportErr.reason != "transcode_node_capability_unavailable" || !transportErr.retryable {
		t.Fatalf("transport error = %#v, want retryable local registry rejection", transportErr)
	}
}

func TestPrepareTransportV3FallsBackToSoftwareCapacity(t *testing.T) {
	requiredTransformations := []playback.TransformationV3{
		{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
		{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}
	newToneMapNode := func(capability tonemap.Capability) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
				writeJSON(w, http.StatusOK, playback.HWAccelInfo{
					Transformations:     requiredTransformations,
					ToneMapCapabilities: tonemap.Capabilities{capability},
				})
			case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
				var request transcodenode.TranscodeStartRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode remote start: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
					SessionID: request.SessionID, Status: "started", HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode,
				})
			case r.Method == http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	}
	hardware := newToneMapNode(tonemap.Capability{
		Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	})
	defer hardware.Close()
	software := newToneMapNode(tonemap.Capability{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	})
	defer software.Close()

	limit := 1
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{URL: hardware.URL, Enabled: true, Healthy: true, ActiveJobs: 1, MaxJobs: &limit},
		{URL: software.URL, Enabled: true, Healthy: true},
	})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		config.PlaybackLocalTranscodeFallbackSettingKey: "false",
	}}
	file := stableToneMapTransportFileV3(t)
	result := playback.PlannerResultV3{
		Plan: &playback.PlanV3{
			PlanID: "plan:tone-map-capacity", Delivery: playback.DeliveryTranscodeHLSV3,
			Transformations: requiredTransformations,
		},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetBitrateKbps: file.Bitrate,
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
	}
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-tone-map-capacity", UserID: 7, ProfileID: "profile-1"},
		file,
		result,
		mediaAuthModeV3{},
	)
	if transportErr != nil || transport.nodeURL != software.URL || transport.toneMapMode != tonemap.ModeSoftware {
		t.Fatalf("transport = %+v error = %v, want software node fallback", transport, transportErr)
	}
	transport.rollback()
}

func TestPrepareTransportV3TriesNextSoftwareNodeAfterStartFailure(t *testing.T) {
	required := []playback.TransformationV3{
		{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
		{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}
	newNode := func(capability tonemap.Capability, startStatus int) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
				writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: required, ToneMapCapabilities: tonemap.Capabilities{capability}})
			case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
				if startStatus != http.StatusAccepted {
					w.WriteHeader(startStatus)
					return
				}
				var request transcodenode.TranscodeStartRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode remote start: %v", err)
					return
				}
				writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode})
			case r.Method == http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	}
	hardware := newNode(tonemap.Capability{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}, http.StatusServiceUnavailable)
	defer hardware.Close()
	failedSoftware := newNode(tonemap.Capability{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}, http.StatusServiceUnavailable)
	defer failedSoftware.Close()
	healthySoftware := newNode(tonemap.Capability{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}, http.StatusAccepted)
	defer healthySoftware.Close()

	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{URL: hardware.URL, Enabled: true, Healthy: true},
		{URL: failedSoftware.URL, Enabled: true, Healthy: true},
		{URL: healthySoftware.URL, Enabled: true, Healthy: true},
	})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.PlaybackLocalTranscodeFallbackSettingKey: "false"}}
	file := stableToneMapTransportFileV3(t)
	result := playback.PlannerResultV3{
		Plan:       &playback.PlanV3{PlanID: "plan:tone-map-retry", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: required},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetBitrateKbps: file.Bitrate,
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
	}
	transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), &playback.Session{ID: "session-tone-map-retry", UserID: 7, ProfileID: "profile-1"}, file, result, mediaAuthModeV3{})
	if transportErr != nil || transport.nodeURL != healthySoftware.URL || transport.toneMapMode != tonemap.ModeSoftware {
		t.Fatalf("transport = %+v error = %v, want second software node", transport, transportErr)
	}
	transport.rollback()
}

func TestPrepareTransportV3ClassifiesExhaustedRemoteLiveValidation(t *testing.T) {
	required := []playback.TransformationV3{
		{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
		{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3},
	}
	tests := []struct {
		name          string
		status        int
		validation    string
		wantCause     error
		wantRetryable bool
	}{
		{name: "stale metadata", status: http.StatusUnprocessableEntity, validation: transcodenode.ToneMapSourceRevisionChangedCode, wantCause: tonemap.ErrSourceRevisionChanged},
		{name: "preflight rejected", status: http.StatusUnprocessableEntity, validation: transcodenode.ToneMapSourcePreflightRejectedCode, wantCause: tonemap.ErrSourcePreflightRejected},
		{name: "probe unavailable", status: http.StatusServiceUnavailable, validation: transcodenode.ToneMapSourceValidationUnavailableCode, wantCause: playback.ErrToneMapSourceValidationUnavailable, wantRetryable: true},
		{name: "executor unavailable", status: http.StatusServiceUnavailable, validation: transcodenode.ToneMapExecutorUnavailableCode, wantCause: playback.ErrToneMapExecutorUnavailable, wantRetryable: true},
		{name: "generic recipe rejection", status: http.StatusUnprocessableEntity, wantRetryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
					writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: required, ToneMapCapabilities: tonemap.Capabilities{{
						Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
						SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
					}}})
				case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
					if tt.validation != "" {
						w.Header().Set(transcodenode.ToneMapExecutionErrorHeader, tt.validation)
					}
					w.WriteHeader(tt.status)
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer node.Close()

			transcodes := nodepool.NewTranscodePool()
			transcodes.SetNodes([]*nodepool.Node{{URL: node.URL, Enabled: true, Healthy: true}})
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.PlaybackLocalTranscodeFallbackSettingKey: "false"}}
			file := stableToneMapTransportFileV3(t)
			result := playback.PlannerResultV3{
				Plan:       &playback.PlanV3{PlanID: "plan:remote-live-validation", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: required},
				PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetBitrateKbps: file.Bitrate,
				SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
				ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware, ToneMapSourceKind: tonemap.SourcePQ,
				ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: tonemap.RevisionForFile(file),
			}

			_, transportErr := handler.prepareTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-remote-live-validation", UserID: 7, ProfileID: "profile-1"},
				file,
				result,
				mediaAuthModeV3{},
			)
			if transportErr == nil || transportErr.retryable != tt.wantRetryable ||
				(tt.wantCause != nil && !errors.Is(transportErr.cause, tt.wantCause)) ||
				(tt.wantCause == nil && transportErr.cause != nil) {
				t.Fatalf("transport error = %#v, want retryable=%t wrapping %v", transportErr, tt.wantRetryable, tt.wantCause)
			}
		})
	}
}

func TestPlanRequiresServerTransformationsV3(t *testing.T) {
	if planRequiresServerTransformationsV3(nil) {
		t.Fatal("nil plan must not require server transformations")
	}
	clientOnly := &playback.PlanV3{Transformations: []playback.TransformationV3{{Name: playback.ClientDV7ToDV81V3, Executor: "client", RecipeVersion: "1"}}}
	if planRequiresServerTransformationsV3(clientOnly) {
		t.Fatal("client-executed transformations must not require a server executor")
	}
	server := &playback.PlanV3{Transformations: []playback.TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"}}}
	if !planRequiresServerTransformationsV3(server) {
		t.Fatal("server-executed transformations must require executor validation")
	}
}

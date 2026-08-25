package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type compatToneMapInventoryPlanner struct {
	urls []string
}

func (p compatToneMapInventoryPlanner) PlanSession(string, string, bool, int) nodepool.Plan {
	return nodepool.Plan{}
}

func (p compatToneMapInventoryPlanner) TranscodeNodeURLs() []string {
	return p.urls
}

func TestCompatToneMapCapabilityInventoryFetchesNodesConcurrently(t *testing.T) {
	var active atomic.Int32
	var startedOnce sync.Once
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	serve := func(w http.ResponseWriter, _ *http.Request) {
		if active.Add(1) == 2 {
			startedOnce.Do(func() { close(bothStarted) })
		}
		<-release
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}})
	}
	first := httptest.NewServer(http.HandlerFunc(serve))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(serve))
	defer second.Close()

	handler := &PlaybackHandler{
		NodePlanner: compatToneMapInventoryPlanner{urls: []string{first.URL, second.URL}},
		SettingsRepo: stubSettingsReader{values: map[string]string{
			config.PlaybackLocalTranscodeFallbackSettingKey: "false",
		}},
	}
	result := make(chan tonemap.Capabilities, 1)
	go func() {
		capabilities, _, _ := handler.compatToneMapCapabilityInventory(context.Background(), time.Second)
		result <- capabilities
	}()
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

func TestCompatToneMapCapabilityInventoryHonorsSharedDeadline(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{{
			Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
			SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
		}}})
	}))
	defer fast.Close()

	handler := &PlaybackHandler{
		NodePlanner: compatToneMapInventoryPlanner{urls: []string{slow.URL, fast.URL}},
		SettingsRepo: stubSettingsReader{values: map[string]string{
			config.PlaybackLocalTranscodeFallbackSettingKey: "false",
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	capabilities, byNode, err := handler.compatToneMapCapabilityInventory(ctx, 25*time.Millisecond)
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("capability aggregation took %s, want shared caller deadline", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capability error = %v, want deadline exceeded", err)
	}
	if len(capabilities) != 1 || !capabilities.Supports(tonemap.ModeSoftware, tonemap.SourcePQ) {
		t.Fatalf("aggregated capabilities = %#v, want successful node retained", capabilities)
	}
	if _, ok := byNode[fast.URL]; !ok {
		t.Fatalf("per-node capabilities = %#v, want successful node retained", byNode)
	}
	if _, ok := byNode[slow.URL]; ok {
		t.Fatalf("per-node capabilities = %#v, failed node should be ignored", byNode)
	}
}

func TestPlanCompatTranscodeSessionFallsBackToSoftwareCapacity(t *testing.T) {
	newToneMapNode := func(capability tonemap.Capability) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{capability}})
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
	handler := &PlaybackHandler{
		NodePlanner: nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes),
		SettingsRepo: stubSettingsReader{values: map[string]string{
			config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
			config.PlaybackLocalTranscodeFallbackSettingKey:   "false",
		}},
	}
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Bitrate: 32_000, HDR: true,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", VideoRangeType: "HDR10", ColorTransfer: "smpte2084",
			ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", BitDepth: 10,
		}},
	}
	plan, err := handler.planCompatTranscodeSession(
		context.Background(), &playback.Session{ID: "compat-capacity"}, file,
		file.Bitrate, true,
	)
	if err != nil || plan.TranscodeNode == nil || plan.TranscodeNode.URL != software.URL {
		t.Fatalf("plan = %+v error = %v, want software node fallback", plan, err)
	}
}

func TestHandlePlaybackInfoReturnsServiceUnavailableWhenToneMapProbeIsIncomplete(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	version := subtitleSelectionVersion()
	version.HDR = true
	version.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Width: 1920, Height: 1080, BitDepth: 10,
		VideoRangeType: "HDR10", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}}
	handler.content = &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Versions:  []catalog.FileVersion{version},
	}}
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.compatToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return nil, context.DeadlineExceeded
	}

	request := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{"EnableDirectPlay":false,"EnableDirectStream":false}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeCtx))
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "token-1"}))
	recorder := httptest.NewRecorder()

	handler.HandlePlaybackInfo(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlePlaybackInfoKeepsDirectPlaybackWhenToneMapProbeIsIncomplete(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	version := subtitleSelectionVersion()
	version.HDR = true
	version.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Width: 1920, Height: 1080, BitDepth: 10,
		VideoRangeType: "HDR10", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}}
	handler.content = &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.compatToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return nil, context.DeadlineExceeded
	}

	request := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeCtx))
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "token-1"}))
	recorder := httptest.NewRecorder()

	handler.HandlePlaybackInfo(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response playbackInfoResponseDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.MediaSources) != 1 || !response.MediaSources[0].SupportsDirectPlay || response.MediaSources[0].SupportsTranscoding {
		t.Fatalf("media sources = %#v, want direct play retained with transcoding disabled", response.MediaSources)
	}
}

func TestHandlePlaybackInfoDoesNotBlameProbeForUnclassifiableHDRSource(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	version := subtitleSelectionVersion()
	version.HDR = true
	version.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Width: 1920, Height: 1080, BitDepth: 10,
		VideoRangeType: "DOVI", DolbyVision: "Dolby Vision Profile 5", DVProfile: 5,
	}}
	handler.content = &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Versions:  []catalog.FileVersion{version},
	}}
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	handler.compatToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return nil, context.DeadlineExceeded
	}

	request := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeCtx))
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "token-1"}))
	recorder := httptest.NewRecorder()

	handler.HandlePlaybackInfo(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with unsupported transcode omitted; body = %s", recorder.Code, recorder.Body.String())
	}
}

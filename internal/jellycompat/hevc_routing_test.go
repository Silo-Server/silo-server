package jellycompat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestPlaybackInfoHEVCUsesAllowedExecutors(t *testing.T) {
	hevc := playback.TransformationV3{Name: playback.TransformationVideoToHEVCV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToHEVCRecipeVersionV3}
	wrongVersion := hevc
	wrongVersion.RecipeVersion = "0"
	for _, tc := range []struct {
		name      string
		execution config.PlaybackExecutionPreference
		localHEVC bool
		remote    []playback.TransformationV3
		unhealthy bool
		want      string
	}{
		{name: "worker only ignores local HEVC", execution: config.PlaybackExecutionWorkerOnly, localHEVC: true, want: "h264"},
		{name: "worker HEVC without local encoder", execution: config.PlaybackExecutionWorkerOnly, remote: []playback.TransformationV3{hevc}, want: "hevc"},
		{name: "API only ignores worker HEVC", execution: config.PlaybackExecutionAPIOnly, remote: []playback.TransformationV3{hevc}, want: "h264"},
		{name: "API only uses local HEVC", execution: config.PlaybackExecutionAPIOnly, localHEVC: true, want: "hevc"},
		{name: "wrong worker recipe", execution: config.PlaybackExecutionWorkerOnly, localHEVC: true, remote: []playback.TransformationV3{wrongVersion}, want: "h264"},
		{name: "unhealthy worker", execution: config.PlaybackExecutionWorkerOnly, remote: []playback.TransformationV3{hevc}, unhealthy: true, want: "h264"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, routeID := newSubtitleSelectionHandler(t)
			version := catalog.FileVersion{FileID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "vp9", CodecAudio: "aac",
				VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Default: true}}}
			handler.content = &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}
			handler.SettingsRepo = stubSettingsReader{values: map[string]string{config.PlaybackAllowHEVCEncodingSettingKey: "true"}}
			handler.compatAudioRegistryProbe = func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
				return playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: hevc.Name, RecipeVersion: hevc.RecipeVersion, Available: tc.localHEVC}}), nil
			}
			policy := config.DefaultPlaybackRoutingPolicy()
			policy.VideoTranscodeExecution = tc.execution
			policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
			handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }
			node := dvStripNode(t, "http://worker.invalid", tc.remote...)
			node.Healthy = !tc.unhealthy
			handler.NodePlanner = dvStripNodePlanner{compatToneMapInventoryPlanner: compatToneMapInventoryPlanner{urls: []string{node.URL}}, nodes: map[string]*nodepool.Node{node.URL: node}}
			response := postPlaybackInfo(t, handler, routeID, `{"EnableDirectPlay":false,"EnableDirectStream":false,"DeviceProfile":{"TranscodingProfiles":[{"Type":"Video","Protocol":"hls","Container":"mp4","VideoCodec":"hevc","AudioCodec":"aac"},{"Type":"Video","Protocol":"hls","Container":"ts","VideoCodec":"h264","AudioCodec":"aac"}]}}`)
			stored, ok := handler.playbackStore.Get(response.PlaySessionID)
			if !ok || len(stored.MediaSources) != 1 {
				t.Fatalf("missing negotiated source: %+v", stored)
			}
			source := stored.MediaSources[0]
			if source.TargetVideoCodec != tc.want || !source.SupportsTranscoding {
				t.Fatalf("target=%q transcode=%v, want %q with a playable route", source.TargetVideoCodec, source.SupportsTranscoding, tc.want)
			}
			wantContainer := "ts"
			if tc.want == "hevc" {
				wantContainer = "mp4"
			}
			if got := response.MediaSources[0].TranscodingContainer; got != wantContainer {
				t.Fatalf("container=%q, want %q", got, wantContainer)
			}
		})
	}
}

func TestHEVCRequiresCompleteRecipeOnOneExecutor(t *testing.T) {
	hevc := playback.TransformationV3{Name: playback.TransformationVideoToHEVCV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToHEVCRecipeVersionV3}
	audio := playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}
	capabilities := tonemap.Capabilities{{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}}
	source := PlaybackMediaSource{TargetVideoCodec: compatVideoCodecHEVC, TargetAudioChannels: 2, TranscodeAudio: true,
		Version: catalog.FileVersion{HDR: true, VideoTracks: []models.VideoTrack{{Codec: "vp9", VideoRangeType: "HDR10", ColorTransfer: "smpte2084"}}, AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}}}}
	newNode := func(url string, transformations []playback.TransformationV3, toneMap tonemap.Capabilities) *nodepool.Node {
		data, err := json.Marshal(playback.HWAccelInfo{Transformations: transformations, ToneMapCapabilities: toneMap})
		if err != nil {
			t.Fatal(err)
		}
		return &nodepool.Node{URL: url, Enabled: true, Healthy: true, Capabilities: data}
	}
	hevcOnly := newNode("http://hevc.invalid", []playback.TransformationV3{hevc}, nil)
	others := newNode("http://h264.invalid", []playback.TransformationV3{audio}, capabilities)
	complete := newNode("http://complete.invalid", []playback.TransformationV3{hevc, audio}, capabilities)
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	policy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
	handler := &PlaybackHandler{PlaybackConfig: func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} },
		SettingsRepo: stubSettingsReader{values: map[string]string{config.PlaybackTranscodeSoftwareToneMapSettingKey: "true"}},
		compatAudioRegistryProbe: func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
			return playback.NewTransformationRegistryV3(nil), nil
		},
	}
	setNodes := func(nodes ...*nodepool.Node) {
		planner := dvStripNodePlanner{nodes: make(map[string]*nodepool.Node)}
		for _, node := range nodes {
			planner.urls = append(planner.urls, node.URL)
			planner.nodes[node.URL] = node
		}
		handler.NodePlanner = planner
	}
	setNodes(hevcOnly, others)
	if handler.compatHEVCExecutable(t.Context(), source) {
		t.Fatal("HEVC accepted with audio and tone mapping only available on another executor")
	}
	setNodes(hevcOnly, others, complete)
	if !handler.compatHEVCExecutable(t.Context(), source) {
		t.Fatal("complete HEVC executor rejected")
	}
	excluded := map[string]struct{}{"previous": {}}
	eligible, shapes := handler.compatHEVCRouting(t.Context(), source, nil, excluded)
	if !eligible(complete) || eligible(hevcOnly) || eligible(others) || eligible(nil) {
		t.Fatal("routing admitted an incomplete HEVC executor")
	}
	if _, ok := shapes["hls_video_api"]; !ok {
		t.Fatal("local fallback without HEVC remained eligible")
	}
	if _, ok := shapes["previous"]; !ok {
		t.Fatal("lost previous route exclusion")
	}
	if len(excluded) != 1 {
		t.Fatal("mutated caller exclusions")
	}
	eligible, _ = handler.compatHEVCRouting(t.Context(), source, func(*nodepool.Node) bool { return false }, nil)
	if eligible(complete) {
		t.Fatal("HEVC eligibility bypassed prior route constraint")
	}
}

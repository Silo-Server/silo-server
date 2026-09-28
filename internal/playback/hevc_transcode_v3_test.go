package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPlanPlaybackV3HEVCTranscodeSelectionAndFallbacks(t *testing.T) {
	tests := []struct {
		name          string
		allowHEVC     bool
		hlsHEVC       bool
		hevcAvailable bool
		wantCodec     string
	}{
		{name: "enabled", allowHEVC: true, hlsHEVC: true, hevcAvailable: true, wantCodec: "hevc"},
		{name: "disabled", hlsHEVC: true, hevcAvailable: true, wantCodec: "h264"},
		{name: "delivery does not support HEVC", allowHEVC: true, hevcAvailable: true, wantCodec: "h264"},
		{name: "HEVC encoder unavailable", allowHEVC: true, hlsHEVC: true, wantCodec: "h264"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := hevcTranscodePlannerInputV3(tc.allowHEVC, tc.hlsHEVC, tc.hevcAvailable)
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.TargetVideoCodec != tc.wantCodec || result.Plan.EffectiveRecipe.VideoCodec != tc.wantCodec {
				t.Fatalf("plan = %s, want %s transcode", ExplainPlannerResultV3(result), tc.wantCodec)
			}
			if tc.wantCodec == "hevc" {
				if result.Plan.EffectiveRecipe.VideoSampleEntry != VideoSampleEntryHVC1 || result.Plan.Transformations[0].Name != TransformationVideoToHEVCV3 {
					t.Fatalf("HEVC plan lacks fMP4 identity: %#v", result.Plan)
				}
				recipe := FreezeExecutableRecipeV3(result)
				if !recipe.Valid() || recipe.TargetVideoCodec != "hevc" || recipe.PlannerResult(result.Plan).TargetVideoCodec != "hevc" {
					t.Fatalf("frozen HEVC recipe lost target identity: %#v", recipe)
				}
			} else if result.Plan.EffectiveRecipe.VideoSampleEntry != "" || result.Plan.Transformations[0].Name != TransformationVideoToH264V3 {
				t.Fatalf("H.264 fallback retained HEVC identity: %#v", result.Plan)
			}
		})
	}
}

func TestPlanPlaybackV3HEVCFailureFallsBackToH264(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	first := PlanPlaybackV3(input)
	if first.Plan == nil || first.TargetVideoCodec != "hevc" {
		t.Fatalf("first plan = %s, want HEVC", ExplainPlannerResultV3(first))
	}
	input.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
	second := PlanPlaybackV3(input)
	if second.Plan == nil || second.TargetVideoCodec != "h264" || second.Plan.Transformations[0].Name != TransformationVideoToH264V3 {
		t.Fatalf("failed HEVC did not downgrade to H.264: %s", ExplainPlannerResultV3(second))
	}
}

func TestPlanPlaybackV3HEVCFailureDoesNotGiveH264ToHEVCOnlyHLS(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	hls := input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.VideoCodecs = []string{"hevc"}
	input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	first := PlanPlaybackV3(input)
	if first.Plan == nil || first.TargetVideoCodec != "hevc" {
		t.Fatalf("first plan = %s, want HEVC", ExplainPlannerResultV3(first))
	}
	input.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
	result := PlanPlaybackV3(input)
	if result.Terminal == nil || result.Terminal.Reason != "adaptation_unavailable" {
		t.Fatalf("HEVC-only client received an invalid fallback: %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3HEVCWithBoundedDecoderLevelFallsBackToH264(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	input.Request.Capabilities.VideoDecode[0].Levels = []int{123}
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.TargetVideoCodec != "h264" {
		t.Fatalf("level-bounded decoder received unpinned HEVC: %s", ExplainPlannerResultV3(result))
	}
}

func TestHEVCTranscodeUsesHVC1FMP4(t *testing.T) {
	opts := TranscodeOpts{InputPath: "/media/source.mkv", OutputDir: t.TempDir(), SourceVideoCodec: "av1", TargetCodecVideo: "hevc", TargetCodecAudio: "aac", VideoSampleEntry: VideoSampleEntryHVC1}
	if got := HLSOutputContainer(opts); got != OutputContainerFMP4 {
		t.Fatalf("HEVC HLS container = %q, want fmp4", got)
	}
	args := strings.Join(buildFFmpegArgs(opts), " ")
	for _, want := range []string{"-c:v libx265", "-tag:v hvc1", "-hls_segment_type fmp4", "-hls_segment_options movflags=+frag_discont", "seg_%05d.m4s"} {
		if !strings.Contains(args, want) {
			t.Fatalf("HEVC args missing %q: %s", want, args)
		}
	}
}

func TestProbeTransformationRegistryV3AdvertisesValidatedHEVCEncoder(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) : ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := ProbeTransformationRegistryV3(context.Background(), ffmpeg)
	if !registry.Available(TransformationVideoToHEVCV3) {
		t.Fatal("HEVC encoder was not advertised")
	}
	if registry.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatal("complete HEVC inventory should stay cached")
	}
}

func TestProbeTransformationRegistryV3DoesNotAdvertiseFailedHEVCSmoke(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) : ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*' -c:v libx265 '*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := ProbeTransformationRegistryV3(context.Background(), ffmpeg)
	if registry.Available(TransformationVideoToHEVCV3) {
		t.Fatal("HEVC transformation advertised despite failed encode smoke")
	}
	if registry.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatal("unsupported HEVC inventory should stay cached")
	}
}

func TestProbeTransformationRegistryV3HEVCTimeoutKeepsBaselineInventory(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) : ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*' -c:v libx265 '*) exec sleep 30 ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil {
		t.Fatalf("optional HEVC timeout invalidated baseline inventory: %v", err)
	}
	if registry.Available(TransformationVideoToHEVCV3) || !registry.Available(TransformationVideoToH264V3) || !registry.Available(TransformationAudioToAACV3) {
		t.Fatalf("HEVC timeout changed baseline capabilities: %#v", registry.Advertised())
	}
	if registry.NeedsRefresh(time.Now()) || registry.refreshAfter.IsZero() {
		t.Fatal("incomplete optional probe must keep baseline capabilities cached briefly")
	}
	if registry.NeedsRefresh(registry.refreshAfter.Add(-time.Nanosecond)) || !registry.NeedsRefresh(registry.refreshAfter) {
		t.Fatal("incomplete HEVC probe did not become retryable at its expiry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = ProbeTransformationRegistryWithToneMapV3Result(ctx, ffmpeg, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller cancellation was swallowed: %v", err)
	}

	// Recovery changes the fake encoder's behavior; expiry above uses an
	// explicit timestamp, so no test needs to wait for the retry interval.
	if err := os.WriteFile(ffmpeg, []byte(strings.Replace(script, "exec sleep 30", "exit 0", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	recovered, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil || !recovered.Available(TransformationVideoToHEVCV3) ||
		!recovered.Available(TransformationVideoToH264V3) || !recovered.Available(TransformationAudioToAACV3) ||
		recovered.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatalf("HEVC recovery did not restore a complete stable inventory: %v, %#v", err, recovered.Advertised())
	}
}

func TestProbeTransformationRegistryV3HEVCStartupFailureExpires(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	// Drop execute permission after the last baseline probe. The optional
	// HEVC command then fails to start rather than returning an unsupported
	// encoder result from a running FFmpeg process.
	script := "#!/bin/sh\ncase \"$2\" in\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*'anullsrc=r=8000:cl=5.1'*) chmod -x \"$0\" ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil || registry.Available(TransformationVideoToHEVCV3) || !registry.NeedsRefresh(time.Now().Add(time.Minute)) {
		t.Fatalf("optional process startup failure did not expire: %v, %#v", err, registry)
	}
}

func TestProbeTransformationRegistryV3MissingHEVCEncoderStaysCached(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-encoders) echo ' V....D libx264 H.264'; echo ' A....D aac AAC' ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil || registry.Available(TransformationVideoToHEVCV3) || registry.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatalf("missing optional encoder should remain a stable negative result: %v, %#v", err, registry)
	}
}

func hevcTranscodePlannerInputV3(allowHEVC, hlsHEVC, hevcAvailable bool) PlannerInputV3 {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "av1", CodecAudio: "aac", Resolution: "1080p", Bitrate: 8_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "av1", Profile: "Main", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
	request := validStartRequestV3()
	request.Capabilities.CodecsVideo = []string{"hevc"}
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"Main"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 10_000, Hardware: true}}
	hls := request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.Containers = []string{"hls"}
	hls.AudioDecodeCodecs = []string{"aac"}
	if hlsHEVC {
		hls.VideoCodecs = []string{"h264", "hevc"}
	} else {
		hls.VideoCodecs = []string{"h264"}
	}
	request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	registry := []TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: true},
	}
	if hevcAvailable {
		registry = append(registry, TransformationSpecV3{Name: TransformationVideoToHEVCV3, RecipeVersion: TransformationVideoToHEVCRecipeVersionV3, Available: true})
	}
	return PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, AllowHEVCEncoding: allowHEVC}, Registry: NewTransformationRegistryV3(registry)}
}

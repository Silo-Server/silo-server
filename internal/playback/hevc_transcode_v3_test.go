package playback

import (
	"context"
	"errors"
	"os"
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

func TestProbeTransformationRegistryV3HEVCTimeoutKeepsBaselineInventory(t *testing.T) {
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) : ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*' -c:v libx265 '*) exec sleep 30 ;;\nesac\n"
	ffmpeg := writeProbeFFmpegV3(t, script)
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

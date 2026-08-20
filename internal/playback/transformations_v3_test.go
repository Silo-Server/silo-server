package playback

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestH264EncoderAvailabilityAcceptsAnyPipelineEncoder(t *testing.T) {
	cases := []struct {
		name    string
		listing string
		want    bool
	}{
		{"software", " V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC", true},
		{"qsv", " V..... h264_qsv             H.264 / AVC / MPEG-4 AVC (Intel Quick Sync Video acceleration)", true},
		{"vaapi", " V..... h264_vaapi           H.264/AVC (VAAPI)", true},
		{"nvenc", " V....D h264_nvenc           NVIDIA NVENC H.264 encoder", true},
		{"videotoolbox", " V..... h264_videotoolbox    VideoToolbox H.264 Encoder", true},
		{"hevc_only", " V..... libx265\n V..... hevc_videotoolbox", false},
		{"empty", "", false},
	}
	for _, value := range cases {
		t.Run(value.name, func(t *testing.T) {
			if got := h264EncoderAvailableV3([]byte(value.listing)); got != value.want {
				t.Fatalf("h264EncoderAvailableV3 = %v, want %v", got, value.want)
			}
		})
	}
}

func TestProbeTransformationRegistryV3AdvertisesVideoToH264RecipeVersion2(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) echo dovi_rpu ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' A....D aac AAC' ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	registry := ProbeTransformationRegistryV3(context.Background(), ffmpeg)
	for _, transformation := range registry.Advertised() {
		if transformation.Name == TransformationVideoToH264V3 {
			if transformation.RecipeVersion != "2" {
				t.Fatalf("video_to_h264 recipe version = %q, want 2", transformation.RecipeVersion)
			}
			return
		}
	}
	t.Fatal("video_to_h264 was not advertised")
}

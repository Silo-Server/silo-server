package playback

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestProbeTransformationRegistryV3AdvertisesExactNoiseBitstreamFilter(t *testing.T) {
	cases := []struct {
		name      string
		listing   string
		help      string
		bsfsExit  string
		helpExit  string
		available bool
	}{
		{name: "exact recipe", listing: "noise", help: "-drop <string>", bsfsExit: "0", helpExit: "0", available: true},
		{name: "among filters", listing: "dovi_rpu noise filter_units", help: "-amount <string> -drop <string> -dropamount <int>", bsfsExit: "0", helpExit: "0", available: true},
		{name: "legacy integer option", listing: "noise", help: "-dropamount <int>", bsfsExit: "0", helpExit: "0", available: false},
		{name: "substring only", listing: "noise_reduction", help: "-drop <string>", bsfsExit: "0", helpExit: "0", available: false},
		{name: "missing", listing: "dovi_rpu", help: "-drop <string>", bsfsExit: "0", helpExit: "0", available: false},
		{name: "partial BSF output on failure", listing: "noise", help: "-drop <string>", bsfsExit: "1", helpExit: "0", available: false},
		{name: "partial help output on failure", listing: "noise", help: "-drop <string>", bsfsExit: "0", helpExit: "1", available: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
			script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) printf '%s\\n' '" + test.listing + "'; exit " + test.bsfsExit + " ;;\n-h) printf '%s\\n' '" + test.help + "'; exit " + test.helpExit + " ;;\n-encoders) : ;;\nesac\n"
			if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}

			registry := ProbeTransformationRegistryV3(context.Background(), ffmpeg)
			if got := registry.Available(TransformationServerHEVCResumeLeadingPictureDropV3); got != test.available {
				t.Fatalf("resume transformation available = %v, want %v", got, test.available)
			}
			advertised := false
			for _, transformation := range registry.Advertised() {
				if transformation.Name == TransformationServerHEVCResumeLeadingPictureDropV3 &&
					(transformation.RecipeVersion != TransformationServerHEVCResumeLeadingPictureDropVersionV3 || len(transformation.ValidatedClaims) != 1 || transformation.ValidatedClaims[0] != ClaimResumeLeadingPicturesRemovedV3) {
					t.Fatalf("resume transformation = %#v", transformation)
				}
				if transformation.Name == TransformationServerHEVCResumeLeadingPictureDropV3 {
					advertised = true
				}
			}
			if advertised != test.available {
				t.Fatalf("resume transformation advertised = %v, want %v", advertised, test.available)
			}
		})
	}
}

func TestSupportsHEVCResumeLeadingPictureRecipeRejectsPartialHelpOnTimeout(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' '-drop <string>'\nexec sleep 1\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if supportsHEVCResumeLeadingPictureRecipeV3(ctx, ffmpeg) {
		t.Fatal("timed-out help probe accepted partial output")
	}
}

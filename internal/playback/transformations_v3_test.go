package playback

import "testing"

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

package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbePrimaryVideoTrackReusesScannerNormalization(t *testing.T) {
	tests := []struct {
		name            string
		stream          string
		wantRangeType   string
		wantTransfer    string
		wantProfile     string
		wantBitDepth    int
		wantDVProfile   int
		wantDVLevel     int
		wantDVCompatID  int
		wantDVConfig    bool
		wantDVCompat    bool
		wantDVBaseLayer bool
		wantDVRPU       bool
		wantEnhancement bool
	}{
		{
			name:          "PQ",
			stream:        `{"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","level":153,"width":3840,"height":2160,"avg_frame_rate":"24000/1001","pix_fmt":"yuv420p10le","color_range":"tv","color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc"}`,
			wantRangeType: "HDR10", wantTransfer: "smpte2084", wantProfile: "Main 10", wantBitDepth: 10,
		},
		{
			name:          "HLG",
			stream:        `{"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","level":153,"width":3840,"height":2160,"avg_frame_rate":"50/1","pix_fmt":"yuv420p10le","color_range":"tv","color_primaries":"bt2020","color_transfer":"arib-std-b67","color_space":"bt2020nc"}`,
			wantRangeType: "HLG", wantTransfer: "arib-std-b67", wantProfile: "Main 10", wantBitDepth: 10,
		},
		{
			name:          "Dolby Vision provenance",
			stream:        `{"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","level":153,"width":3840,"height":2160,"avg_frame_rate":"24000/1001","pix_fmt":"yuv420p10le","color_range":"tv","color_primaries":"bt2020","color_transfer":"smpte2084","color_space":"bt2020nc","side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":7,"dv_level":6,"bl_present_flag":1,"el_present_flag":1,"rpu_present_flag":1,"dv_bl_signal_compatibility_id":6}]}`,
			wantRangeType: "DOVIWithEL", wantTransfer: "smpte2084", wantProfile: "Main 10", wantBitDepth: 10,
			wantDVProfile: 7, wantDVLevel: 6, wantDVCompatID: 6, wantDVConfig: true, wantDVCompat: true,
			wantDVBaseLayer: true, wantDVRPU: true, wantEnhancement: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ffprobe := writePrimaryVideoFFprobe(t, `{"streams":[`+tt.stream+`]}`)
			track, err := ProbePrimaryVideoTrack(context.Background(), ffprobe, "movie.mkv")
			if err != nil {
				t.Fatalf("ProbePrimaryVideoTrack() error = %v", err)
			}
			if track.Codec != "hevc" || track.Profile != tt.wantProfile || track.Level != 153 ||
				track.Width != 3840 || track.Height != 2160 || track.BitDepth != tt.wantBitDepth ||
				track.ColorRange != "tv" || track.ColorPrimaries != "bt2020" ||
				track.ColorTransfer != tt.wantTransfer || track.ColorSpace != "bt2020nc" ||
				track.VideoRangeType != tt.wantRangeType {
				t.Fatalf("normalized primary track = %#v", track)
			}
			if track.DVProfile != tt.wantDVProfile || track.DVLevel != tt.wantDVLevel ||
				track.DVBLCompatID != tt.wantDVCompatID || track.DVConfigPresent != tt.wantDVConfig ||
				track.DVBLCompatIDPresent != tt.wantDVCompat || track.DVBLPresent != tt.wantDVBaseLayer ||
				track.DVRPUPresent != tt.wantDVRPU || track.DVELPresent != tt.wantEnhancement {
				t.Fatalf("normalized Dolby Vision provenance = %#v", track)
			}
		})
	}
}

func TestProbePrimaryVideoTrackRejectsMissingPrimaryVideo(t *testing.T) {
	ffprobe := writePrimaryVideoFFprobe(t, `{"streams":[{"index":0,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}},{"index":1,"codec_name":"aac","codec_type":"audio"}]}`)
	_, err := ProbePrimaryVideoTrack(context.Background(), ffprobe, "audio.m4b")
	if !errors.Is(err, ErrPrimaryVideoNotFound) {
		t.Fatalf("ProbePrimaryVideoTrack() error = %v, want ErrPrimaryVideoNotFound", err)
	}
}

func TestProbePrimaryVideoTrackRejectsMalformedJSON(t *testing.T) {
	ffprobe := writePrimaryVideoFFprobe(t, `{not-json`)
	if _, err := ProbePrimaryVideoTrack(context.Background(), ffprobe, "movie.mkv"); err == nil {
		t.Fatal("ProbePrimaryVideoTrack() accepted malformed FFprobe JSON")
	}
}

func TestProbePrimaryVideoTrackHonorsCallerTimeout(t *testing.T) {
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := ProbePrimaryVideoTrack(ctx, ffprobe, "movie.mkv")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ProbePrimaryVideoTrack() error = %v, want context deadline", err)
	}
}

func writePrimaryVideoFFprobe(t *testing.T, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	script := "#!/bin/sh\nprintf '%s' '" + output + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

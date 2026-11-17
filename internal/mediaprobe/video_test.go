package mediaprobe

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFFprobePathFromFFmpegRewritesOnlyExecutableName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain executable", in: "ffmpeg", want: "ffprobe"},
		{name: "executable suffix", in: "jellyfin-ffmpeg.exe", want: "jellyfin-ffprobe.exe"},
		{name: "case-insensitive Windows executable", in: "FFMPEG.EXE", want: "ffprobe.EXE"},
		{name: "sibling path", in: filepath.Join("opt", "bin", "ffmpeg"), want: filepath.Join("opt", "bin", "ffprobe")},
		{name: "parent name is not rewritten", in: filepath.Join("opt", "ffmpeg-suite", "bin", "custom"), want: "ffprobe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FFprobePathFromFFmpeg(tt.in); got != tt.want {
				t.Fatalf("FFprobePathFromFFmpeg(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// hdr10PlusFrameSideData is the side data FFmpeg 8.1.3 (jellyfin-ffmpeg)
// reports on the first decoded frame of an HDR10+ HEVC file. The stream-level
// probe of the same file carries none of it.
var hdr10PlusFrameSideData = []SideData{
	{SideDataType: "Mastering display metadata"},
	{SideDataType: "Content light level metadata"},
	{SideDataType: "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"},
}

func TestNormalizeVideoTrackReadsHDR10PlusFromFrameSideData(t *testing.T) {
	dv8 := []SideData{{SideDataType: "DOVI configuration record", DVProfile: 8, DVLevel: 6, DVBlPresent: 1, DVRPUPresent: 1, DVBLCompatID: 1, DVBLCompatIDPresent: true}}
	tests := []struct {
		name          string
		stream        Stream
		wantHDR10Plus bool
		wantRangeType string
	}{
		{
			name:          "PQ stream with HDR10+ frame metadata",
			stream:        Stream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084", FrameSideDataList: hdr10PlusFrameSideData},
			wantHDR10Plus: true,
			wantRangeType: "HDR10Plus",
		},
		{
			name:          "Dolby Vision 8.1 with HDR10+ frame metadata",
			stream:        Stream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084", SideDataList: dv8, FrameSideDataList: hdr10PlusFrameSideData},
			wantHDR10Plus: true,
			wantRangeType: "DOVIWithHDR10Plus",
		},
		{
			name:          "PQ stream whose frame has only static metadata",
			stream:        Stream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084", FrameSideDataList: hdr10PlusFrameSideData[:2]},
			wantRangeType: "HDR10",
		},
		{
			name:          "PQ stream whose frame was not read",
			stream:        Stream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084"},
			wantRangeType: "HDR10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			track := NormalizeVideoTrack(tt.stream)
			if track.HDR10Plus != tt.wantHDR10Plus || track.VideoRangeType != tt.wantRangeType {
				t.Fatalf("HDR10Plus = %v, VideoRangeType = %q; want %v, %q", track.HDR10Plus, track.VideoRangeType, tt.wantHDR10Plus, tt.wantRangeType)
			}
		})
	}
}

func TestNormalizeVideoTrackRecordsTheFirstFrameRange(t *testing.T) {
	tests := []struct {
		name   string
		stream Stream
		want   string
	}{
		{name: "full-range frame under a limited tag", stream: Stream{ColorRange: "tv", FrameSideDataRead: true, FrameColorRange: "pc"}, want: "pc"},
		{name: "frame without a range", stream: Stream{ColorRange: "tv", FrameSideDataRead: true}, want: "unknown"},
		{name: "frame not read", stream: Stream{ColorRange: "tv"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.stream.CodecType, tt.stream.CodecName, tt.stream.ColorTransfer = "video", "hevc", "smpte2084"
			track := NormalizeVideoTrack(tt.stream)
			if track.FrameColorRange != tt.want || track.ColorRange != "tv" {
				t.Fatalf("FrameColorRange = %q, ColorRange = %q; want %q, %q", track.FrameColorRange, track.ColorRange, tt.want, "tv")
			}
		})
	}
}

func TestNeedsFrameSideDataOnlyForPrimaryPQVideo(t *testing.T) {
	tests := []struct {
		name   string
		stream Stream
		want   bool
	}{
		{name: "PQ video", stream: Stream{CodecType: "video", ColorTransfer: "smpte2084"}, want: true},
		{name: "HLG video", stream: Stream{CodecType: "video", ColorTransfer: "arib-std-b67"}},
		{name: "SDR video", stream: Stream{CodecType: "video", ColorTransfer: "bt709"}},
		{name: "PQ cover art", stream: Stream{CodecType: "video", ColorTransfer: "smpte2084", Disposition: Disposition{AttachedPic: 1}}},
		{name: "audio", stream: Stream{CodecType: "audio"}},
	}
	for _, tt := range tests {
		if got := NeedsFrameSideData(tt.stream); got != tt.want {
			t.Errorf("%s: NeedsFrameSideData() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRangeTypeWithHDR10PlusKeepsDolbyVision(t *testing.T) {
	tests := []struct {
		name  string
		track models.VideoTrack
		want  string
	}{
		{name: "HDR10", track: models.VideoTrack{ColorTransfer: "smpte2084", VideoRangeType: "HDR10"}, want: "HDR10Plus"},
		{name: "DV 8.1 with a profile", track: models.VideoTrack{ColorTransfer: "smpte2084", DVProfile: 8, DVBLCompatID: 1, VideoRangeType: "DOVIWithHDR10"}, want: "DOVIWithHDR10Plus"},
		{name: "DV 7 with a profile", track: models.VideoTrack{ColorTransfer: "smpte2084", DVProfile: 7, VideoRangeType: "DOVIWithEL"}, want: "DOVIWithELHDR10Plus"},
		// Older probes and seeded rows can name Dolby Vision only here.
		{name: "DV 8.1 range type only", track: models.VideoTrack{ColorTransfer: "smpte2084", VideoRangeType: "DOVIWithHDR10"}, want: "DOVIWithHDR10Plus"},
		{name: "DV 7 range type only", track: models.VideoTrack{ColorTransfer: "smpte2084", VideoRangeType: "DOVIWithEL"}, want: "DOVIWithELHDR10Plus"},
		{name: "DV 5 range type only", track: models.VideoTrack{ColorTransfer: "smpte2084", VideoRangeType: "DOVI"}, want: "DOVI"},
	}
	for _, tt := range tests {
		if got := RangeTypeWithHDR10Plus(tt.track); got != tt.want {
			t.Errorf("%s: RangeTypeWithHDR10Plus() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStreamBitrateKbps(t *testing.T) {
	tests := []struct {
		name   string
		stream Stream
		want   int
	}{
		{name: "bit_rate", stream: Stream{BitRate: "640000"}, want: 640},
		{name: "bit_rate wins over tag", stream: Stream{BitRate: "640000", Tags: map[string]string{matroskaBPSTag: "1"}}, want: 640},
		// Matroska reports bit_rate N/A and keeps the rate in mkvmerge's tags.
		{name: "matroska BPS tag", stream: Stream{BitRate: "N/A", Tags: map[string]string{matroskaBPSTag: "1509000"}}, want: 1509},
		{name: "older BPS-eng tag", stream: Stream{Tags: map[string]string{matroskaBPSEngTag: "448000"}}, want: 448},
		{name: "BPS-eng wins over BPS", stream: Stream{Tags: map[string]string{matroskaBPSEngTag: "448000", matroskaBPSTag: "640000"}}, want: 448},
		{name: "unparseable tag", stream: Stream{Tags: map[string]string{matroskaBPSTag: "fast"}}, want: 0},
		// A corrupt tag must stay unknown, not become a huge bitrate.
		{name: "overflowing tag falls through", stream: Stream{Tags: map[string]string{matroskaBPSEngTag: "99999999999999999999", matroskaBPSTag: "640000"}}, want: 640},
		{name: "implausible tag", stream: Stream{Tags: map[string]string{matroskaBPSTag: "3000000000000"}}, want: 0},
		{name: "unknown", stream: Stream{}, want: 0},
	}
	for _, tt := range tests {
		if got := StreamBitrateKbps(tt.stream); got != tt.want {
			t.Errorf("%s: StreamBitrateKbps() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// dolbyVisionFrameJSON is FFmpeg 8.1.3's first-frame "Dolby Vision Metadata"
// for a Profile 7 remux, trimmed to the fields the scanner asks for, with each
// of the three components' NLQ coefficients set to nlq.
func dolbyVisionFrameJSON(nlq string) string {
	component := `{"pieces":[{"mapping_idc":0,"poly_order":1,"poly_coef":"0 8388608"}]` + nlq + `}`
	return `[{"side_data_type":"Dolby Vision RPU Data"},` +
		`{"side_data_type":"Dolby Vision Metadata","coef_log2_denom":23,"components":[` +
		component + "," + component + "," + component + `]}]`
}

func TestNormalizeVideoTrackReadsTheEnhancementLayerTypeFromTheFirstFrame(t *testing.T) {
	dv7 := []SideData{{SideDataType: "DOVI configuration record", DVProfile: 7, DVLevel: 6, DVBlPresent: 1, DVElPresent: 1, DVRPUPresent: 1, DVBLCompatID: 6, DVBLCompatIDPresent: true}}
	tests := []struct {
		name  string
		frame string
		read  bool
		want  string
	}{
		// A UHD Blu-ray remux with a minimal enhancement layer.
		{name: "minimal", frame: dolbyVisionFrameJSON(`,"nlq_offset":0,"vdr_in_max":8388608,"linear_deadzone_slope":0,"linear_deadzone_threshold":0`), read: true, want: "mel"},
		// A UHD Blu-ray remux with a full enhancement layer.
		{name: "full", frame: dolbyVisionFrameJSON(`,"nlq_offset":512,"vdr_in_max":1048576,"linear_deadzone_slope":2048,"linear_deadzone_threshold":0`), read: true, want: "fel"},
		{name: "no NLQ coefficients", frame: dolbyVisionFrameJSON(""), read: true, want: "unknown"},
		{name: "frame without Dolby Vision metadata", frame: `[{"side_data_type":"Mastering display metadata"}]`, read: true, want: "unknown"},
		{name: "frame not read", frame: `[]`, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var frame []SideData
			if err := json.Unmarshal([]byte(tt.frame), &frame); err != nil {
				t.Fatal(err)
			}
			stream := Stream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084", SideDataList: dv7,
				FrameSideDataList: frame, FrameSideDataRead: tt.read}
			if got := NormalizeVideoTrack(stream).DVEnhancementLayer; got != tt.want {
				t.Fatalf("DVEnhancementLayer = %q, want %q", got, tt.want)
			}
		})
	}
	single := Stream{CodecType: "video", CodecName: "hevc", ColorTransfer: "smpte2084",
		SideDataList: []SideData{{SideDataType: "DOVI configuration record", DVProfile: 8, DVBlPresent: 1, DVRPUPresent: 1}}}
	if got := NormalizeVideoTrack(single).DVEnhancementLayer; got != "none" {
		t.Fatalf("single-layer DVEnhancementLayer = %q, want none", got)
	}
}

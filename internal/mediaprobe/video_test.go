package mediaprobe

import (
	"encoding/json"
	"path/filepath"
	"testing"
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

// dolbyVisionFrameJSON is FFmpeg 8.1.3's first-frame "Dolby Vision Metadata"
// for a Profile 7 remux, trimmed to the fields the scanner asks for, with each
// of the three components' NLQ coefficients set to nlq, or the last one's to
// lastNLQ when given.
func dolbyVisionFrameJSON(nlq string, lastNLQ ...string) string {
	component := func(nlq string) string {
		return `{"pieces":[{"mapping_idc":0,"poly_order":1,"poly_coef":"0 8388608"}]` + nlq + `}`
	}
	last := nlq
	if len(lastNLQ) > 0 {
		last = lastNLQ[0]
	}
	return `[{"side_data_type":"Dolby Vision RPU Data"},` +
		`{"side_data_type":"Dolby Vision Metadata","coef_log2_denom":23,"components":[` +
		component(nlq) + "," + component(nlq) + "," + component(last) + `]}]`
}

func TestNormalizeVideoTrackReadsTheEnhancementLayerTypeFromTheFirstFrame(t *testing.T) {
	const minimalNLQ = `,"nlq_offset":0,"vdr_in_max":8388608,"linear_deadzone_slope":0,"linear_deadzone_threshold":0`
	dv7 := []SideData{{SideDataType: "DOVI configuration record", DVProfile: 7, DVLevel: 6, DVBlPresent: 1, DVElPresent: 1, DVRPUPresent: 1, DVBLCompatID: 6, DVBLCompatIDPresent: true}}
	tests := []struct {
		name  string
		frame string
		read  bool
		want  string
	}{
		// A UHD Blu-ray remux with a minimal enhancement layer.
		{name: "minimal", frame: dolbyVisionFrameJSON(minimalNLQ), read: true, want: "mel"},
		// A UHD Blu-ray remux with a full enhancement layer.
		{name: "full", frame: dolbyVisionFrameJSON(`,"nlq_offset":512,"vdr_in_max":1048576,"linear_deadzone_slope":2048,"linear_deadzone_threshold":0`), read: true, want: "fel"},
		// One coefficient off the identity in one component is enough.
		{name: "one component's deadzone", frame: dolbyVisionFrameJSON(minimalNLQ, `,"nlq_offset":0,"vdr_in_max":8388608,"linear_deadzone_slope":0,"linear_deadzone_threshold":1`), read: true, want: "fel"},
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

// Dolby Vision profile 10 (AV1) takes its range type from the base-layer
// compatibility id alone. A 10.0 stream is PQ-tagged too, so the transfer
// must never turn an unmapped id into an HDR10-compatible range type.
func TestNormalizeVideoTrackProfile10RangeType(t *testing.T) {
	tests := []struct {
		name      string
		compatID  int
		transfer  string
		frameData []SideData
		want      string
	}{
		{name: "10.0 PQ", compatID: 0, transfer: "smpte2084", want: "DOVI"},
		{name: "10.1 HDR10 base", compatID: 1, transfer: "smpte2084", want: "DOVIWithHDR10"},
		{name: "10.1 HDR10+ base", compatID: 1, transfer: "smpte2084", frameData: hdr10PlusFrameSideData, want: "DOVIWithHDR10Plus"},
		{name: "10.2 SDR base", compatID: 2, transfer: "bt709", want: "DOVIWithSDR"},
		{name: "10.4 HLG base", compatID: 4, transfer: "arib-std-b67", want: "DOVIWithHLG"},
		{name: "unmapped id with PQ", compatID: 6, transfer: "smpte2084", want: "DOVI"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			track := NormalizeVideoTrack(Stream{
				CodecName:     "av1",
				CodecType:     "video",
				ColorTransfer: tt.transfer,
				SideDataList: []SideData{{
					SideDataType: "DOVI configuration record", DVProfile: 10, DVLevel: 6,
					DVBLCompatID: tt.compatID, DVBLCompatIDPresent: true,
				}},
				FrameSideDataList: tt.frameData,
				FrameSideDataRead: true,
			})
			if track.VideoRangeType != tt.want {
				t.Fatalf("VideoRangeType = %q, want %q", track.VideoRangeType, tt.want)
			}
			// The HDR10+ backfill rewrites a stored track's range type; it
			// must keep the same mapping.
			wantBackfill := tt.want
			if wantBackfill == "DOVIWithHDR10" {
				wantBackfill = "DOVIWithHDR10Plus"
			}
			if got := RangeTypeWithHDR10Plus(track); got != wantBackfill {
				t.Fatalf("RangeTypeWithHDR10Plus = %q, want %q", got, wantBackfill)
			}
		})
	}
}

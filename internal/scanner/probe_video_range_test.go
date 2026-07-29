package scanner

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestProbePipelinePreservesVideoColorRange(t *testing.T) {
	tests := []struct {
		name           string
		colorRange     string
		wantColorRange string
	}{
		{name: "limited", colorRange: "tv", wantColorRange: "tv"},
		{name: "full", colorRange: "pc", wantColorRange: "pc"},
		{name: "unspecified", wantColorRange: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawJSON := `{"streams":[{"codec_type":"video","codec_name":"h264","color_range":"` + tt.colorRange + `"}]}`
			var raw ffprobeOutput
			if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
				t.Fatalf("unmarshal ffprobe output: %v", err)
			}

			probe := convertProbeData(&raw)
			file := &models.MediaFile{}
			applyProbeData(file, probe, "local")

			if len(file.VideoTracks) != 1 {
				t.Fatalf("VideoTracks length = %d, want 1", len(file.VideoTracks))
			}
			if got := file.VideoTracks[0].ColorRange; got != tt.wantColorRange {
				t.Fatalf("ColorRange = %q, want %q", got, tt.wantColorRange)
			}
		})
	}
}

func TestConvertProbeDataVideoRangeTypes(t *testing.T) {
	tests := []struct {
		name          string
		stream        ffprobeStream
		wantRange     string
		wantRangeType string
		wantProfile   int
		wantCompatID  int
		wantEL        bool
		wantHDR10Plus bool
	}{
		{
			name: "dolby vision profile 7 enhancement layer",
			stream: ffprobeStream{
				CodecType:     "video",
				CodecName:     "hevc",
				ColorTransfer: "smpte2084",
				SideDataList: []ffprobeSideData{{
					SideDataType: "DOVI configuration record",
					DVProfile:    7,
					DVElPresent:  1,
				}},
			},
			wantRange:     "DolbyVision",
			wantRangeType: "DOVIWithEL",
			wantProfile:   7,
			wantEL:        true,
		},
		{
			name: "dolby vision profile 7 with hdr10 plus",
			stream: ffprobeStream{
				CodecType: "video",
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{
					{SideDataType: "DOVI configuration record", DVProfile: 7, DVElPresent: 1},
					{SideDataType: "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"},
				},
			},
			wantRange:     "DolbyVision",
			wantRangeType: "DOVIWithELHDR10Plus",
			wantProfile:   7,
			wantEL:        true,
			wantHDR10Plus: true,
		},
		{
			name: "dolby vision profile 8 hdr10 base layer",
			stream: ffprobeStream{
				CodecType: "video",
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{{
					SideDataType: "DOVI configuration record",
					DVProfile:    8,
					DVBLCompatID: 1,
				}},
			},
			wantRange:     "DolbyVision",
			wantRangeType: "DOVIWithHDR10",
			wantProfile:   8,
			wantCompatID:  1,
		},
		{
			name: "dolby vision profile 8 hlg base layer",
			stream: ffprobeStream{
				CodecType: "video",
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{{
					SideDataType: "DOVI configuration record",
					DVProfile:    8,
					DVBLCompatID: 4,
				}},
			},
			wantRange:     "DolbyVision",
			wantRangeType: "DOVIWithHLG",
			wantProfile:   8,
			wantCompatID:  4,
		},
		{
			name: "hdr10 plus",
			stream: ffprobeStream{
				CodecType:     "video",
				CodecName:     "hevc",
				ColorTransfer: "smpte2084",
				SideDataList:  []ffprobeSideData{{SideDataType: "HDR10+ metadata"}},
			},
			wantRange:     "HDR",
			wantRangeType: "HDR10Plus",
			wantHDR10Plus: true,
		},
		{
			name: "hlg",
			stream: ffprobeStream{
				CodecType:     "video",
				CodecName:     "hevc",
				ColorTransfer: "arib-std-b67",
			},
			wantRange:     "HDR",
			wantRangeType: "HLG",
		},
		{
			name: "hdr10",
			stream: ffprobeStream{
				CodecType:     "video",
				CodecName:     "hevc",
				ColorTransfer: "smpte2084",
			},
			wantRange:     "HDR",
			wantRangeType: "HDR10",
		},
		{
			name: "sdr",
			stream: ffprobeStream{
				CodecType: "video",
				CodecName: "h264",
			},
			wantRangeType: "SDR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertProbeData(&ffprobeOutput{
				Streams: []ffprobeStream{tt.stream},
			})
			if len(got.VideoTracks) != 1 {
				t.Fatalf("VideoTracks length = %d, want 1", len(got.VideoTracks))
			}
			track := got.VideoTracks[0]
			if track.VideoRange != tt.wantRange {
				t.Fatalf("VideoRange = %q, want %q", track.VideoRange, tt.wantRange)
			}
			if track.VideoRangeType != tt.wantRangeType {
				t.Fatalf("VideoRangeType = %q, want %q", track.VideoRangeType, tt.wantRangeType)
			}
			if track.DVProfile != tt.wantProfile {
				t.Fatalf("DVProfile = %d, want %d", track.DVProfile, tt.wantProfile)
			}
			if track.DVBLCompatID != tt.wantCompatID {
				t.Fatalf("DVBLCompatID = %d, want %d", track.DVBLCompatID, tt.wantCompatID)
			}
			if track.DVELPresent != tt.wantEL {
				t.Fatalf("DVELPresent = %v, want %v", track.DVELPresent, tt.wantEL)
			}
			if track.HDR10Plus != tt.wantHDR10Plus {
				t.Fatalf("HDR10Plus = %v, want %v", track.HDR10Plus, tt.wantHDR10Plus)
			}
		})
	}
}

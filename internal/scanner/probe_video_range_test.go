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

// TestProbePipelinePreservesJellyfinFFprobeDolbyVisionPresenceFlags verifies explicit side-data flags survive probing.
func TestProbePipelinePreservesJellyfinFFprobeDolbyVisionPresenceFlags(t *testing.T) {
	for _, fields := range []string{
		`"rpu_present_flag":1,"el_present_flag":1,"bl_present_flag":1`,
		`"dv_rpu_present":1,"dv_el_present":1,"dv_bl_present":1`,
	} {
		rawJSON := `{"streams":[{"codec_type":"video","codec_name":"hevc","color_range":"tv","color_space":"bt2020nc","color_transfer":"smpte2084","color_primaries":"bt2020","side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":7,"dv_level":6,` + fields + `,"dv_bl_signal_compatibility_id":6}]}]}`
		var raw ffprobeOutput
		if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
			t.Fatal(err)
		}
		probe := convertProbeData(&raw)
		file := &models.MediaFile{}
		applyProbeData(file, probe, "local")
		if len(file.VideoTracks) != 1 {
			t.Fatalf("VideoTracks length = %d, want 1", len(file.VideoTracks))
		}
		track := file.VideoTracks[0]
		if track.DVProfile != 7 || track.DVLevel != 6 || track.DVBLCompatID != 6 || !track.DVConfigPresent || !track.DVBLCompatIDPresent || !track.DVBLPresent || !track.DVRPUPresent || !track.DVELPresent {
			t.Fatalf("Dolby Vision facts were not preserved: %#v", track)
		}
	}
}

// TestProbePipelineDistinguishesMissingAndExplicitZeroCompatibilityID verifies absent and zero values remain distinct.
func TestProbePipelineDistinguishesMissingAndExplicitZeroCompatibilityID(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		present bool
	}{
		{name: "missing"},
		{name: "explicit zero", field: `,"dv_bl_signal_compatibility_id":0`, present: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawJSON := `{"streams":[{"codec_type":"video","codec_name":"hevc","side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":8` + tt.field + `}]}]}`
			var raw ffprobeOutput
			if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
				t.Fatal(err)
			}
			track := convertProbeData(&raw).VideoTracks[0]
			if track.DVBLCompatIDPresent != tt.present {
				t.Fatalf("DVBLCompatIDPresent = %t, want %t", track.DVBLCompatIDPresent, tt.present)
			}
		})
	}
}

// TestConvertProbeDataVideoRangeTypes verifies FFprobe dynamic-range fields map into catalog tracks.
func TestConvertProbeDataVideoRangeTypes(t *testing.T) {
	tests := []struct {
		name          string
		stream        ffprobeStream
		wantRange     string
		wantRangeType string
		wantProfile   int
		wantLevel     int
		wantCompatID  int
		wantConfig    bool
		wantCompat    bool
		wantBL        bool
		wantRPU       bool
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
			wantConfig:    true,
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
			wantConfig:    true,
		},
		{
			name: "dolby vision profile 8 hdr10 base layer",
			stream: ffprobeStream{
				CodecType: "video",
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{{
					SideDataType: "DOVI configuration record",
					DVProfile:    8,
					DVLevel:      6,
					DVBLCompatID: 1,
				}},
			},
			wantRange:     "DolbyVision",
			wantRangeType: "DOVIWithHDR10",
			wantProfile:   8,
			wantLevel:     6,
			wantCompatID:  1,
			wantConfig:    true,
			wantCompat:    true,
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
			wantConfig:    true,
			wantCompat:    true,
		},
		{
			name: "uses one dolby vision configuration record consistently",
			stream: ffprobeStream{
				CodecType: "video",
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{
					{SideDataType: "DOVI configuration record", DVProfile: 8, DVLevel: 6, DVBLCompatID: 1},
					{SideDataType: "DOVI configuration record", DVProfile: 7, DVElPresent: 1},
				},
			},
			wantRange:     "DolbyVision",
			wantRangeType: "DOVIWithHDR10",
			wantProfile:   8,
			wantLevel:     6,
			wantCompatID:  1,
			wantConfig:    true,
			wantCompat:    true,
		},
		{
			name: "dolby vision profile 7 uhd bluray fallback",
			stream: ffprobeStream{
				CodecType: "video", CodecName: "hevc", ColorRange: "tv",
				ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
				SideDataList: []ffprobeSideData{{
					SideDataType: "DOVI configuration record", DVProfile: 7, DVLevel: 6,
					DVBlPresent: 1, DVElPresent: 1, DVRPUPresent: 1, DVBLCompatID: 6,
				}},
			},
			wantRange: "DolbyVision", wantRangeType: "DOVIWithEL",
			wantProfile: 7, wantLevel: 6, wantCompatID: 6, wantConfig: true, wantCompat: true, wantBL: true, wantRPU: true, wantEL: true,
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
			if track.DVLevel != tt.wantLevel {
				t.Fatalf("DVLevel = %d, want %d", track.DVLevel, tt.wantLevel)
			}
			if track.DVBLCompatID != tt.wantCompatID {
				t.Fatalf("DVBLCompatID = %d, want %d", track.DVBLCompatID, tt.wantCompatID)
			}
			if track.DVConfigPresent != tt.wantConfig || track.DVBLCompatIDPresent != tt.wantCompat || track.DVBLPresent != tt.wantBL || track.DVRPUPresent != tt.wantRPU {
				t.Fatalf("Dolby presence flags = config:%t compat:%t bl:%t rpu:%t, want config:%t compat:%t bl:%t rpu:%t", track.DVConfigPresent, track.DVBLCompatIDPresent, track.DVBLPresent, track.DVRPUPresent, tt.wantConfig, tt.wantCompat, tt.wantBL, tt.wantRPU)
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

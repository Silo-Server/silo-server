package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDetailedVideoEligibilityDoesNotRejectHardwareForReportedBitrate(t *testing.T) {
	file := &models.MediaFile{
		ID:        42,
		Container: "mkv",
		VideoTracks: []models.VideoTrack{{
			Codec:         "hevc",
			Profile:       "Main 10",
			Level:         153,
			BitDepth:      10,
			Width:         3840,
			Height:        2160,
			FrameRate:     "23.976",
			Bitrate:       120_000_000,
			VideoRange:    "HDR",
			ColorTransfer: "smpte2084",
		}},
	}

	request := validStartRequestV3()
	request.ClientPlaybackContext.Features = append(request.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{
		Codec:          "hevc",
		Profiles:       []string{"main 10"},
		Levels:         []int{153},
		BitDepths:      []int{10},
		MaxWidth:       3840,
		MaxHeight:      2160,
		MaxFrameRate:   60,
		MaxBitrateKbps: 80_000,
		Hardware:       true,
	}}

	source := SourceDescriptorFromFileV3(file, -1)
	if !detailedVideoEligibleV3(source, request) {
		t.Fatal("hardware HDR decode should not be rejected because of reported bitrate")
	}
}

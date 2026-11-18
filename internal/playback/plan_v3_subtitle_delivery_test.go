package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveSubtitlePolicyV3PreservesTextThroughSidecarOnlyRenderer(t *testing.T) {
	file := detailedFixtureFileV3()
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Codec: "subrip"}}
	request := validStartRequestV3()
	request.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
	request.SubtitleTrackIndex = new(0)
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Subtitles: DeliverySubtitleCapabilitiesV3{SidecarText: true},
	}
	result := ResolveSubtitlePolicyV3(file, request, false, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	if result.Terminal != nil || result.Decision.Mode != SubtitleRenderV3 || result.Decision.Embedded != nil {
		t.Fatalf("a text sidecar renderer must not require embedded decoding support: %#v", result)
	}
}

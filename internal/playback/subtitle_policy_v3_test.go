package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveSubtitlePolicyV3RendersCEA608AsTextArtifact(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "eia_608"}}
	req := validStartRequestV3()
	index := 0
	req.SubtitleTrackIndex = &index

	result := ResolveSubtitlePolicyV3(file, req, true, nil)

	if result.Terminal != nil || result.RequiresBurn || result.Decision.Mode != SubtitleRenderV3 {
		t.Fatalf("CEA-608 should render as a client-styled text artifact: %#v", result)
	}
	if result.Claims.Reason != "client_render_supported" || result.Claims.BitmapOverlay {
		t.Fatalf("CEA-608 claims = %#v", result.Claims)
	}
}

func TestResolveSubtitlePolicyV3DoesNotOfferDVBTeletextAsClientBitmap(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "dvb_teletext"}}
	req := validStartRequestV3()
	index := 0
	req.SubtitleTrackIndex = &index

	result := ResolveSubtitlePolicyV3(file, req, true, nil)

	if result.Terminal != nil || !result.RequiresBurn || result.Decision.Mode != SubtitleBurnInV3 {
		t.Fatalf("DVB teletext must stay on the server fallback: %#v", result)
	}
	if !result.Claims.BitmapOverlay {
		t.Fatalf("DVB teletext burn-in must retain bitmap overlay semantics: %#v", result.Claims)
	}
}

func TestClientRenderableBitmapSubtitleV3UsesExactCodecFamilies(t *testing.T) {
	for _, codec := range []string{"pgs", "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "vobsub"} {
		if !isClientRenderableBitmapSubtitleV3(codec) {
			t.Errorf("expected client-renderable bitmap codec: %s", codec)
		}
	}
	for _, codec := range []string{"dvb_teletext", "hdmv_text_subtitle", "arib_caption", "eia_608"} {
		if isClientRenderableBitmapSubtitleV3(codec) {
			t.Errorf("must not advertise unsupported client bitmap codec: %s", codec)
		}
	}
}

package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// subtitlePolicyForTrackV3 selects the only subtitle track, of codec, on the
// fixture file (or an ordinal past it) and resolves the original_http
// subtitle policy with or without transcoding allowed.
func subtitlePolicyForTrackV3(codec string, index int) func(transcodeAllowed bool) SubtitlePolicyResultV3 {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: codec}}
	req := validStartRequestV3()
	req.SubtitleTrackIndex = &index
	return func(transcodeAllowed bool) SubtitlePolicyResultV3 {
		return ResolveSubtitlePolicyV3(file, req, transcodeAllowed, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	}
}

func TestResolveSubtitlePolicyV3UnknownCodecIsExplicitlyUnsupported(t *testing.T) {
	for codec, message := range map[string]string{
		"arib_caption": "Subtitle format arib_caption has no validated rendering or burn-in route.",
		// A format the probe couldn't name has no name to give.
		"": "The subtitle track's format was not recognized, so it has no validated rendering or burn-in route.",
	} {
		resolve := subtitlePolicyForTrackV3(codec, 0)
		for _, transcodeAllowed := range []bool{true, false} {
			result := resolve(transcodeAllowed)
			if result.Terminal == nil || result.Terminal.Reason != "subtitle_codec_unsupported" || result.Terminal.Message != message {
				t.Fatalf("codec %q, transcodeAllowed=%v: unknown codec must be explicitly unsupported, got %#v", codec, transcodeAllowed, result.Terminal)
			}
		}
	}
}

// The inventory marks exactly the tracks the policy refuses as unsupported,
// so a client can list them as unavailable instead of offering them.
func TestSubtitleInventoryV3MarksTracksWithoutARouteUnplayable(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = []models.ExternalSubtitle{{Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 2, Codec: ""}, {Index: 3, Codec: "webvtt", IndexOnly: true}, {Index: 4, Codec: "dvd_subtitle"}, {Index: 5, Codec: "arib_caption"}}
	want := []bool{false, true, false, false, true}
	items := BuildSubtitleInventoryV3(file, nil)
	for i, item := range items {
		if item.Unplayable != want[i] {
			t.Errorf("track %d (%s %q): unplayable = %v, want %v", i, item.Source, item.Codec, item.Unplayable, want[i])
		}
	}
	if len(items) != len(want) {
		t.Fatalf("inventory has %d tracks, want %d", len(items), len(want))
	}
}

// Malformed selections stay terminal: a bad identity is a client bug, and
// swallowing it would make that bug harder to find.
func TestResolveSubtitlePolicyV3StillRejectsInvalidIdentity(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.SubtitleTrackID = "not-a-track-id"

	result := ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	if result.Terminal == nil || result.Terminal.Reason != "subtitle_track_invalid" {
		t.Fatalf("a malformed identity must stay terminal: %#v", result)
	}
}

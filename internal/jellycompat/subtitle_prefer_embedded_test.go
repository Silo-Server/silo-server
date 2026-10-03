package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// embedded and external are the two source classes playback.prefer_embedded_subtitles
// compares. Downloaded rows are a third source and are not embedded, so the
// preference leaves them below an embedded track.
func embeddedTrack(index int, language string) compatSubtitleCandidate {
	return compatSubtitleCandidate{Index: index, Language: language, Source: playback.SubtitleSourceEmbeddedV3}
}

func externalTrack(index int, language string) compatSubtitleCandidate {
	return compatSubtitleCandidate{Index: index, Language: language, External: true, Source: playback.SubtitleSourceExternalV3}
}

func downloadedTrack(index int, language string) compatSubtitleCandidate {
	return compatSubtitleCandidate{Index: index, Language: language, External: true, Source: playback.SubtitleSourceDownloadedV3}
}

// TestCompatDefaultSubtitleStreamIndexPreferEmbedded pins what
// playback.prefer_embedded_subtitles does to the Jellyfin 12.1 ranking: it
// moves the source comparison from the front of the key list to a tie-break
// after the language and default keys, and inverts its direction. Everything
// else keeps its relative order, so an embedded track only wins when the
// viewer's language and the track class already match.
func TestCompatDefaultSubtitleStreamIndexPreferEmbedded(t *testing.T) {
	cases := []struct {
		name       string
		candidates []compatSubtitleCandidate
		preferred  []string
		mode       string
		audio      string
		want       int // -1 means none
	}{
		{
			"embedded beats an external sidecar in the same language",
			[]compatSubtitleCandidate{externalTrack(5, "eng"), embeddedTrack(9, "eng")},
			[]string{"en"}, compatSubtitleAlways, "en",
			9,
		},
		{
			"language still outranks the source preference",
			[]compatSubtitleCandidate{externalTrack(5, "eng"), embeddedTrack(9, "fre")},
			[]string{"en"}, compatSubtitleAlways, "en",
			5,
		},
		{
			"a full track still beats a forced one",
			[]compatSubtitleCandidate{{Index: 9, Language: "eng", Forced: true, Source: playback.SubtitleSourceEmbeddedV3}, externalTrack(5, "eng")},
			[]string{"en"}, compatSubtitleAlways, "en",
			5,
		},
		{
			"the container default flag still outranks the source preference",
			[]compatSubtitleCandidate{{Index: 5, Language: "eng", External: true, Default: true, Source: playback.SubtitleSourceExternalV3}, embeddedTrack(9, "eng")},
			[]string{"en"}, compatSubtitleAlways, "en",
			5,
		},
		{
			"an embedded track beats a downloaded one",
			[]compatSubtitleCandidate{downloadedTrack(5, "eng"), embeddedTrack(9, "eng")},
			[]string{"en"}, compatSubtitleAlways, "en",
			9,
		},
		{
			"no embedded track falls back to the Jellyfin default rule",
			[]compatSubtitleCandidate{downloadedTrack(5, "fre"), {Index: 7, Language: "de", External: true, Default: true, Source: playback.SubtitleSourceExternalV3}},
			[]string{"en"}, compatSubtitleDefault, "",
			7,
		},
		{
			"Default mode picks the embedded track over a plainly flagged external",
			[]compatSubtitleCandidate{externalTrack(5, "eng"), embeddedTrack(9, "eng")},
			[]string{"en"}, compatSubtitleDefault, "",
			9,
		},
		{
			"Smart mode picks the embedded track for foreign audio",
			[]compatSubtitleCandidate{externalTrack(5, "eng"), embeddedTrack(9, "eng")},
			[]string{"en"}, compatSubtitleSmart, "ja",
			9,
		},
		{
			"Smart mode leaves subtitles off for preferred-language audio",
			[]compatSubtitleCandidate{externalTrack(5, "eng"), embeddedTrack(9, "eng")},
			[]string{"en"}, compatSubtitleSmart, "en",
			-1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compatDefaultSubtitleStreamIndex(tc.candidates, tc.preferred, tc.mode, tc.audio, true)
			if tc.want < 0 {
				if got != nil {
					t.Fatalf("got %d, want none", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("got %v, want %d", got, tc.want)
			}
		})
	}
}

// The same candidates must keep the Jellyfin answer while the preference is
// off, which is the default for every profile.
func TestCompatDefaultSubtitleStreamIndexPreferEmbeddedOffKeepsJellyfinOrder(t *testing.T) {
	candidates := []compatSubtitleCandidate{embeddedTrack(9, "eng"), externalTrack(5, "eng")}
	if got := compatDefaultSubtitleStreamIndex(candidates, []string{"en"}, compatSubtitleAlways, "en", false); got == nil || *got != 5 {
		t.Fatalf("preference off = %v, want the external sidecar at 5", got)
	}
}

// The profile's choice has to survive the trip from the resolved item detail
// into the compat selection, which is the only place the server picks a
// subtitle itself.
func TestCompatDetailSubtitleStreamIndexUsesTheProfilePreference(t *testing.T) {
	version := catalog.FileVersion{
		FileID:      42,
		Container:   "mkv",
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Language: "en", Default: true}},
		SubtitleTracks: []catalog.VersionSubtitleTrack{
			{Index: 2, Codec: "subrip", Language: "en"},
			{Index: 5, Codec: "srt", Language: "en", External: true},
		},
	}
	newDetail := func(preferEmbedded bool) *upstreamItemDetail {
		return &upstreamItemDetail{
			ContentID:               "item-1",
			Type:                    "movie",
			Versions:                []catalog.FileVersion{version},
			SubtitleLanguage:        "en",
			SubtitleMode:            "always",
			SubtitleModeSet:         true,
			ShowForcedSubtitles:     true,
			PreferEmbeddedSubtitles: preferEmbedded,
		}
	}
	for _, tc := range []struct {
		name           string
		preferEmbedded bool
		want           int
	}{
		{"preference off keeps Jellyfin's external-first order", false, 5},
		{"preference on starts on the embedded track", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := compatDetailSubtitleStreamIndex(newDetail(tc.preferEmbedded), version, nil, "", nil)
			if got == nil || *got != tc.want {
				t.Fatalf("got %v, want %d", got, tc.want)
			}
		})
	}
}

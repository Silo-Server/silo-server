package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCompatAudioSpatialFormat(t *testing.T) {
	cases := []struct {
		profile string
		want    string
	}{
		{"Dolby Digital Plus + Dolby Atmos", "DolbyAtmos"},
		{"TrueHD + Dolby Atmos", "DolbyAtmos"},
		{"dolby atmos", "DolbyAtmos"},
		{"DTS-HD MA + DTS:X", "DTSX"},
		{"dts:x", "DTSX"},
		{"DTS-HD MA", "None"},
		{"LC", "None"},
		{"", "None"},
	}
	for _, tc := range cases {
		if got := compatAudioSpatialFormat(tc.profile); got != tc.want {
			t.Errorf("compatAudioSpatialFormat(%q) = %q, want %q", tc.profile, got, tc.want)
		}
	}
}

func TestBuildMediaStreamsCarriesAudioProfile(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks: []models.VideoTrack{{Codec: "hevc"}},
		AudioTracks: []models.AudioTrack{
			{
				Codec:    "eac3",
				Profile:  "Dolby Digital Plus + Dolby Atmos",
				Language: "eng",
				Channels: 6,
				Default:  true,
			},
			{
				Codec:    "dts",
				Profile:  "DTS-HD MA + DTS:X",
				Language: "eng",
				Channels: 8,
			},
			{
				Codec:    "aac",
				Profile:  "LC",
				Language: "eng",
				Channels: 2,
			},
		},
	}

	streams := buildMediaStreams("item", "source", version)
	if len(streams) != 4 {
		t.Fatalf("streams length = %d, want 4", len(streams))
	}

	atmos := streams[1]
	if atmos.Profile != "Dolby Digital Plus + Dolby Atmos" {
		t.Fatalf("atmos Profile = %q", atmos.Profile)
	}
	if atmos.AudioSpatialFormat != "DolbyAtmos" {
		t.Fatalf("atmos AudioSpatialFormat = %q, want DolbyAtmos", atmos.AudioSpatialFormat)
	}
	if atmos.DisplayTitle != "English - Dolby Digital Plus + Dolby Atmos 5.1" {
		t.Fatalf("atmos DisplayTitle = %q", atmos.DisplayTitle)
	}

	dtsx := streams[2]
	if dtsx.Profile != "DTS-HD MA + DTS:X" {
		t.Fatalf("dtsx Profile = %q", dtsx.Profile)
	}
	if dtsx.AudioSpatialFormat != "DTSX" {
		t.Fatalf("dtsx AudioSpatialFormat = %q, want DTSX", dtsx.AudioSpatialFormat)
	}

	// The AAC "LC" profile is uninformative: it is carried on the stream but
	// must not replace the codec name in the display title, and it is not a
	// spatial format.
	aac := streams[3]
	if aac.Profile != "LC" {
		t.Fatalf("aac Profile = %q", aac.Profile)
	}
	if aac.AudioSpatialFormat != "None" {
		t.Fatalf("aac AudioSpatialFormat = %q, want None", aac.AudioSpatialFormat)
	}
	if aac.DisplayTitle != "English - AAC Stereo" {
		t.Fatalf("aac DisplayTitle = %q", aac.DisplayTitle)
	}
}

func TestAudioTrackDisplayTitleWithoutProfileKeepsCodecName(t *testing.T) {
	title := audioTrackDisplayTitle(models.AudioTrack{Codec: "eac3", Language: "eng", Channels: 6})
	if title != "English - EAC3 5.1" {
		t.Fatalf("DisplayTitle = %q", title)
	}
}

func TestBuildMediaStreamsJellyfin12LocalizedLabels(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks:    []models.VideoTrack{{Codec: "h264"}},
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Language: "ja", Default: true}, {Codec: "ac3"}},
		SubtitleTracks: []catalog.VersionSubtitleTrack{{Codec: "subrip", Language: "en"}},
	}
	streams := buildMediaStreams("item", "source", version)
	if len(streams) != 4 {
		t.Fatalf("streams length = %d, want 4", len(streams))
	}
	if streams[0].LocalizedLanguage != "" || streams[0].LocalizedOriginal != "" {
		t.Fatalf("video stream carries audio labels: %+v", streams[0])
	}
	if streams[1].LocalizedLanguage != compatLanguageName("ja") || streams[1].LocalizedOriginal != "Original" {
		t.Fatalf("audio labels = %q/%q", streams[1].LocalizedLanguage, streams[1].LocalizedOriginal)
	}
	if streams[2].LocalizedLanguage != "" || streams[2].LocalizedOriginal != "Original" {
		t.Fatalf("an audio stream without a language has no LocalizedLanguage: %+v", streams[2])
	}
	if streams[3].LocalizedLanguage != compatLanguageName("en") || streams[3].LocalizedOriginal != "" {
		t.Fatalf("subtitle labels = %q/%q", streams[3].LocalizedLanguage, streams[3].LocalizedOriginal)
	}
}

// The catalog's per-viewer audio choice (language preference, original
// language, remembered series track) drives DefaultAudioStreamIndex.
func TestDefaultAudioStreamIndexUsesEffectiveAudioTrack(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Language: "en", Default: true}, {Codec: "aac", Language: "ja"}},
	}
	if got := defaultAudioStreamIndex(version); got == nil || *got != 1 {
		t.Fatalf("without a viewer choice the file default wins: %v", got)
	}
	version.EffectiveAudioTrackIndex = intPtr(1)
	if got := defaultAudioStreamIndex(version); got == nil || *got != 2 {
		t.Fatalf("effective track 1 = stream %v, want 2", got)
	}
	version.EffectiveAudioTrackIndex = intPtr(9)
	if got := defaultAudioStreamIndex(version); got == nil || *got != 1 {
		t.Fatalf("an out-of-range choice falls back to the file default: %v", got)
	}
}

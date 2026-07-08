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

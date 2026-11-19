package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuildMediaStreamsCarriesAudioFacts(t *testing.T) {
	version := catalog.FileVersion{
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Bitrate: 20_000}},
		AudioTracks: []models.AudioTrack{
			{
				Codec:    "eac3",
				Profile:  "Dolby Digital Plus + Dolby Atmos",
				Language: "eng",
				Layout:   "5.1(side)",
				Channels: 6,
				Bitrate:  768,
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

	// Silo stores kbps; Jellyfin's BitRate is bits per second.
	if streams[0].BitRate != 20_000_000 {
		t.Fatalf("video BitRate = %d, want 20000000", streams[0].BitRate)
	}

	atmos := streams[1]
	// Jellyfin trims ffprobe's layout variant: "5.1(side)" is reported as "5.1".
	if atmos.ChannelLayout != "5.1" {
		t.Fatalf("atmos ChannelLayout = %q, want 5.1", atmos.ChannelLayout)
	}
	if atmos.BitRate != 768_000 {
		t.Fatalf("atmos BitRate = %d, want 768000", atmos.BitRate)
	}
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
	if streams[1].LocalizedLanguage != "Japanese" || streams[1].LocalizedOriginal != "Original" {
		t.Fatalf("audio labels = %q/%q", streams[1].LocalizedLanguage, streams[1].LocalizedOriginal)
	}
	if streams[2].LocalizedLanguage != "" || streams[2].LocalizedOriginal != "Original" {
		t.Fatalf("an audio stream without a language has no LocalizedLanguage: %+v", streams[2])
	}
	if streams[3].LocalizedLanguage != "English" || streams[3].LocalizedOriginal != "" {
		t.Fatalf("subtitle labels = %q/%q", streams[3].LocalizedLanguage, streams[3].LocalizedOriginal)
	}
}

package scanner

import "testing"

func TestConvertProbeDataCapturesAudioProfile(t *testing.T) {
	probe := convertProbeData(&ffprobeOutput{
		Streams: []ffprobeStream{
			{
				CodecType:        "audio",
				CodecName:        "eac3",
				CodecLongName:    "E-AC-3",
				Profile:          "Dolby Digital Plus + Dolby Atmos",
				ChannelLayout:    "5.1(side)",
				Channels:         6,
				BitRate:          "768000",
				SampleRate:       "48000",
				BitsPerRawSample: "24",
				Disposition:      ffprobeDisp{Default: 1},
				Tags:             map[string]string{"language": "eng", "title": "Main Audio"},
			},
		},
	})

	if len(probe.AudioTracks) != 1 {
		t.Fatalf("audio tracks = %d, want 1", len(probe.AudioTracks))
	}
	track := probe.AudioTracks[0]
	if track.Profile != "Dolby Digital Plus + Dolby Atmos" {
		t.Fatalf("profile = %q, want Dolby Digital Plus + Dolby Atmos", track.Profile)
	}
	if track.Codec != "eac3" {
		t.Fatalf("codec = %q, want eac3", track.Codec)
	}
	if !track.Default {
		t.Fatal("expected default audio track")
	}
}

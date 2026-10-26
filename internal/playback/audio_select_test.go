package playback_test

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestSelectAudioTrack(t *testing.T) {
	pref := func(index int, language string) *playback.AudioTrackPreference {
		return &playback.AudioTrackPreference{AudioTrackIndex: index, AudioLanguage: language}
	}
	mixed := []models.AudioTrack{
		{Language: "en", Codec: "aac", Default: true, Channels: 2},
		{Language: "ja", Codec: "aac", Channels: 2},
		{Language: "ja", Codec: "flac", Channels: 6, Layout: "5.1"},
		{Language: "de", Codec: "aac", Channels: 2},
	}
	regional := []models.AudioTrack{{Language: "en-GB"}, {Language: "en"}, {Language: "en-US"}}
	// Real-world FFmpeg track languages use 3-letter ISO 639-2 codes.
	iso639 := []models.AudioTrack{
		{Language: "spa", Codec: "aac", Default: true, Channels: 2},
		{Language: "eng", Codec: "aac", Channels: 6, Layout: "5.1"},
		{Language: "jpn", Codec: "flac", Channels: 2},
	}
	// Preference saved before regional subtags were preserved: bare "en" for
	// the track at index 1. The saved index must still win over the bare
	// "en" commentary track at index 0.
	regionalVariant := []models.AudioTrack{
		{Language: "en", Codec: "aac", Channels: 2, Title: "Commentary"},
		{Language: "en-US", Codec: "eac3", Channels: 6, Title: "English 5.1", Default: true},
	}
	regionalSignatures := []models.AudioTrack{
		{Language: "en-GB", Codec: "eac3", Channels: 6, Layout: "5.1", Title: "English 5.1"},
		{Language: "en-US", Codec: "eac3", Channels: 6, Layout: "5.1", Title: "English 5.1"},
	}
	regionalSignature := func(language string) *playback.AudioTrackPreference {
		return &playback.AudioTrackPreference{AudioTrackIndex: 0, AudioLanguage: language, TrackSignature: &userstore.AudioTrackSignature{Language: language, Title: "English 5.1", Codec: "eac3", Layout: "5.1", Channels: 6}}
	}
	undecodable := undecodableFirstAudioTracks()
	undecodableSignature := func(track int) *playback.AudioTrackPreference {
		return &playback.AudioTrackPreference{AudioTrackIndex: 0, AudioLanguage: "zh", TrackSignature: playback.AudioTrackSignatureFromTrack(undecodable[track])}
	}

	tests := []struct {
		name          string
		tracks        []models.AudioTrack
		preferredLang string
		seriesPref    *playback.AudioTrackPreference
		want          int
	}{
		{name: "series pref index+lang match", tracks: mixed, preferredLang: "en", seriesPref: pref(2, "ja"), want: 2},
		{name: "series pref index out of bounds falls back to lang", tracks: mixed, preferredLang: "en", seriesPref: pref(99, "ja"), want: 1},
		{name: "series pref index wrong lang falls back to lang match", tracks: mixed, preferredLang: "en", seriesPref: pref(0, "ja"), want: 1},
		{name: "no series pref uses profile language", tracks: mixed, preferredLang: "de", want: 3},
		{name: "no matching language uses default track", tracks: mixed, preferredLang: "fr", want: 0},
		{name: "no tracks", preferredLang: "en", want: 0},
		{name: "exact regional tag", tracks: regional, preferredLang: "en-US", want: 2},
		{name: "other region falls back to the generic tag", tracks: regional, preferredLang: "en-AU", want: 1},
		{name: "no default falls to first", tracks: []models.AudioTrack{{Language: "ja", Codec: "aac"}, {Language: "en", Codec: "aac"}}, preferredLang: "fr", want: 0},
		// 2-letter profile preferences match 3-letter FFmpeg track codes and vice versa.
		{name: "2-letter en matches 3-letter eng", tracks: iso639, preferredLang: "en", want: 1},
		{name: "3-letter eng matches 3-letter eng", tracks: iso639, preferredLang: "eng", want: 1},
		// Series pref stored with 3-letter code (from track), profile uses 2-letter.
		{name: "series pref jpn at index 2", tracks: iso639, preferredLang: "en", seriesPref: pref(2, "jpn"), want: 2},
		{name: "series pref 2-letter ja language fallback", tracks: iso639, preferredLang: "en", seriesPref: pref(99, "ja"), want: 2},
		{name: "series pref index keeps the regional variant", tracks: regionalVariant, seriesPref: pref(1, "en"), want: 1},
		// A regional signature must not settle for the earlier variant.
		{name: "en-US signature", tracks: regionalSignatures, seriesPref: regionalSignature("en-US"), want: 1},
		// A legacy bare-language signature still matches a regional track.
		{name: "bare en signature", tracks: regionalSignatures, seriesPref: regionalSignature("en"), want: 0},
		{name: "exact track signature over index fallback", tracks: []models.AudioTrack{
			{Language: "eng", Codec: "aac", Channels: 2, Layout: "stereo", Title: "English Stereo", Default: true},
			{Language: "eng", Codec: "flac", Channels: 6, Layout: "5.1", Title: "English 5.1"},
		}, preferredLang: "en", seriesPref: &playback.AudioTrackPreference{AudioTrackIndex: 0, AudioLanguage: "en", TrackSignature: &userstore.AudioTrackSignature{
			Language: "eng", Title: "English 5.1", Codec: "flac", Layout: "5.1", Channels: 6, Default: false,
		}}, want: 1},
		{name: "undecodable file default", tracks: undecodable, want: 1},
		{name: "undecodable: language match prefers a decodable track", tracks: undecodable, preferredLang: "zh", want: 1},
		{name: "undecodable: saved index on the undecodable track", tracks: undecodable, seriesPref: pref(0, "zh"), want: 1},
		{name: "undecodable: saved index after the undecodable track", tracks: undecodable, seriesPref: pref(2, "zh"), want: 2},
		{name: "undecodable: saved signature of the undecodable track", tracks: undecodable, seriesPref: undecodableSignature(0), want: 1},
		{name: "undecodable: saved signature of a decodable track", tracks: undecodable, seriesPref: undecodableSignature(2), want: 2},
		{name: "no decodable track keeps the container default", tracks: []models.AudioTrack{
			{Language: "zh", Codec: "unknown"},
			{Language: "en", Codec: "", Default: true},
		}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := playback.SelectAudioTrack(tt.tracks, tt.preferredLang, tt.seriesPref); got != tt.want {
				t.Fatalf("SelectAudioTrack(%q) = %d, want %d", tt.preferredLang, got, tt.want)
			}
		})
	}
}

func TestMatchAudioTrackAcrossVersions(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		requested, effective []models.AudioTrack
		index, want          int
	}{
		{"remaps a reordered language", []models.AudioTrack{
			{Language: "ja", Codec: "aac", Channels: 2, Title: "Japanese"},
			{Language: "en", Codec: "eac3", Channels: 6, Title: "English 5.1"},
		}, []models.AudioTrack{
			{Language: "en", Codec: "eac3", Channels: 6, Title: "English 5.1"},
			{Language: "ja", Codec: "aac", Channels: 2, Title: "Japanese"},
		}, 1, 0},
		{"falls back to the language across codecs", []models.AudioTrack{
			{Language: "ja", Codec: "aac", Channels: 2},
			{Language: "en", Codec: "truehd", Channels: 8},
		}, []models.AudioTrack{
			{Language: "en", Codec: "eac3", Channels: 6},
			{Language: "es", Codec: "aac", Channels: 2, Default: true},
		}, 1, 0},
		{"keeps the language when the codec changes and the order differs", []models.AudioTrack{
			{Language: "en", Codec: "ac3", Channels: 6, Default: true},
			{Language: "ja", Codec: "ac3", Channels: 6},
		}, []models.AudioTrack{
			{Language: "ja", Codec: "aac", Channels: 2},
			{Language: "en", Codec: "aac", Channels: 2, Default: true},
		}, 1, 0},
		{"keeps the language when only the channels change", []models.AudioTrack{
			{Language: "en", Codec: "aac", Channels: 2, Default: true},
			{Language: "ja", Codec: "aac", Channels: 6},
		}, []models.AudioTrack{
			{Language: "ja", Codec: "aac", Channels: 2},
			{Language: "en", Codec: "aac", Channels: 2, Default: true},
		}, 1, 0},
		{"prefers the exact regional tag", []models.AudioTrack{
			{Language: "en-GB", Codec: "eac3", Channels: 6},
			{Language: "en-US", Codec: "eac3", Channels: 6},
		}, []models.AudioTrack{
			{Language: "en-GB", Codec: "aac", Channels: 2},
			{Language: "en-US", Codec: "aac", Channels: 2},
		}, 1, 1},
		{"keeps main audio over commentary in the same language", []models.AudioTrack{
			{Language: "en", Codec: "truehd", Channels: 8, Title: "Main"},
			{Language: "en", Codec: "ac3", Channels: 2, Title: "Director's Commentary"},
		}, []models.AudioTrack{
			{Language: "en", Codec: "ac3", Channels: 2, Title: "Director's Commentary"},
			{Language: "en", Codec: "eac3", Channels: 6, Title: "Main"},
		}, 0, 1},
		{"matches an untagged track among untagged tracks", []models.AudioTrack{
			{Language: "en", Codec: "aac", Channels: 2},
			{Codec: "ac3", Channels: 6},
		}, []models.AudioTrack{
			{Codec: "aac", Channels: 2},
			{Language: "en", Codec: "aac", Channels: 2, Default: true},
		}, 1, 0},
	} {
		if got := playback.MatchAudioTrackAcrossVersions(tt.requested, tt.effective, tt.index); got != tt.want {
			t.Errorf("%s: MatchAudioTrackAcrossVersions() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestIsOriginalLanguagePreference(t *testing.T) {
	for value, want := range map[string]bool{
		"original": true, "Original": true, "x-silo-original": true, " X-Silo-Original ": true,
		"": false, "en": false, "ja": false, "x-silo-other": false,
	} {
		if got := playback.IsOriginalLanguagePreference(value); got != want {
			t.Errorf("IsOriginalLanguagePreference(%q) = %v, want %v", value, got, want)
		}
	}
}

// undecodableFirstAudioTracks mirrors an MP4 whose first audio track is AVS3
// (av3a): ffprobe's JSON names no codec for it, so the scanner stores none.
func undecodableFirstAudioTracks() []models.AudioTrack {
	return []models.AudioTrack{
		{Language: "zh", Codec: "", Channels: 6, Default: true},
		{Language: "zh", Codec: "eac3", Channels: 6, Layout: "5.1"},
		{Language: "zh", Codec: "aac", Channels: 2, Layout: "stereo"},
	}
}

func TestAudioTrackDecodable(t *testing.T) {
	tests := []struct {
		name      string
		codecs    []string
		fileCodec string
		want      []bool
	}{
		{name: "named codecs", codecs: []string{"eac3", "aac", " TrueHD "}, fileCodec: "eac3", want: []bool{true, true, true}},
		{name: "ffprobe text unknown", codecs: []string{" Unknown ", "aac"}, fileCodec: "aac", want: []bool{false, true}},
		{name: "unnamed beside a named track", codecs: []string{"", "eac3"}, fileCodec: "eac3", want: []bool{false, true}},
		{name: "no audio codec named anywhere", codecs: []string{"", ""}, want: []bool{false, false}},
		{name: "older rows without per-track codecs", codecs: []string{"", ""}, fileCodec: "aac", want: []bool{true, true}},
	}
	for _, tt := range tests {
		tracks := make([]models.AudioTrack, len(tt.codecs))
		for i, codec := range tt.codecs {
			tracks[i].Codec = codec
		}
		for i, want := range tt.want {
			if got := playback.AudioTrackDecodable(tracks, i, tt.fileCodec); got != want {
				t.Errorf("%s: AudioTrackDecodable(track %d) = %v, want %v", tt.name, i, got, want)
			}
		}
		if got, want := playback.AnyAudioTrackDecodable(tracks, tt.fileCodec), slices.Contains(tt.want, true); got != want {
			t.Errorf("%s: AnyAudioTrackDecodable = %v, want %v", tt.name, got, want)
		}
	}
}

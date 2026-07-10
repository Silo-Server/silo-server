package playback_test

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestSelectAudioTrack(t *testing.T) {
	tracks := []models.AudioTrack{
		{Language: "en", Codec: "aac", Default: true, Channels: 2},
		{Language: "ja", Codec: "aac", Channels: 2},
		{Language: "ja", Codec: "flac", Channels: 6, Layout: "5.1"},
		{Language: "de", Codec: "aac", Channels: 2},
	}

	tests := []struct {
		name           string
		preferredLang  string
		seriesPrefIdx  int
		seriesPrefLang string
		hasSeriesPref  bool
		want           int
	}{
		{
			name:          "series pref index+lang match",
			preferredLang: "en",
			seriesPrefIdx: 2, seriesPrefLang: "ja", hasSeriesPref: true,
			want: 2,
		},
		{
			name:          "series pref index out of bounds falls back to lang",
			preferredLang: "en",
			seriesPrefIdx: 99, seriesPrefLang: "ja", hasSeriesPref: true,
			want: 1,
		},
		{
			name:          "series pref index wrong lang falls back to lang match",
			preferredLang: "en",
			seriesPrefIdx: 0, seriesPrefLang: "ja", hasSeriesPref: true,
			want: 1,
		},
		{
			name:          "no series pref uses profile language",
			preferredLang: "de",
			hasSeriesPref: false,
			want:          3,
		},
		{
			name:          "no matching language uses default track",
			preferredLang: "fr",
			hasSeriesPref: false,
			want:          0,
		},
		{
			name:          "empty preferred lang uses default track",
			preferredLang: "",
			hasSeriesPref: false,
			want:          0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seriesPref *playback.AudioTrackPreference
			if tt.hasSeriesPref {
				seriesPref = &playback.AudioTrackPreference{
					AudioTrackIndex: tt.seriesPrefIdx,
					AudioLanguage:   tt.seriesPrefLang,
				}
			}
			got := playback.SelectAudioTrack(tracks, tt.preferredLang, seriesPref)
			if got != tt.want {
				t.Errorf("SelectAudioTrack() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSelectAudioTrack_NoTracks(t *testing.T) {
	got := playback.SelectAudioTrack(nil, "en", nil)
	if got != 0 {
		t.Errorf("SelectAudioTrack(nil) = %d, want 0", got)
	}
}

func TestSelectAudioTrack_NoDefaultFallsToFirst(t *testing.T) {
	tracks := []models.AudioTrack{
		{Language: "ja", Codec: "aac"},
		{Language: "en", Codec: "aac"},
	}
	got := playback.SelectAudioTrack(tracks, "fr", nil)
	if got != 0 {
		t.Errorf("SelectAudioTrack() = %d, want 0", got)
	}
}

// TestSelectAudioTrack_ISO639CrossFormat tests that 2-letter profile
// preferences match 3-letter FFmpeg track codes and vice versa.
func TestSelectAudioTrack_ISO639CrossFormat(t *testing.T) {
	// Real-world FFmpeg track languages use 3-letter ISO 639-2 codes.
	tracks := []models.AudioTrack{
		{Language: "spa", Codec: "aac", Default: true, Channels: 2},
		{Language: "eng", Codec: "aac", Channels: 6, Layout: "5.1"},
		{Language: "jpn", Codec: "flac", Channels: 2},
	}

	tests := []struct {
		name          string
		preferredLang string
		want          int
	}{
		{"2-letter en matches 3-letter eng", "en", 1},
		{"2-letter es matches 3-letter spa", "es", 0},
		{"2-letter ja matches 3-letter jpn", "ja", 2},
		{"3-letter eng matches 3-letter eng", "eng", 1},
		{"unmatched falls to default", "fr", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := playback.SelectAudioTrack(tracks, tt.preferredLang, nil)
			if got != tt.want {
				t.Errorf("SelectAudioTrack() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSelectAudioTrack_SeriesPrefCrossFormat(t *testing.T) {
	tracks := []models.AudioTrack{
		{Language: "spa", Codec: "aac", Default: true, Channels: 2},
		{Language: "eng", Codec: "aac", Channels: 6},
		{Language: "jpn", Codec: "flac", Channels: 2},
	}

	// Series pref stored with 3-letter code (from track), profile uses 2-letter.
	pref := &playback.AudioTrackPreference{
		AudioTrackIndex: 2,
		AudioLanguage:   "jpn",
	}
	got := playback.SelectAudioTrack(tracks, "en", pref)
	if got != 2 {
		t.Errorf("series pref jpn at index 2: got %d, want 2", got)
	}

	// Series pref language fallback with 2-letter stored code.
	pref2 := &playback.AudioTrackPreference{
		AudioTrackIndex: 99,   // out of bounds
		AudioLanguage:   "ja", // 2-letter
	}
	got = playback.SelectAudioTrack(tracks, "en", pref2)
	if got != 2 {
		t.Errorf("series pref ja fallback: got %d, want 2", got)
	}
}

func TestSelectAudioTrack_PrefersExactTrackSignatureOverIndexFallback(t *testing.T) {
	tracks := []models.AudioTrack{
		{Language: "eng", Codec: "aac", Channels: 2, Layout: "stereo", Title: "English Stereo", Default: true},
		{Language: "eng", Codec: "flac", Channels: 6, Layout: "5.1", Title: "English 5.1"},
	}

	pref := &playback.AudioTrackPreference{
		AudioTrackIndex: 0,
		AudioLanguage:   "en",
		TrackSignature: &userstore.AudioTrackSignature{
			Language: "eng",
			Title:    "English 5.1",
			Codec:    "flac",
			Layout:   "5.1",
			Channels: 6,
			Default:  false,
		},
	}

	got := playback.SelectAudioTrack(tracks, "en", pref)
	if got != 1 {
		t.Fatalf("SelectAudioTrack() = %d, want 1", got)
	}
}

func TestMatchAudioTrackAcrossVersionsRemapsReorderedLanguage(t *testing.T) {
	requested := []models.AudioTrack{
		{Language: "ja", Codec: "aac", Channels: 2, Title: "Japanese"},
		{Language: "en", Codec: "eac3", Channels: 6, Title: "English 5.1"},
	}
	effective := []models.AudioTrack{
		{Language: "en", Codec: "eac3", Channels: 6, Title: "English 5.1"},
		{Language: "ja", Codec: "aac", Channels: 2, Title: "Japanese"},
	}

	if got := playback.MatchAudioTrackAcrossVersions(requested, effective, 1); got != 0 {
		t.Fatalf("MatchAudioTrackAcrossVersions() = %d, want English track 0", got)
	}
}

func TestMatchAudioTrackAcrossVersionsFallsBackToLanguageAcrossCodecs(t *testing.T) {
	requested := []models.AudioTrack{
		{Language: "ja", Codec: "aac", Channels: 2},
		{Language: "en", Codec: "truehd", Channels: 8},
	}
	effective := []models.AudioTrack{
		{Language: "en", Codec: "eac3", Channels: 6},
		{Language: "es", Codec: "aac", Channels: 2, Default: true},
	}

	if got := playback.MatchAudioTrackAcrossVersions(requested, effective, 1); got != 0 {
		t.Fatalf("MatchAudioTrackAcrossVersions() = %d, want English track 0", got)
	}
}

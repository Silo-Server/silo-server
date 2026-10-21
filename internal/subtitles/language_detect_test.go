package subtitles

import (
	"slices"
	"testing"
)

func TestDetectSubtitleLanguage(t *testing.T) {
	ass := []byte(`[Script Info]
Title: Example
Language: Spanish

[Events]
Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hola
`)
	srt := []byte(`1
00:00:01,000 --> 00:00:04,000
Bonjour tout le monde, comment allez-vous aujourd'hui?

2
00:00:05,000 --> 00:00:08,000
Je suis tres heureux de vous voir ici ce soir.
`)
	cases := []struct {
		filename string
		format   SubtitleFormat
		data     []byte
		want     string
		source   LanguageDetectionSource
	}{
		{"The.Super.Mario.Galaxy.Movie.2026.720p.WEBRip.x264.AAC-[YTS.BZ]-TR.srt", FormatSRT, nil, "tr", LanguageSourceFilename},
		{"Dune.Part.Two.2024.1080p.BluRay.x265.DTS-HD.MA.5.1-EN.srt", FormatSRT, nil, "en", LanguageSourceFilename},
		{"some.movie.chs.srt", FormatSRT, nil, "zh", LanguageSourceFilename},
		{"some.movie.pt-BR.srt", FormatSRT, nil, "pt-BR", LanguageSourceFilename},
		{"some.movie.pob.srt", FormatSRT, nil, "pt-BR", LanguageSourceFilename},
		{"some.movie.pt.srt", FormatSRT, nil, "pt", LanguageSourceFilename},
		{"subtitle.ass", FormatASS, ass, "es", LanguageSourceMetadata},
		{"subtitle.srt", FormatSRT, srt, "fr", LanguageSourceContent},
	}
	for _, tc := range cases {
		detected := DetectSubtitleLanguage(tc.filename, tc.format, tc.data)
		if detected.Language != tc.want || detected.Source != tc.source {
			t.Fatalf("%q: language = %q, source = %q, want %q from %q", tc.filename, detected.Language, detected.Source, tc.want, tc.source)
		}
	}
}

func TestResolveUploadLanguage(t *testing.T) {
	for _, tc := range []struct {
		filename string
		want     string
		source   LanguageDetectionSource // empty: not checked
	}{
		{"subtitle.srt", "de", LanguageSourceManual},
		// Filename detection wins over a non-override user language.
		{"movie.ja.srt", "ja", ""},
	} {
		detected, err := ResolveUploadLanguage(tc.filename, FormatSRT, []byte("hello"), "de", false)
		if err != nil {
			t.Fatalf("ResolveUploadLanguage(%q) error = %v", tc.filename, err)
		}
		if detected.Language != tc.want || (tc.source != "" && detected.Source != tc.source) {
			t.Fatalf("%q: language = %q, source = %q, want %q", tc.filename, detected.Language, detected.Source, tc.want)
		}
	}
}

func TestManagerUploadDetectsLanguageFromFilename(t *testing.T) {
	repo := newMockSubtitleRepo()
	s3 := newMockBlobStore()
	manager := NewManager(repo, s3)

	data := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	sub, err := manager.Upload(t.Context(), UploadRequest{
		MediaFileID: 42,
		Filename:    "custom.fr.srt",
		Data:        data,
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if sub.Language != "fr" {
		t.Fatalf("language = %q, want fr", sub.Language)
	}
}

func TestNormalizeSearchLanguages(t *testing.T) {
	got, err := NormalizeSearchLanguages([]string{"ar", "ara", "Arabic", "pt-BR", "pt_br", "zh-Hant"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ar", "pt-BR", "zh-Hant"}
	if !slices.Equal(got, want) {
		t.Fatalf("NormalizeSearchLanguages() = %#v, want %#v", got, want)
	}
}

func TestNormalizeSearchLanguagesRejectsInvalidFilter(t *testing.T) {
	for _, value := range []string{"", "unknown", "und"} {
		if _, err := NormalizeSearchLanguages([]string{value}); err == nil {
			t.Errorf("accepted invalid filter %q", value)
		}
	}
}

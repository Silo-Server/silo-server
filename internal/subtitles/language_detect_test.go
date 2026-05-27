package subtitles

import "testing"

func TestDetectSubtitleLanguageFromReleaseFilename(t *testing.T) {
	cases := []struct {
		filename string
		want     string
	}{
		{
			filename: "The.Super.Mario.Galaxy.Movie.2026.720p.WEBRip.x264.AAC-[YTS.BZ]-TR.srt",
			want:     "tr",
		},
		{
			filename: "Dune.Part.Two.2024.1080p.BluRay.x265.DTS-HD.MA.5.1-EN.srt",
			want:     "en",
		},
		{
			filename: "some.movie.chs.srt",
			want:     "zh",
		},
	}
	for _, tc := range cases {
		detected := DetectSubtitleLanguage(tc.filename, FormatSRT, nil)
		if detected.Language != tc.want {
			t.Fatalf("filename %q: language = %q, source = %q, want %q", tc.filename, detected.Language, detected.Source, tc.want)
		}
		if detected.Source != LanguageSourceFilename {
			t.Fatalf("filename %q: source = %q, want filename", tc.filename, detected.Source)
		}
	}
}

func TestDetectSubtitleLanguageFromFilename(t *testing.T) {
	detected := DetectSubtitleLanguage("Movie.en.srt", FormatSRT, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"))
	if detected.Language != "en" {
		t.Fatalf("language = %q, want en", detected.Language)
	}
	if detected.Source != LanguageSourceFilename {
		t.Fatalf("source = %q, want filename", detected.Source)
	}
}

func TestDetectSubtitleLanguageFromASSMetadata(t *testing.T) {
	data := []byte(`[Script Info]
Title: Example
Language: Spanish

[Events]
Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hola
`)
	detected := DetectSubtitleLanguage("subtitle.ass", FormatASS, data)
	if detected.Language != "es" {
		t.Fatalf("language = %q, want es", detected.Language)
	}
	if detected.Source != LanguageSourceMetadata {
		t.Fatalf("source = %q, want metadata", detected.Source)
	}
}

func TestDetectSubtitleLanguageFromContent(t *testing.T) {
	data := []byte(`1
00:00:01,000 --> 00:00:04,000
Bonjour tout le monde, comment allez-vous aujourd'hui?

2
00:00:05,000 --> 00:00:08,000
Je suis tres heureux de vous voir ici ce soir.
`)
	detected := DetectSubtitleLanguage("subtitle.srt", FormatSRT, data)
	if detected.Language != "fr" {
		t.Fatalf("language = %q, want fr", detected.Language)
	}
	if detected.Source != LanguageSourceContent {
		t.Fatalf("source = %q, want content", detected.Source)
	}
}

func TestResolveUploadLanguageUsesManualFallback(t *testing.T) {
	detected, err := ResolveUploadLanguage("subtitle.srt", FormatSRT, []byte("hello"), "de", false)
	if err != nil {
		t.Fatalf("ResolveUploadLanguage() error = %v", err)
	}
	if detected.Language != "de" {
		t.Fatalf("language = %q, want de", detected.Language)
	}
	if detected.Source != LanguageSourceManual {
		t.Fatalf("source = %q, want manual", detected.Source)
	}
}

func TestResolveUploadLanguagePrefersFilename(t *testing.T) {
	detected, err := ResolveUploadLanguage("movie.ja.srt", FormatSRT, []byte("hello"), "de", false)
	if err != nil {
		t.Fatalf("ResolveUploadLanguage() error = %v", err)
	}
	if detected.Language != "ja" {
		t.Fatalf("language = %q, want ja", detected.Language)
	}
}

func TestManagerUploadDetectsLanguageFromFilename(t *testing.T) {
	repo := newMockSubtitleRepo()
	s3 := newMockS3Client()
	manager := NewManager(repo, s3, "test-bucket")

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

package subtitles

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/abadojack/whatlanggo"
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
	french := "1\r\n00:00:01,000 --> 00:00:04,000\r\nBonjour tout le monde, comment allez-vous aujourd'hui?\r\n\r\n2\r\n00:00:05,000 --> 00:00:08,000\r\nJe suis tres heureux de vous voir ici ce soir.\r\n"
	for _, tc := range []struct {
		filename string
		data     []byte
		want     string
		source   LanguageDetectionSource // empty: not checked
	}{
		{"subtitle.srt", []byte("hello"), "de", LanguageSourceManual},
		// Filename detection wins over a non-override user language.
		{"movie.ja.srt", []byte("hello"), "ja", ""},
		// UTF-16 text is read as Upload stores it, so the preview matches.
		{"subtitle.srt", utf16LEWithBOM(french), "fr", LanguageSourceContent},
	} {
		detected, err := ResolveUploadLanguage(tc.filename, FormatSRT, tc.data, "de", false)
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

// largeSubtitle repeats lines as cues in format until the file reaches size bytes.
func largeSubtitle(format SubtitleFormat, size int, lines []string) []byte {
	var b strings.Builder
	switch format {
	case FormatVTT:
		b.WriteString("WEBVTT\n\n")
	case FormatASS:
		b.WriteString("[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	}
	for i := 0; b.Len() < size; i++ {
		line := lines[i%len(lines)]
		switch format {
		case FormatVTT:
			fmt.Fprintf(&b, "00:00:%02d.000 --> 00:00:%02d.500\n%s\n\n", i%60, i%60, line)
		case FormatASS:
			fmt.Fprintf(&b, "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\an8}%s\n", line) //nolint:misspell // ASS event keyword
		default:
			fmt.Fprintf(&b, "%d\n00:00:%02d,000 --> 00:00:%02d,500\n%s\n\n", i+1, i%60, i%60, line)
		}
	}
	return []byte(b.String())
}

var englishDialogue = []string{
	"I told you we should have left the city before the storm arrived.",
	"Nobody in this village remembers what happened to the old lighthouse keeper.",
	"If you open that door again, I promise I will not come looking for you.",
	"We have three hours until sunrise, so keep your voice down and follow me.",
}

func TestContentDetectionBoundsDetectorInput(t *testing.T) {
	const size = MaxUploadSize
	inputs := map[string]struct {
		format SubtitleFormat
		data   []byte
	}{
		"srt":            {FormatSRT, largeSubtitle(FormatSRT, size, englishDialogue)},
		"vtt":            {FormatVTT, largeSubtitle(FormatVTT, size, englishDialogue)},
		"ass":            {FormatASS, largeSubtitle(FormatASS, size, englishDialogue)},
		"no newlines":    {FormatSRT, bytes.Repeat([]byte("word "), size/5)},
		"multibyte text": {FormatSRT, largeSubtitle(FormatSRT, size, []string{"日本語の字幕はここにあります。", "Ünïcödé — ✓ 🎬"})},
	}
	for name, in := range inputs {
		text := contentDetectionText(in.data, in.format)
		if len(text) > contentSampleBytes {
			t.Errorf("%s: detector input is %d bytes, want at most %d", name, len(text), contentSampleBytes)
		}
		if !utf8.ValidString(text) {
			t.Errorf("%s: detector input is not valid UTF-8", name)
		}
	}
}

// Detection cost must not grow with file size: ResolveUploadLanguage on a
// maximum-size upload allocates about as much as on a normal feature-length
// subtitle, rather than millions of times while reading the whole file.
func TestResolveUploadLanguageWorkIsBounded(t *testing.T) {
	for _, format := range []SubtitleFormat{FormatSRT, FormatVTT, FormatASS} {
		data := largeSubtitle(format, MaxUploadSize, englishDialogue)
		filename := "subtitle." + string(format)
		var detected LanguageDetection
		allocs := testing.AllocsPerRun(1, func() {
			detected, _ = ResolveUploadLanguage(filename, format, data, "", false)
		})
		if detected.Language != "en" || detected.Source != LanguageSourceContent {
			t.Fatalf("%s: detected %+v, want en from content", format, detected)
		}
		if allocs > 1_000_000 {
			t.Errorf("%s: detecting a %d byte upload made %.0f allocations, want at most 1000000", format, len(data), allocs)
		}
	}
}

func TestSampleSubtitleContentKeepsSmallFilesWhole(t *testing.T) {
	data := largeSubtitle(FormatSRT, contentSampleBytes-200, englishDialogue)
	if got := sampleSubtitleContent(data); !bytes.Equal(got, data) {
		t.Fatalf("a %d byte file was sampled; files up to %d bytes must be read whole", len(data), contentSampleBytes)
	}
}

// Sampling a large file must find the language that reading all of it finds,
// including when typesetting lines fill the start of an ASS file.
func TestSampledDetectionMatchesFullText(t *testing.T) {
	typesetting := make([]string, 0, 3000)
	for i := range 3000 {
		typesetting = append(typesetting, fmt.Sprintf("{\\p1\\pos(%d,40)}m 0 0 l %d 0 %d 60 0 60{\\p0}", i, i%400, i%400))
	}
	karaoke := largeSubtitle(FormatASS, 200<<10, typesetting)
	karaoke = append(karaoke, largeSubtitle(FormatASS, 400<<10, []string{
		"{\\k20}今日は本当に楽しかったね。また明日も一緒に遊ぼう。",
		"あの駅の前で待っているから、遅れないでください。",
		"どうしてそんなことを言うのか、私にはわからない。",
	})...)
	cases := []struct {
		name   string
		format SubtitleFormat
		data   []byte
		want   string
	}{
		{"english srt", FormatSRT, largeSubtitle(FormatSRT, 600<<10, englishDialogue), "en"},
		{"french vtt", FormatVTT, largeSubtitle(FormatVTT, 600<<10, []string{
			"Je ne sais pas pourquoi tu es revenu ici après tout ce temps.",
			"Nous devons partir avant que la police arrive à la maison.",
			"Elle m'a dit que son frère travaillait encore à la gare.",
		}), "fr"},
		{"russian srt", FormatSRT, largeSubtitle(FormatSRT, 600<<10, []string{
			"Я не понимаю, почему ты вернулся сюда после всего этого.",
			"Нам нужно уйти до того, как полиция приедет к дому.",
			"Она сказала, что её брат всё ещё работает на вокзале.",
		}), "ru"},
		{"arabic ass", FormatASS, largeSubtitle(FormatASS, 600<<10, []string{
			"لا أفهم لماذا عدت إلى هنا بعد كل هذا الوقت.",
			"يجب أن نغادر قبل أن تصل الشرطة إلى المنزل.",
			"قالت لي إن أخاها لا يزال يعمل في المحطة.",
		}), "ar"},
		{"japanese ass after typesetting", FormatASS, karaoke, "ja"},
	}
	for _, tc := range cases {
		if len(tc.data) <= contentSampleBytes {
			t.Fatalf("%s: %d byte fixture does not exercise sampling", tc.name, len(tc.data))
		}
		full := whatlanggo.Detect(extractSubtitleDialogue(tc.data, tc.format))
		got, ok := languageFromContent(tc.data, tc.format)
		if !ok || got != tc.want || full.Lang.Iso6391() != tc.want {
			t.Errorf("%s: sampled %q (ok=%v), full text %q, want %q", tc.name, got, ok, full.Lang.Iso6391(), tc.want)
		}
	}
}

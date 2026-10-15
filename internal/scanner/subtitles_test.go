package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestExternalSubtitleDirCacheMatchesSidecars(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Movie.mkv", "Movie.srt", "Movie.en.forced.SRT", "MovieExtra.srt", "Movie.jpg", "Movie.part2.mkv", "Movie.part2.fr.ass", "Other.vtt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "Movie.de.srt"), 0700); err != nil {
		t.Fatal(err)
	}
	cache := newExternalSubtitleDirCache()
	for _, test := range []struct {
		media  string
		titles []string
	}{
		{"Movie.mkv", []string{"Movie.en.forced.SRT", "Movie.part2.fr.ass", "Movie.srt"}},
		{"Movie.part2.mkv", []string{"Movie.part2.fr.ass"}},
		{"Missing.mkv", nil},
	} {
		got, err := cache.Detect(filepath.Join(dir, test.media))
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, subtitle := range got {
			titles = append(titles, subtitle.Title)
		}
		if !reflect.DeepEqual(titles, test.titles) {
			t.Fatalf("%s titles = %v, want %v", test.media, titles, test.titles)
		}
		want, err := DetectExternalSubtitles(filepath.Join(dir, test.media))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("cached = %#v, uncached = %#v, error = %v", got, want, err)
		}
		if test.media == "Movie.mkv" && (!got[0].Forced || got[0].Language != "en" || got[0].Format != "srt") {
			t.Fatalf("uppercase forced sidecar = %#v", got[0])
		}
	}
}

func TestExternalSubtitleDirCacheConcurrent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Movie.en.srt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cache := newExternalSubtitleDirCache()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			got, err := cache.Detect(filepath.Join(dir, "Movie.mkv"))
			if err != nil || len(got) != 1 {
				t.Errorf("Detect = %#v, %v", got, err)
			}
		})
	}
	wg.Wait()
}

func TestParseSuffixCanonicalizesCompatibleLanguageTags(t *testing.T) {
	tests := []struct {
		name, suffix, want string
	}{
		{"three letter", ".eng", "en"},
		{"display name", ".Arabic", "ar"},
		{"region", ".pt-BR", "pt-BR"},
		{"script", ".zh-Hant", "zh-Hant"},
		{"missing", ".forced", ""},
		{"unknown", ".zzzz", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got ExternalSubtitleInfo
			parseSuffix(tc.suffix, &got)
			if got.Language != tc.want {
				t.Fatalf("language = %q, want %q", got.Language, tc.want)
			}
		})
	}
}

func TestParseSuffixReadsLanguageAndFlagsInAnyPosition(t *testing.T) {
	tests := []struct {
		suffix          string
		language        string
		forced, hearing bool
	}{
		{".en", "en", false, false},
		{".en.sdh", "en", false, true},
		{".sdh.en", "en", false, true},
		{".EN.SDH", "en", false, true},
		{".en.cc", "en", false, true},
		{".cc.en", "en", false, true},
		{".sdh", "", false, true},
		{".ai.hr", "hr", false, false},
		{".hr.ai", "hr", false, false},
		{".forced.sdh.eng", "en", true, true},
		{".en.forced.sdh", "en", true, true},
		{".default.en", "en", false, false},
		{".ai", "", false, false},
		{".jp", "", false, false},
		// The first language word wins.
		{".en.de", "en", false, false},
		{".pt.br", "pt", false, false},
		// Many short words are also ISO 639-3 codes, so a language with a
		// two-letter code wins over one that has only a three-letter code.
		{".sub.en", "en", false, false},
		{".dub.en", "en", false, false},
		{".mkv.en.sdh", "en", false, true},
		{".und.en", "en", false, false},
		{".yue", "yue", false, false},
		{".fil.forced", "fil", true, false},
		// "tl" is a two-letter code that canonicalizes to "fil".
		{".tl.en", "fil", false, false},
		{".tl.hi", "fil", false, true},
		// "hi" is Hindi only when no other word names a language.
		{".hi", "hi", false, false},
		{".HI", "hi", false, false},
		{".hi.forced", "hi", true, false},
		{".hi.sdh", "hi", false, true},
		{".en.hi", "en", false, true},
		{".hi.en", "en", false, true},
		{".hin.hi", "hi", false, true},
		{".hi.hi", "hi", false, true},
		{".hi.dub", "hi", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.suffix, func(t *testing.T) {
			var got ExternalSubtitleInfo
			parseSuffix(tc.suffix, &got)
			if got.Language != tc.language || got.Forced != tc.forced || got.HearingImpaired != tc.hearing {
				t.Fatalf("parseSuffix(%q) = language %q, forced %v, hearing impaired %v; want %q, %v, %v",
					tc.suffix, got.Language, got.Forced, got.HearingImpaired, tc.language, tc.forced, tc.hearing)
			}
		})
	}
}

func TestExternalSubtitleModelsCarryHearingImpaired(t *testing.T) {
	got := externalSubtitleModels([]ExternalSubtitleInfo{
		{Path: "/m/Movie.en.sdh.srt", Language: "en", Format: "srt", Title: "Movie.en.sdh.srt", HearingImpaired: true},
		{Path: "/m/Movie.en.srt", Language: "en", Format: "srt", Title: "Movie.en.srt"},
	})
	if len(got) != 2 || !got[0].HearingImpaired || got[1].HearingImpaired {
		t.Fatalf("externalSubtitleModels = %#v, want only the SDH sidecar hearing impaired", got)
	}
	if empty := externalSubtitleModels(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("externalSubtitleModels(nil) = %#v, want an empty non-nil slice", empty)
	}
}

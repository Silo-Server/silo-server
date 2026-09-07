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

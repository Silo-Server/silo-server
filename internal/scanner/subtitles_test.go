package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// Workers share one cache. With room for fewer directories than they read,
// listings are evicted and read again while other workers use them.
func TestExternalSubtitleDirCacheConcurrent(t *testing.T) {
	root := t.TempDir()
	var media []string
	for _, title := range []string{"One", "Two", "Three", "Four"} {
		for _, name := range []string{title + ".mkv", title + ".en.srt", "Subs/French.srt"} {
			path := filepath.Join(root, title, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		media = append(media, filepath.Join(root, title, title+".mkv"))
	}
	cache := newExternalSubtitleDirCache("movies")
	cache.size = 2
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Go(func() {
			for i := range media {
				path := media[(worker+i)%len(media)]
				got, err := cache.Detect(path)
				if err != nil || len(got) != 2 {
					t.Errorf("%s: Detect = %#v, %v", path, got, err)
				}
			}
		})
	}
	wg.Wait()
	if len(cache.dirs) > cache.size || cache.recent.Len() != len(cache.dirs) {
		t.Fatalf("cache holds %d directories (%d in order), want at most %d", len(cache.dirs), cache.recent.Len(), cache.size)
	}
}

// A directory that has dropped out of the cache is read again, so its videos
// get the same sidecars as they would from a cache that kept it.
func TestExternalSubtitleDirCacheReadsADroppedDirectoryAgain(t *testing.T) {
	root := t.TempDir()
	var media []string
	for _, name := range []string{"One/One.mkv", "One/One.en.srt", "One/Subs/One.fr.srt", "Two/Two.mkv", "Two/Two.de.srt", "One/One.part2.mkv"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(name) == ".mkv" {
			media = append(media, path)
		}
	}
	cache := newExternalSubtitleDirCache("movies")
	cache.size = 1
	for _, path := range append(media, media...) {
		got, err := cache.Detect(path)
		if err != nil {
			t.Fatal(err)
		}
		want, err := newExternalSubtitleDirCache("movies").Detect(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: bounded cache = %#v, fresh cache = %#v, error = %v", path, got, want, err)
		}
		if len(cache.dirs) > 1 || cache.recent.Len() != len(cache.dirs) {
			t.Fatalf("cache holds %d directories (%d in order), want at most 1", len(cache.dirs), cache.recent.Len())
		}
	}
}

func TestExternalSubtitleDirCacheKeepsSidecarsWhenASubtitleFolderFails(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Movie.mkv", "Movie.en.srt", "Subs/English.srt"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "Subs"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "Subs"), 0700) })
	if _, err := os.ReadDir(filepath.Join(dir, "Subs")); err == nil {
		t.Skip("an unreadable directory is still readable here (running as root)")
	}
	got, err := newExternalSubtitleDirCache("movies").Detect(filepath.Join(dir, "Movie.mkv"))
	if err == nil || len(got) != 1 || got[0].Title != "Movie.en.srt" {
		t.Fatalf("Detect = %#v, %v; want the sidecar beside the video and an error", got, err)
	}
}

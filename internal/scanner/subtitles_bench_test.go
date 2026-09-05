package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkExternalSubtitleDirCache(b *testing.B) {
	benchmarkExternalSubtitleDirCache(b, false)
}

func BenchmarkExternalSubtitleDirCacheCold(b *testing.B) {
	benchmarkExternalSubtitleDirCache(b, true)
}

func benchmarkExternalSubtitleDirCache(b *testing.B, cold bool) {
	for _, count := range []int{1000, 10000} {
		for _, density := range []string{"empty", "sparse", "dense"} {
			b.Run(fmt.Sprintf("%d/%s", count, density), func(b *testing.B) {
				dir := b.TempDir()
				paths := make([]string, count)
				for i := range count {
					paths[i] = filepath.Join(dir, fmt.Sprintf("Movie%05d.mkv", i))
					if err := os.WriteFile(paths[i], nil, 0600); err != nil {
						b.Fatal(err)
					}
					if density == "dense" || density == "sparse" && i%100 == 0 {
						if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("Movie%05d.en.srt", i)), nil, 0600); err != nil {
							b.Fatal(err)
						}
					}
				}
				cache := newExternalSubtitleDirCache()
				if _, err := cache.Detect(paths[0]); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					if cold {
						cache = newExternalSubtitleDirCache()
					}
					if _, err := cache.Detect(paths[i%count]); err != nil {
						b.Fatal(err)
					}
					i++
				}
			})
		}
	}
}

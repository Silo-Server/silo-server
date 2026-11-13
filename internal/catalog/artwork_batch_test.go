package catalog

import (
	"context"
	"slices"
	"testing"
)

type hintEchoPresigner struct{ calls [][]string }

func (p *hintEchoPresigner) PresignURLsWithExpiry(_ context.Context, paths []string, variant string) map[string]ResolvedImageURL {
	p.calls = append(p.calls, append([]string{variant}, paths...))
	out := make(map[string]ResolvedImageURL, len(paths))
	for _, path := range paths {
		out[path] = ResolvedImageURL{URL: variant + ":" + path}
	}
	return out
}

// Cached keys resolve the same at any hint, so a page's keys share one batch
// however its slots are sized; a plugin-hosted path is resolved at each hint a
// slot asks for it at.
func TestArtworkBatchResolvesPluginPathsPerHint(t *testing.T) {
	var batch ArtworkBatch
	batch.Add("tmdb/movies/1/poster/w500.r1.webp", "featured")
	batch.Add("tmdb/movies/1/backdrop/original.r1.webp", "original")
	batch.Add("tmdb://backdrop/a.jpg", "original")
	batch.Add("tmdb://poster/b.jpg", "featured")
	batch.Add("tmdb://backdrop/a.jpg", "featured")
	batch.Add("https://image.example.invalid/c.jpg", "original")
	batch.Add("-", "featured")
	batch.Add("", "featured")

	presigner := &hintEchoPresigner{}
	urls := batch.Resolve(t.Context(), presigner, "featured")

	want := [][]string{
		{"featured", "tmdb/movies/1/poster/w500.r1.webp", "tmdb/movies/1/backdrop/original.r1.webp", "https://image.example.invalid/c.jpg"},
		{"featured", "tmdb://poster/b.jpg", "tmdb://backdrop/a.jpg"},
		{"original", "tmdb://backdrop/a.jpg"},
	}
	if !slices.EqualFunc(presigner.calls, want, slices.Equal[[]string]) {
		t.Fatalf("batches = %q, want %q", presigner.calls, want)
	}
	for _, tc := range []struct{ path, hint, want string }{
		{"tmdb/movies/1/backdrop/original.r1.webp", "original", "featured:tmdb/movies/1/backdrop/original.r1.webp"},
		{"tmdb://backdrop/a.jpg", "original", "original:tmdb://backdrop/a.jpg"},
		{"tmdb://backdrop/a.jpg", "featured", "featured:tmdb://backdrop/a.jpg"},
		{"https://image.example.invalid/c.jpg", "original", "featured:https://image.example.invalid/c.jpg"},
		{"-", "featured", ""},
	} {
		if got := urls.Get(tc.path, tc.hint).URL; got != tc.want {
			t.Errorf("Get(%q, %q) = %q, want %q", tc.path, tc.hint, got, tc.want)
		}
	}
}

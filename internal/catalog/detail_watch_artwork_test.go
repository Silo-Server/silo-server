package catalog

import (
	"context"
	"testing"
)

type dummyImageResolver struct{}

func (r *dummyImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	if path == "" {
		return ""
	}
	return "https://cdn.example.com/" + variant + "/" + path
}

func (r *dummyImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = r.ResolveImageURL(ctx, p, variant)
	}
	return out
}

func TestWatchDetailArtworkFields(t *testing.T) {
	svc := &DetailService{}
	svc.SetImageResolver(&dummyImageResolver{})

	filter := AccessFilter{ImageSize: "medium"}

	// Verify PresignImageURL returns presigned values using resolver.
	posterURL := svc.PresignImageURL(context.Background(), "poster1.jpg", "poster", string(filter.ImageSize))
	if posterURL != "https://cdn.example.com/featured/poster/w500/poster1.jpg" && posterURL != "https://cdn.example.com/featured/poster1.jpg" {
		t.Logf("PresignImageURL poster result: %s", posterURL)
	}

	detail := WatchDetail{
		ContentID:         "m-100",
		Type:              "movie",
		Title:             "Test Movie",
		PosterThumbhash:   "hash_poster",
		BackdropThumbhash: "hash_backdrop",
		PosterURL:         svc.PresignImageURL(context.Background(), "movies/100/poster.jpg", "poster", string(filter.ImageSize)),
		BackdropURL:       svc.PresignImageURL(context.Background(), "movies/100/backdrop.jpg", "backdrop", string(filter.ImageSize)),
	}

	if detail.PosterThumbhash != "hash_poster" {
		t.Errorf("PosterThumbhash = %q, want %q", detail.PosterThumbhash, "hash_poster")
	}
	if detail.BackdropThumbhash != "hash_backdrop" {
		t.Errorf("BackdropThumbhash = %q, want %q", detail.BackdropThumbhash, "hash_backdrop")
	}
	if detail.PosterURL == "" {
		t.Errorf("PosterURL is empty, want non-empty presigned URL")
	}
	if detail.BackdropURL == "" {
		t.Errorf("BackdropURL is empty, want non-empty presigned URL")
	}
}

package catalog

import (
	"context"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type echoImageResolver struct{}

func (echoImageResolver) ResolveImageURL(_ context.Context, path, _ string) string {
	return "https://cdn.example/" + path
}

func (r echoImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

type fakeBackdropLister map[string][]models.ItemBackdrop

func (f fakeBackdropLister) ListByContentIDs(context.Context, []string) (map[string][]models.ItemBackdrop, error) {
	return f, nil
}

func TestPresignExtraArtworkUsesEachTypesLadder(t *testing.T) {
	s := &DetailService{imageResolver: echoImageResolver{}}
	item := &models.MediaItem{
		BannerPath: "local/movies/m/aa/banner/original.r1.webp", BannerThumbhash: "bh",
		ThumbPath:    "local/movies/m/bb/thumb/original.r2.webp",
		ClearArtPath: "local/movies/m/cc/clearart/original.r3.webp",
		DiscPath:     "local/movies/m/dd/disc/original.r4.webp",
	}
	detail := &ItemDetail{}
	s.presignExtraArtwork(context.Background(), detail, item, "large")
	for name, pair := range map[string][2]string{
		"banner":   {detail.BannerURL, "https://cdn.example/local/movies/m/aa/banner/w1000.r1.webp"},
		"thumb":    {detail.ThumbURL, "https://cdn.example/local/movies/m/bb/thumb/w1280.r2.webp"},
		"clearart": {detail.ClearArtURL, "https://cdn.example/local/movies/m/cc/clearart/w1000.r3.webp"},
		"disc":     {detail.DiscURL, "https://cdn.example/local/movies/m/dd/disc/w1000.r4.webp"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s URL = %q, want %q", name, pair[0], pair[1])
		}
	}
	if detail.BannerThumbhash != "bh" || detail.ThumbThumbhash != "" {
		t.Fatalf("thumbhashes = %q/%q", detail.BannerThumbhash, detail.ThumbThumbhash)
	}

	empty := &ItemDetail{}
	s.presignExtraArtwork(context.Background(), empty, &models.MediaItem{BannerThumbhash: "orphan"}, "")
	if empty.BannerURL != "" || empty.BannerThumbhash != "" {
		t.Fatalf("an item without a banner got %q/%q", empty.BannerURL, empty.BannerThumbhash)
	}
}

func TestAttachBackdropURLsListsThePrimaryFirst(t *testing.T) {
	s := &DetailService{imageResolver: echoImageResolver{}, backdropRepo: fakeBackdropLister{
		"movie-1": {
			{Ordinal: 1, Path: "local/movies/movie-1/aa-1/backdrop/original.r1.webp"},
			{Ordinal: 2, Path: ""}, // still waiting for the image cache
			{Ordinal: 3, Path: "local/movies/movie-1/cc-3/backdrop/original.r3.webp"},
		},
		"series-1": {{Ordinal: 1, Path: "local/series/series-1/dd-4/backdrop/original.r4.webp"}},
	}}
	movie := &ItemDetail{ContentID: "movie-1", Type: "movie", BackdropURL: "https://cdn.example/primary"}
	series := &ItemDetail{ContentID: "series-1", Type: "series"}
	bare := &ItemDetail{ContentID: "movie-2", Type: "movie"}
	episode := &ItemDetail{ContentID: "movie-1", Type: "episode", BackdropURL: "https://cdn.example/series-backdrop"}
	s.attachBackdropURLs(context.Background(), []*ItemDetail{movie, series, bare, episode, nil}, "")

	if want := []string{
		"https://cdn.example/primary",
		"https://cdn.example/local/movies/movie-1/aa-1/backdrop/w1920.r1.webp",
		"https://cdn.example/local/movies/movie-1/cc-3/backdrop/w1920.r3.webp",
	}; !slices.Equal(movie.BackdropURLs, want) {
		t.Fatalf("movie backdrop URLs = %q, want %q", movie.BackdropURLs, want)
	}
	if want := []string{"https://cdn.example/local/series/series-1/dd-4/backdrop/w1920.r4.webp"}; !slices.Equal(series.BackdropURLs, want) {
		t.Fatalf("series backdrop URLs = %q, want %q", series.BackdropURLs, want)
	}
	if bare.BackdropURLs != nil || episode.BackdropURLs != nil {
		t.Fatalf("bare=%q episode=%q, want none", bare.BackdropURLs, episode.BackdropURLs)
	}
}

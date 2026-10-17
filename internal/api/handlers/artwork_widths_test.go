package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The TV spec's home request: medium cards, a 4K hero backdrop and logo. Each
// slot resolves on its own ladder, and an episode still in the backdrop slot
// follows the backdrop width.
var tvHomeArtwork = imagesize.Request{
	Size:   imagesize.Medium,
	Widths: imagesize.Widths{Backdrop: 2688, Logo: 1760},
}

func TestFeaturedArtworkResolvesEachSlotAtItsWidth(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)

	cards := []featuredArtworkCard[string]{
		{key: "movie", item: &models.MediaItem{
			ContentID:    "movie",
			PosterPath:   "tmdb/movies/1/poster/original.r1.webp",
			BackdropPath: "tmdb/movies/1/backdrop/original.r1.webp",
			LogoPath:     "tmdb/movies/1/logo/original.r1.webp",
		}},
		{key: "episode", item: &models.MediaItem{
			ContentID:    "episode",
			BackdropPath: "tmdb/series/2/seasons/1/episodes/3/still/original.r2.webp",
		}},
	}
	urls := resolveFeaturedArtwork(t.Context(), detailSvc, cards, tvHomeArtwork)

	if resolver.batchCalls != 1 || resolver.singleCalls != 0 {
		t.Fatalf("batch=%d single=%d, want one batch", resolver.batchCalls, resolver.singleCalls)
	}
	want := map[string]sectionItemImageURLs{
		"movie": {
			posterURL:   "resolved:tmdb/movies/1/poster/w500.r1.webp",
			backdropURL: "resolved:tmdb/movies/1/backdrop/original.r1.webp",
			logoURL:     "resolved:tmdb/movies/1/logo/original.r1.webp",
		},
		"episode": {
			backdropURL: "resolved:tmdb/series/2/seasons/1/episodes/3/still/original.r2.webp",
		},
	}
	for key, w := range want {
		if urls[key] != w {
			t.Errorf("%s artwork = %+v, want %+v", key, urls[key], w)
		}
	}
	// Cached keys share one batch at image_size's hint, which only names the
	// resolver's URL cache entry for them.
	if resolver.variant != imagesize.PluginVariantFeatured {
		t.Errorf("hint = %q, want %q", resolver.variant, imagesize.PluginVariantFeatured)
	}
}

// Android TV's 1080p hero is 1344px of backdrop: the w1920 rung covers it, and
// the logo's w1280 covers its 880px.
func TestFeaturedArtworkPicksNarrowestCoveringRung(t *testing.T) {
	item := &models.MediaItem{
		PosterPath:   "tmdb/movies/1/poster/original.r1.webp",
		BackdropPath: "tmdb/movies/1/backdrop/original.r1.webp",
		LogoPath:     "tmdb/movies/1/logo/original.r1.webp",
	}
	got := featuredItemArtworkPaths(item, "", imagesize.Request{Widths: imagesize.Widths{Poster: 176, Backdrop: 1344, Logo: 880}})
	want := featuredArtworkPaths{
		posterPath:   "tmdb/movies/1/poster/w300.r1.webp",
		backdropPath: "tmdb/movies/1/backdrop/w1920.r1.webp",
		logoPath:     "tmdb/movies/1/logo/w1280.r1.webp",
	}
	if got != want {
		t.Fatalf("paths = %+v, want %+v", got, want)
	}
}

// Plugin-hosted art has no cached rungs, so the slot's width reaches the
// plugin as its hint: a 4K hero backdrop asks for the original while the card
// poster beside it stays at the featured size.
func TestFeaturedArtworkSendsPluginsEachSlotsHint(t *testing.T) {
	presigner := &hintRecordingPresigner{}
	cards := []featuredArtworkCard[string]{{key: "movie", item: &models.MediaItem{
		ContentID:    "movie",
		PosterPath:   "tmdb://poster/p.jpg",
		BackdropPath: "tmdb://backdrop/b.jpg",
		LogoPath:     "tmdb://logo/l.png",
	}}}
	urls := resolveFeaturedArtwork(t.Context(), presigner, cards, tvHomeArtwork)

	want := sectionItemImageURLs{
		posterURL:   imagesize.PluginVariantFeatured + ":tmdb://poster/p.jpg",
		backdropURL: imagesize.PluginVariantOriginal + ":tmdb://backdrop/b.jpg",
		logoURL:     imagesize.PluginVariantOriginal + ":tmdb://logo/l.png",
	}
	if urls["movie"] != want {
		t.Fatalf("artwork = %+v, want %+v", urls["movie"], want)
	}
}

type hintRecordingPresigner struct{}

func (hintRecordingPresigner) PresignURLsWithExpiry(_ context.Context, paths []string, variant string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(paths))
	for _, path := range paths {
		out[path] = catalog.ResolvedImageURL{URL: variant + ":" + path}
	}
	return out
}

package handlers

import (
	"context"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
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

// Recommendation cards resolve through the featured resolver in one batch,
// at the request's artwork, rather than one presign per image.
func TestRecommendationCardsBatchArtworkAtRequestedWidths(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &RecommendationsHandler{DetailSvc: detailSvc}

	items := map[string]*models.MediaItem{
		"a": {ContentID: "a", PosterPath: "tmdb/movies/1/poster/original.r1.webp", BackdropPath: "tmdb/movies/1/backdrop/original.r1.webp"},
		"b": {ContentID: "b", PosterPath: "tmdb/movies/2/poster/original.r2.webp", LogoPath: "tmdb/movies/2/logo/original.r2.webp"},
	}
	enrichment := &itemEnrichment{items: items}
	artwork := h.featuredCardArtwork(t.Context(), items, tvHomeArtwork)
	cards := h.buildSectionItems(t.Context(),
		[]recommendations.ScoredItem{{MediaItemID: "a"}, {MediaItemID: "b"}}, enrichment, nil, artwork)

	if resolver.batchCalls != 1 || resolver.singleCalls != 0 {
		t.Fatalf("batch=%d single=%d, want one batch", resolver.batchCalls, resolver.singleCalls)
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	if got, want := cards[0].BackdropURL, "resolved:tmdb/movies/1/backdrop/original.r1.webp"; got != want {
		t.Errorf("hero backdrop = %q, want %q", got, want)
	}
	if got, want := cards[1].PosterURL, "resolved:tmdb/movies/2/poster/w500.r2.webp"; got != want {
		t.Errorf("card poster = %q, want %q", got, want)
	}
	if got, want := cards[1].LogoURL, "resolved:tmdb/movies/2/logo/original.r2.webp"; got != want {
		t.Errorf("logo = %q, want %q", got, want)
	}
}

// Without sizes, recommendation cards keep the keys and hint they always had,
// which the frozen /api/v1 routes depend on: the featured poster and backdrop,
// the stored logo, and the featured plugin hint.
func TestRecommendationCardsUnsizedKeepTheirArtwork(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &RecommendationsHandler{DetailSvc: detailSvc}

	items := map[string]*models.MediaItem{"a": {
		ContentID:    "a",
		PosterPath:   "tmdb/movies/1/poster/original.r1.webp",
		BackdropPath: "tmdb/movies/1/backdrop/original.r1.webp",
		LogoPath:     "tmdb/movies/1/logo/original.r1.webp",
	}}
	got := h.featuredCardArtwork(t.Context(), items, imagesize.Request{})["a"]
	want := sectionItemImageURLs{
		posterURL:   "resolved:" + featuredPosterPath(items["a"].PosterPath),
		backdropURL: "resolved:" + featuredBackdropPath(items["a"].BackdropPath),
		logoURL:     "resolved:" + items["a"].LogoPath,
	}
	if got != want {
		t.Fatalf("artwork = %+v, want %+v", got, want)
	}
	if resolver.variant != imagesize.PluginVariantFeatured {
		t.Errorf("hint = %q, want %q", resolver.variant, imagesize.PluginVariantFeatured)
	}
}

// Watch Tonight cards fill cast headshots on the profile ladder, w500 unsized
// like item detail, in one batch; an http(s) photo passes through.
func TestWatchTonightCastPhotosResolveOnProfileLadder(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &RecommendationsHandler{DetailSvc: detailSvc}

	cards := []swipeCardResponse{{Cast: []swipeCardCastMember{
		{Name: "Cached", photoPath: "tmdb/people/1/profile/original.r1.webp"},
		{Name: "Remote", photoPath: "https://image.example.invalid/p.jpg"},
		{Name: "None"},
	}}}
	h.resolveCastPhotos(t.Context(), cards, imagesize.Unset)

	cast := cards[0].Cast
	if got, want := cast[0].PhotoURL, "resolved:tmdb/people/1/profile/w500.r1.webp"; got != want {
		t.Errorf("cached photo = %q, want %q", got, want)
	}
	if got, want := cast[1].PhotoURL, "https://image.example.invalid/p.jpg"; got != want {
		t.Errorf("remote photo = %q, want %q", got, want)
	}
	if cast[2].PhotoURL != "" {
		t.Errorf("photo without a path = %q, want empty", cast[2].PhotoURL)
	}
	if resolver.batchCalls != 1 || !slices.Equal(resolver.paths, []string{"tmdb/people/1/profile/w500.r1.webp"}) {
		t.Fatalf("batch=%d paths=%v, want one batch of the cached headshot", resolver.batchCalls, resolver.paths)
	}

	// A plugin-hosted photo keeps its path; the plugin hint picks the size.
	plugin := []swipeCardResponse{{Cast: []swipeCardCastMember{{Name: "Plugin", photoPath: "metadb://images/abc/original.jpg"}}}}
	h.resolveCastPhotos(t.Context(), plugin, imagesize.Unset)
	if got, want := plugin[0].Cast[0].PhotoURL, "resolved:metadb://images/abc/original.jpg"; got != want {
		t.Errorf("plugin photo = %q, want %q", got, want)
	}
	if resolver.variant != imagesize.PluginVariantLarge {
		t.Errorf("plugin hint = %q, want %q", resolver.variant, imagesize.PluginVariantLarge)
	}

	h.resolveCastPhotos(t.Context(), cards, imagesize.Width(200))
	if got, want := cards[0].Cast[0].PhotoURL, "resolved:tmdb/people/1/profile/w300.r1.webp"; got != want {
		t.Errorf("photo at 200px = %q, want %q", got, want)
	}
	// A plugin's narrower headshots are softer than a cast card, so a width
	// short of the original keeps the large hint.
	h.resolveCastPhotos(t.Context(), plugin, imagesize.Width(200))
	if resolver.variant != imagesize.PluginVariantLarge {
		t.Errorf("plugin hint at 200px = %q, want %q", resolver.variant, imagesize.PluginVariantLarge)
	}
}

// A Watch Tonight page resolves artwork for the cards it returns, not for the
// rest of the hydration window, which only decides whether more remain.
func TestSwipeCardsResolveArtworkOnlyForReturnedCards(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &RecommendationsHandler{DetailSvc: detailSvc, CastFetcher: castByID{}}
	items := map[string]*models.MediaItem{}
	for _, id := range []string{"a", "b", "c"} {
		items[id] = &models.MediaItem{ContentID: id, PosterPath: "tmdb/movies/" + id + "/poster/original.r1.webp"}
	}

	cards, more := h.swipeCards(t.Context(), recommendationCandidates("a", "b", "c"), watchTonightEnrichment{items: items}, 1, imagesize.Request{})
	if len(cards) != 1 || !more {
		t.Fatalf("cards = %d, more = %v; want one card and more", len(cards), more)
	}
	if !slices.Equal(resolver.paths, []string{"tmdb/movies/a/poster/w500.r1.webp"}) {
		t.Fatalf("resolved %v, want only the returned card's poster", resolver.paths)
	}
}

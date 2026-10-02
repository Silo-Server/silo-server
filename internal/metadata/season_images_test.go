package metadata

import (
	"context"
	"errors"
	"testing"
	"time"
)

type seasonImageProvider struct {
	slug            string
	seasonGalleries map[int][]RemoteImage
	itemImages      []RemoteImage
	seasons         []SeasonResult
	galleryErr      error
	itemErr         error
	seasonErr       error
	imageRequests   []ImageRequest
	seasonRequests  []SeasonsRequest
}

type itemImageProvider struct {
	slug   string
	images []RemoteImage
}

func (p *itemImageProvider) Slug() string       { return p.slug }
func (p *itemImageProvider) Name() string       { return p.slug }
func (p *itemImageProvider) ForTypes() []string { return []string{"series"} }

func (p *itemImageProvider) GetImages(context.Context, ImageRequest) ([]RemoteImage, error) {
	return p.images, nil
}

func (p *seasonImageProvider) Slug() string       { return p.slug }
func (p *seasonImageProvider) Name() string       { return p.slug }
func (p *seasonImageProvider) ForTypes() []string { return []string{"series"} }

func (p *seasonImageProvider) GetImages(_ context.Context, req ImageRequest) ([]RemoteImage, error) {
	p.imageRequests = append(p.imageRequests, req)
	if req.SeasonNumber == nil {
		return p.itemImages, p.itemErr
	}
	return p.seasonGalleries[*req.SeasonNumber], p.galleryErr
}

func (p *seasonImageProvider) GetSeasons(_ context.Context, req SeasonsRequest) ([]SeasonResult, error) {
	p.seasonRequests = append(p.seasonRequests, req)
	return p.seasons, p.seasonErr
}

func (p *seasonImageProvider) GetEpisodes(context.Context, EpisodesRequest) ([]EpisodeResult, error) {
	return nil, nil
}

func TestFetchSeasonImagesReturnsFullExactGallery(t *testing.T) {
	seasonTwo := 2
	seasonOne := 1
	provider := &seasonImageProvider{
		slug: "tmdb",
		seasonGalleries: map[int][]RemoteImage{2: {
			{URL: "tmdb://season-2-low.jpg", Type: ImagePoster, Rating: 4, SeasonNumber: &seasonTwo},
			{URL: "tmdb://season-2-high.jpg", Type: ImagePoster, Rating: 9, SeasonNumber: &seasonTwo},
			{URL: "tmdb://unscoped-show.jpg", Type: ImagePoster, Rating: 10},
			{URL: "tmdb://season-1.jpg", Type: ImagePoster, Rating: 8, SeasonNumber: &seasonOne},
		}},
	}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"42:season": {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)},
	}}

	images, providerErrors, err := service.FetchSeasonImages(
		context.Background(), map[string]string{"tmdb": "123"}, "en", 42, 2,
	)
	if err != nil {
		t.Fatalf("FetchSeasonImages() error = %v", err)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("provider errors = %v, want none", providerErrors)
	}
	if len(images) != 2 || images[0].URL != "tmdb://season-2-high.jpg" || images[1].URL != "tmdb://season-2-low.jpg" {
		t.Fatalf("images = %#v, want exact Season 2 gallery sorted by rating", images)
	}
	if len(provider.imageRequests) != 1 || provider.imageRequests[0].SeasonNumber == nil || *provider.imageRequests[0].SeasonNumber != 2 {
		t.Fatalf("image requests = %#v, want exact Season 2", provider.imageRequests)
	}
	if len(provider.seasonRequests) != 0 {
		t.Fatalf("GetSeasons calls = %d, want no compatibility fallback", len(provider.seasonRequests))
	}
}

func TestFetchSeasonImagesListsSpecialsBeforeShowFallbacks(t *testing.T) {
	specials := 0
	provider := &seasonImageProvider{
		slug: "tmdb",
		seasonGalleries: map[int][]RemoteImage{0: {
			{URL: "tmdb://specials-low.jpg", Type: ImagePoster, Rating: 4, SeasonNumber: &specials},
			{URL: "tmdb://specials-high.jpg", Type: ImagePoster, Rating: 8, SeasonNumber: &specials},
		}},
		itemImages: []RemoteImage{
			{URL: "tmdb://show-high.jpg", Type: ImagePoster, Rating: 10},
			{URL: "tmdb://show-low.jpg", Type: ImagePoster, Rating: 2},
		},
	}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"8:season": {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)},
	}}

	images, providerErrors, err := service.FetchSeasonImages(
		context.Background(), map[string]string{"tmdb": "123"}, "en", 8, 0,
	)
	if err != nil {
		t.Fatalf("FetchSeasonImages() error = %v", err)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("provider errors = %v, want none", providerErrors)
	}
	want := []string{
		"tmdb://specials-high.jpg", "tmdb://specials-low.jpg",
		"tmdb://show-high.jpg", "tmdb://show-low.jpg",
	}
	if len(images) != len(want) {
		t.Fatalf("images = %#v, want %v", images, want)
	}
	for i := range want {
		if images[i].URL != want[i] {
			t.Fatalf("images[%d] = %q, want %q (Specials must precede show fallbacks)", i, images[i].URL, want[i])
		}
	}
	if len(provider.imageRequests) != 2 || provider.imageRequests[0].SeasonNumber == nil || provider.imageRequests[1].SeasonNumber != nil {
		t.Fatalf("image requests = %#v, want scoped Specials then unscoped show gallery", provider.imageRequests)
	}
}

func TestFetchSeasonImagesPrefersLaterExactSpecialsOverEarlierShowFallback(t *testing.T) {
	specials := 0
	legacy := &seasonImageProvider{
		slug: "legacy",
		itemImages: []RemoteImage{
			{URL: "shared://specials.jpg", Type: ImagePoster, Rating: 1},
			{URL: "legacy://show.jpg", Type: ImagePoster, Rating: 10},
		},
	}
	scoped := &seasonImageProvider{
		slug: "scoped",
		seasonGalleries: map[int][]RemoteImage{0: {
			{URL: "shared://specials.jpg", Type: ImagePoster, Rating: 8, SeasonNumber: &specials},
		}},
	}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"11:season": {providers: []Provider{legacy, scoped}, expiresAt: time.Now().Add(time.Hour)},
	}}

	images, _, err := service.FetchSeasonImages(context.Background(), map[string]string{"legacy": "1"}, "en", 11, 0)
	if err != nil {
		t.Fatalf("FetchSeasonImages() error = %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("images = %#v, want exact Specials poster then one show fallback", images)
	}
	first := images[0]
	if first.URL != "shared://specials.jpg" || first.ProviderID != "scoped" || first.Rating != 8 ||
		first.SeasonNumber == nil || *first.SeasonNumber != 0 {
		t.Fatalf("images[0] = %#v, want scoped exact Specials record", first)
	}
	if images[1].URL != "legacy://show.jpg" {
		t.Fatalf("images[1] = %#v, want remaining show fallback", images[1])
	}
	if len(scoped.seasonRequests) != 0 {
		t.Fatalf("scoped GetSeasons calls = %d, want none once its exact result is accepted", len(scoped.seasonRequests))
	}
}

func TestFetchSeasonImagesUsesPrimaryCompatibilityFallback(t *testing.T) {
	provider := &seasonImageProvider{
		slug:       "legacy",
		itemImages: []RemoteImage{{URL: "legacy://show.jpg", Type: ImagePoster}},
		seasons:    []SeasonResult{{SeasonNumber: 3, PosterPath: "legacy://season-3-primary.jpg"}},
	}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"9:season": {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)},
	}}

	images, _, err := service.FetchSeasonImages(context.Background(), map[string]string{"legacy": "1"}, "en", 9, 3)
	if err != nil {
		t.Fatalf("FetchSeasonImages() error = %v", err)
	}
	if len(images) != 1 || images[0].URL != "legacy://season-3-primary.jpg" {
		t.Fatalf("images = %#v, want exact primary compatibility fallback", images)
	}
}

func TestFetchSeasonImagesKeepsProviderErrorsAndContinues(t *testing.T) {
	failing := &seasonImageProvider{
		slug:       "tvdb",
		galleryErr: errors.New("gallery unavailable"),
		seasonErr:  errors.New("season unavailable"),
	}
	seasonThree := 3
	working := &seasonImageProvider{slug: "tmdb", seasonGalleries: map[int][]RemoteImage{3: {{
		URL: "tmdb://season-3.jpg", Type: ImagePoster, SeasonNumber: &seasonThree,
	}}}}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"7:season": {providers: []Provider{failing, working}, expiresAt: time.Now().Add(time.Hour)},
	}}

	images, providerErrors, err := service.FetchSeasonImages(
		context.Background(), map[string]string{"tvdb": "456", "tmdb": "123"}, "en", 7, 3,
	)
	if err != nil {
		t.Fatalf("FetchSeasonImages() error = %v", err)
	}
	if len(images) != 1 || images[0].URL != "tmdb://season-3.jpg" {
		t.Fatalf("images = %#v, want working provider poster", images)
	}
	if providerErrors["tvdb"] != "season unavailable" {
		t.Fatalf("provider errors = %v", providerErrors)
	}
}

func TestFetchItemImagesFiltersClearArtLogos(t *testing.T) {
	tvdb := &itemImageProvider{slug: "tvdb", images: []RemoteImage{
		{ProviderID: "tvdb", URL: "tvdb://show-poster.jpg", Type: ImagePoster, Rating: 3},
		{ProviderID: "tvdb", URL: tvdbClearArtURL, Type: ImageLogo, Rating: 10},
		{ProviderID: "tvdb", URL: tvdbClearLogoURL, Type: ImageLogo, Rating: 8},
	}}
	tmdb := &itemImageProvider{slug: "tmdb", images: []RemoteImage{
		{ProviderID: "tmdb", URL: "tmdb://lower-rated-wordmark.png", Type: ImageLogo, Rating: 5},
		{ProviderID: "tmdb", URL: "tmdb://backdrop.jpg", Type: ImageBackdrop, Rating: 7},
		{ProviderID: "tmdb", URL: "tmdb://top-rated-wordmark.png", Type: ImageLogo, Rating: 9},
	}}
	service := &MetadataService{chainCache: map[string]chainCacheEntry{
		"11:series": {providers: []Provider{tvdb, tmdb}, expiresAt: time.Now().Add(time.Hour)},
	}}

	images, providerErrors, err := service.FetchItemImages(
		context.Background(), map[string]string{"tvdb": "456", "tmdb": "123"}, "series", "en", 11,
	)
	if err != nil {
		t.Fatalf("FetchItemImages() error = %v", err)
	}
	if len(providerErrors) != 0 {
		t.Fatalf("provider errors = %v, want none", providerErrors)
	}
	if len(images) != 5 {
		t.Fatalf("images = %#v, want all art except the TVDB clear art", images)
	}
	sawClearLogo := false
	for _, image := range images {
		if image.URL == tvdbClearArtURL {
			t.Fatalf("TVDB clear art was not filtered: %#v", image)
		}
		sawClearLogo = sawClearLogo || image.URL == tvdbClearLogoURL
	}
	if !sawClearLogo {
		t.Fatalf("images = %#v, want the TVDB ClearLogo offered", images)
	}
	for i := 1; i < len(images); i++ {
		if images[i-1].Rating < images[i].Rating {
			t.Fatalf("images are not sorted by rating: %#v", images)
		}
	}
}

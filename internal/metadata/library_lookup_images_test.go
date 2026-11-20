package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func seedImageChain(t *testing.T, service *MetadataService, folderID int, contentType string, providers []Provider) {
	t.Helper()
	if service.chainCache == nil {
		service.chainCache = map[string]chainCacheEntry{}
	}
	service.chainCache[fmt.Sprintf("%d:%s", folderID, contentType)] = chainCacheEntry{
		providers: providers,
		expiresAt: time.Now().Add(time.Hour),
	}
}

// itemImageProvider is an image-capable provider stub: it answers every image
// request with its fixed gallery.
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

func TestDiscoverLibraryItemImagesUsesConfiguredImageProviders(t *testing.T) {
	provider := &itemImageProvider{slug: "tmdb", images: []RemoteImage{
		{ProviderID: "tmdb", URL: "tmdb://poster.jpg", Type: ImagePoster, Rating: 5},
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/backdrop.jpg", Type: ImageBackdrop, Rating: 7},
	}}
	service := &MetadataService{}
	seedImageChain(t, service, 11, "series", []Provider{provider})

	item := &models.MediaItem{Type: "series", Title: "Example"}
	poster, backdrop, images, err := service.DiscoverLibraryItemImages(
		context.Background(), 11, item, map[string]string{"tmdb": "123"}, "series", "en",
	)
	if err != nil {
		t.Fatalf("DiscoverLibraryItemImages: %v", err)
	}
	if poster != "tmdb://poster.jpg" {
		t.Fatalf("poster = %q, want tmdb://poster.jpg", poster)
	}
	if backdrop != "https://image.tmdb.org/backdrop.jpg" {
		t.Fatalf("backdrop = %q", backdrop)
	}
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}
	if item.PosterSourcePath != "tmdb://poster.jpg" {
		t.Fatalf("item poster source = %q", item.PosterSourcePath)
	}
	if item.BackdropSourcePath != "https://image.tmdb.org/backdrop.jpg" {
		t.Fatalf("item backdrop source = %q", item.BackdropSourcePath)
	}
}

func TestDiscoverLibraryItemImagesRetainsHTTPSAndPluginSchemes(t *testing.T) {
	provider := &itemImageProvider{slug: "tmdb", images: []RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.tmdb.org/p.jpg", Type: ImagePoster, Rating: 1},
	}}
	service := &MetadataService{}
	seedImageChain(t, service, 7, "movie", []Provider{provider})

	item := &models.MediaItem{Type: "movie"}
	poster, _, _, err := service.DiscoverLibraryItemImages(
		context.Background(), 7, item, map[string]string{"tmdb": "1"}, "movie", "en",
	)
	if err != nil {
		t.Fatalf("DiscoverLibraryItemImages: %v", err)
	}
	if poster != "https://image.tmdb.org/p.jpg" {
		t.Fatalf("poster = %q", poster)
	}
}

func TestDiscoverLibraryItemImagesMalformedSchemeDoesNotReplaceExisting(t *testing.T) {
	provider := &itemImageProvider{slug: "bad", images: []RemoteImage{
		{ProviderID: "bad", URL: "bad scheme://poster.jpg", Type: ImagePoster, Rating: 10},
	}}
	service := &MetadataService{}
	seedImageChain(t, service, 9, "series", []Provider{provider})

	item := &models.MediaItem{
		Type:       "series",
		PosterPath: "https://valid.example/poster.jpg",
	}
	poster, _, _, err := service.DiscoverLibraryItemImages(
		context.Background(), 9, item, map[string]string{}, "series", "en",
	)
	if err != nil {
		t.Fatalf("DiscoverLibraryItemImages: %v", err)
	}
	if poster != "https://valid.example/poster.jpg" {
		t.Fatalf("poster = %q, want existing valid artwork retained", poster)
	}
}

func TestDiscoverLibraryItemImageStructForm(t *testing.T) {
	provider := &itemImageProvider{slug: "tmdb", images: []RemoteImage{
		{ProviderID: "tmdb", URL: "tmdb://poster.jpg", Type: ImagePoster, Rating: 5},
	}}
	service := &MetadataService{}
	seedImageChain(t, service, 5, "series", []Provider{provider})

	item := &models.MediaItem{Type: "series"}
	result, err := service.DiscoverLibraryItemImage(
		context.Background(), 5, item, map[string]string{"tmdb": "1"}, "series", "en",
	)
	if err != nil {
		t.Fatalf("DiscoverLibraryItemImage: %v", err)
	}
	if result == nil || result.PosterPath != "tmdb://poster.jpg" {
		t.Fatalf("result = %#v", result)
	}
}

func TestEnqueueLibraryItemImagesRecordsCacheJobs(t *testing.T) {
	enqueuer := &recordingImageCacheJobEnqueuer{}
	service := &MetadataService{}
	service.SetAutoCacheImages(true)
	service.SetImageCacheJobEnqueuer(enqueuer)

	item := &models.MediaItem{
		ContentID:        "series-1",
		Type:             "series",
		PosterPath:       "tmdb://poster.jpg",
		PosterSourcePath: "tmdb://poster.jpg",
	}
	err := service.EnqueueLibraryItemImages(context.Background(), item, map[string]string{"tmdb": "1"}, []RemoteImage{
		{ProviderID: "tmdb", URL: "tmdb://poster.jpg", Type: ImagePoster, Rating: 5},
	})
	if err != nil {
		t.Fatalf("EnqueueLibraryItemImages: %v", err)
	}
	if len(enqueuer.inputs) == 0 {
		t.Fatal("expected cache jobs to be enqueued")
	}
	if enqueuer.inputs[0].TargetContentID != "series-1" {
		t.Fatalf("target = %q", enqueuer.inputs[0].TargetContentID)
	}
}

func TestEnqueueLibraryItemImagesNoEnqueuerNonFatal(t *testing.T) {
	service := &MetadataService{}
	item := &models.MediaItem{ContentID: "series-2", Type: "series"}
	if err := service.EnqueueLibraryItemImages(context.Background(), item, nil, nil); err != nil {
		t.Fatalf("EnqueueLibraryItemImages: %v", err)
	}
}

package metadata

import (
	"context"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// unreadableLocalArtProvider is the local provider with an NFO it cannot
// read when err is set.
type unreadableLocalArtProvider struct {
	localHintArtProvider
	err error
}

func (p *unreadableLocalArtProvider) GetMetadata(ctx context.Context, req MetadataRequest) (*MetadataResult, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.localHintArtProvider.GetMetadata(ctx, req)
}

// A cached copy of an image URL the NFO names stays through a fill-empty
// refresh that could not read the NFO (a share that is briefly down), as a
// cached sidecar does, and gives way once the NFO is read without it.
func TestScheduledRefreshKeepsNFOURLArtWhileTheNFOIsUnreadable(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		cachedPoster = "nfo/movies/movie:tmdb:7/poster/original.webp"
		nfoPoster    = "nfo+https://img.example.org/poster.jpg"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	remote := &imageArtProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
		images:   []RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}},
	}
	for _, tc := range []struct {
		name    string
		readErr error
		want    string
	}{
		{"NFO unreadable", fmt.Errorf("%w: reading movie.nfo: input/output error", ErrLocalMetadataUnavailable), nfoPoster},
		{"NFO read without the URL", nil, remotePoster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			ctx := context.Background()
			h.service.SetAutoCacheImages(true)
			h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
			providerRepo := newFakeProviderIDRepo()
			providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
			h.service.providerIDRepo = providerRepo
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
				ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
				DefaultMetadataLanguage: "en",
				PosterPath:              cachedPoster, PosterSourcePath: nfoPoster, PosterThumbhash: "nfo-thumb",
				Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}); err != nil {
				t.Fatalf("seed item: %v", err)
			}
			local := &unreadableLocalArtProvider{localHintArtProvider{imageArtProvider{slug: "nfo"}}, tc.readErr}
			if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
				ContentID: contentID, Mode: ModeScheduledRefresh, Language: "en",
			}, []Provider{local, remote}); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			got, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got.PosterSourcePath != tc.want {
				t.Fatalf("poster source = %q, want %q", got.PosterSourcePath, tc.want)
			}
			if tc.want == nfoPoster && (got.PosterPath != cachedPoster || got.PosterThumbhash != "nfo-thumb") {
				t.Errorf("poster = path %q thumb %q, want the cached NFO poster served", got.PosterPath, got.PosterThumbhash)
			}
		})
	}
}

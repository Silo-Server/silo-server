package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netguard"
)

type curationImageItems map[string]*models.MediaItem

func (f curationImageItems) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	if row := f[id]; row != nil {
		return row, nil
	}
	return nil, catalog.ErrItemNotFound
}

type curationImageSeasons struct{}

func (curationImageSeasons) GetByID(context.Context, string) (*models.Season, error) {
	return nil, catalog.ErrSeasonNotFound
}

type curationImageEpisodes struct{}

func (curationImageEpisodes) GetByID(context.Context, string) (*models.Episode, error) {
	return &models.Episode{SeriesID: "series", SeasonNumber: 0, EpisodeNumber: 2}, nil
}

type curationImageService struct {
	calls   int
	request metadata.ApplyItemImageRequest
	private bool
	fail    bool
	stored  string
}

func (f *curationImageService) FetchItemImages(context.Context, map[string]string, string, string, int) ([]metadata.RemoteImage, map[string]string, error) {
	return []metadata.RemoteImage{{ProviderID: "tmdb", URL: "source", Type: metadata.ImagePoster}}, nil, nil
}
func (f *curationImageService) FetchSeasonImages(context.Context, map[string]string, string, int, int) ([]metadata.RemoteImage, map[string]string, error) {
	return []metadata.RemoteImage{{ProviderID: "tmdb", URL: "source", Type: metadata.ImagePoster}}, nil, nil
}
func (f *curationImageService) ApplyItemImage(ctx context.Context, r metadata.ApplyItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	f.calls++
	f.request = r
	f.private = netguard.PrivateAccess(ctx)
	if f.fail {
		return nil, errors.New("synthetic image failure")
	}
	return &metadata.ApplyItemImageResult{StoredPath: f.stored, Thumbhash: "hash", Revision: "revision"}, nil
}
func (f *curationImageService) FetchItemImagesWithLocal(ctx context.Context, providerIDs map[string]string, contentType, language string, folderID int, _ string) ([]metadata.RemoteImage, map[string]string, error) {
	return f.FetchItemImages(ctx, providerIDs, contentType, language, folderID)
}
func (f *curationImageService) LocalImagePreview(context.Context, string, string) (string, error) {
	return "", errors.New("no local images")
}
func (f *curationImageService) ApplyLocalItemImage(context.Context, metadata.ApplyLocalItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	return nil, errors.New("no local images")
}
func (f *curationImageService) ApplyUploadedItemImage(context.Context, metadata.ApplyUploadedItemImageRequest) (*metadata.ApplyUploadedItemImageResult, error) {
	return nil, errors.New("no uploads")
}

// An episode takes its image as a still for its own season and episode, and
// an administrator may apply an image from the local network.
func TestAdminApplyEpisodeImageTargetsTheEpisode(t *testing.T) {
	svc := &curationImageService{fail: true}
	h := NewAdminImageHandler(curationImageItems{"series": {ContentID: "series", Type: "series", TmdbID: "42"}}, curationImageSeasons{}, curationImageEpisodes{}, nil, svc, nil, nil)
	_, err := h.ApplyAdminItemImage(t.Context(), "episode", AdminItemImageRequest{OriginalURL: "source", Type: "poster", ProviderID: "tmdb"})
	if err == nil || svc.calls != 1 || svc.request.ImageType != metadata.ImageStill ||
		svc.request.SeasonNumber == nil || *svc.request.SeasonNumber != 0 ||
		svc.request.EpisodeNumber == nil || *svc.request.EpisodeNumber != 2 {
		t.Fatalf("episode apply %v %+v", err, svc.request)
	}
	if !svc.private {
		t.Fatal("admin apply did not allow a local network image source")
	}
}

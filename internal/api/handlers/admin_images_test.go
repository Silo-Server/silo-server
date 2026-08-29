package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type adminImageItemLookup map[string]*models.MediaItem

func (l adminImageItemLookup) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if item := l[contentID]; item != nil {
		return item, nil
	}
	return nil, catalog.ErrItemNotFound
}

type adminImageSeasonLookup map[string]*models.Season

func (l adminImageSeasonLookup) GetByID(_ context.Context, contentID string) (*models.Season, error) {
	if season := l[contentID]; season != nil {
		return season, nil
	}
	return nil, catalog.ErrSeasonNotFound
}

type adminImageEpisodeLookup map[string]*models.Episode

func (l adminImageEpisodeLookup) GetByID(_ context.Context, contentID string) (*models.Episode, error) {
	if episode := l[contentID]; episode != nil {
		return episode, nil
	}
	return nil, catalog.ErrEpisodeNotFound
}

type successfulAdminImageService struct {
	result metadata.ApplyItemImageResult
}

func (s successfulAdminImageService) FetchItemImages(context.Context, map[string]string, string, string, int) ([]metadata.RemoteImage, map[string]string, error) {
	return nil, nil, nil
}

func (s successfulAdminImageService) ApplyItemImage(context.Context, metadata.ApplyItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	result := s.result
	return &result, nil
}

type recordingAdminImageDetailService struct {
	presigner *catalog.DetailService
	target    artworkurl.Target
	path      string
	imageType string
	size      string
}

func (*recordingAdminImageDetailService) PublishArtworkSelection(context.Context, catalog.ArtworkSelection) error {
	return nil
}

func (*recordingAdminImageDetailService) QueueArtworkRevisionGC(context.Context, string, string, time.Time) error {
	return nil
}

func (s *recordingAdminImageDetailService) PresignArtworkTargetImageURL(
	ctx context.Context,
	target artworkurl.Target,
	path, imageType, size string,
) string {
	s.target, s.path, s.imageType, s.size = target, path, imageType, size
	return s.presigner.PresignArtworkTargetImageURL(ctx, target, path, imageType, size)
}

func TestHandleApplyItemImageReturnsTargetCapabilityURL(t *testing.T) {
	signer, err := artworkurl.NewSigner("admin-image-test-secret", nil)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	artworkResolver, err := artworkurl.NewResolver(signer)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	imageResolver := metadata.NewPluginImageResolver()
	defer imageResolver.Close()
	imageResolver.SetArtworkURLResolver(artworkResolver)
	presigner := &catalog.DetailService{}
	presigner.SetImageResolver(imageResolver)

	parent := &models.MediaItem{ContentID: "series-1", Type: "series", TmdbID: "100"}
	items := adminImageItemLookup{
		"movie-1":         {ContentID: "movie-1", Type: "movie", TmdbID: "200"},
		"series-1":        parent,
		"season-as-item":  {ContentID: "season-as-item", Type: "season", TmdbID: "201"},
		"episode-as-item": {ContentID: "episode-as-item", Type: "episode", TmdbID: "202"},
	}
	seasons := adminImageSeasonLookup{
		"season-1": {ContentID: "season-1", SeriesID: "series-1", SeasonNumber: 1},
	}
	episodes := adminImageEpisodeLookup{
		"episode-1": {ContentID: "episode-1", SeriesID: "series-1", SeasonNumber: 1, EpisodeNumber: 2},
	}

	tests := []struct {
		name        string
		contentID   string
		requestType string
		wantSurface string
		wantSlot    string
	}{
		{name: "item poster", contentID: "movie-1", requestType: "poster", wantSurface: artworkurl.SurfaceItemPosters, wantSlot: artworkImagePoster},
		{name: "item backdrop", contentID: "movie-1", requestType: "backdrop", wantSurface: artworkurl.SurfaceItemBackdrops, wantSlot: artworkImageBackdrop},
		{name: "item logo", contentID: "movie-1", requestType: "logo", wantSurface: artworkurl.SurfaceItemLogos, wantSlot: artworkImageLogo},
		{name: "season poster", contentID: "season-1", requestType: "poster", wantSurface: artworkurl.SurfaceSeasonPosters, wantSlot: artworkImagePoster},
		{name: "episode still", contentID: "episode-1", requestType: "poster", wantSurface: artworkurl.SurfaceEpisodeStills, wantSlot: artworkImageStill},
		{name: "season media item falls back to item poster", contentID: "season-as-item", requestType: "poster", wantSurface: artworkurl.SurfaceItemPosters, wantSlot: artworkImagePoster},
		{name: "episode media item falls back to item poster", contentID: "episode-as-item", requestType: "poster", wantSurface: artworkurl.SurfaceItemPosters, wantSlot: artworkImageStill},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storedPath := "provider/catalog/" + tc.wantSlot + "/original.reviewbot.webp"
			detail := &recordingAdminImageDetailService{presigner: presigner}
			handler := NewAdminImageHandler(
				items,
				seasons,
				episodes,
				nil,
				successfulAdminImageService{result: metadata.ApplyItemImageResult{StoredPath: storedPath, Revision: "reviewbot", Thumbhash: "hash"}},
				nil,
				detail,
			)
			router := chi.NewRouter()
			router.Post("/admin/items/{id}/images/apply", handler.HandleApplyItemImage)

			body := []byte(`{"original_url":"provider://selected","type":"` + tc.requestType + `"}`)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/items/"+tc.contentID+"/images/apply", bytes.NewReader(body)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", recorder.Code, recorder.Body.String())
			}

			var response applyItemImageResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.ImageURL == "" {
				t.Fatal("image_url is empty")
			}
			parsed, err := url.Parse(response.ImageURL)
			if err != nil {
				t.Fatalf("parse image_url: %v", err)
			}
			parts := strings.Split(strings.TrimPrefix(parsed.Path, artworkurl.RoutePrefix), "/")
			if len(parts) != 2 || !strings.HasPrefix(parsed.Path, artworkurl.RoutePrefix) {
				t.Fatalf("image_url path = %q, want target capability route", parsed.Path)
			}
			target, _, err := signer.VerifyTarget(parts[0], parts[1], time.Now())
			if err != nil {
				t.Fatalf("VerifyTarget: %v", err)
			}
			if target.Surface != tc.wantSurface || target.Slot != tc.wantSlot {
				t.Fatalf("target = surface %q slot %q, want %q/%q", target.Surface, target.Slot, tc.wantSurface, tc.wantSlot)
			}
			if len(target.Keys) != 1 || target.Keys[0] != tc.contentID {
				t.Fatalf("target keys = %v, want [%s]", target.Keys, tc.contentID)
			}
			if target.ExpectedRevision != artworkkey.Revision(storedPath) {
				t.Fatalf("target revision = %q, want revision from published path", target.ExpectedRevision)
			}
			if parts[1] != catalog.ArtworkVariantForSize(tc.wantSlot, "") {
				t.Fatalf("variant = %q, want default display variant", parts[1])
			}
			if detail.path != storedPath || detail.imageType != tc.wantSlot || detail.size != "" {
				t.Fatalf("presign args = path %q type %q size %q", detail.path, detail.imageType, detail.size)
			}
		})
	}
}

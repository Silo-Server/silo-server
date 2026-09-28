package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/go-chi/chi/v5"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	fail    bool
	stored  string
}

func (f *curationImageService) FetchItemImages(context.Context, map[string]string, string, string, int) ([]metadata.RemoteImage, map[string]string, error) {
	return []metadata.RemoteImage{{ProviderID: "tmdb", URL: "source", Type: metadata.ImagePoster}}, nil, nil
}
func (f *curationImageService) ApplyItemImage(_ context.Context, r metadata.ApplyItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	f.calls++
	f.request = r
	if f.fail {
		return nil, errors.New("synthetic image failure")
	}
	return &metadata.ApplyItemImageResult{StoredPath: f.stored, Thumbhash: "hash", Revision: "revision"}, nil
}
func TestAdminImageSharedBridgeAndEpisodePreflight(t *testing.T) {
	svc := &curationImageService{fail: true}
	h := NewAdminImageHandler(curationImageItems{"series": {ContentID: "series", Type: "series", TmdbID: "42"}}, curationImageSeasons{}, curationImageEpisodes{}, nil, svc, nil, nil)
	router := chi.NewRouter()
	router.Get("/{id}/images", h.HandleGetItemImages)
	router.Post("/{id}/images/apply", h.HandleApplyItemImage)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/series/images", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"original_url":"source"`) {
		t.Fatalf("bridge %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/series/images/apply", strings.NewReader(`{"original_url":"source","type":"still"}`)))
	if rec.Code != 400 || svc.calls != 0 || !strings.Contains(rec.Body.String(), `"error":"unsupported_image_type"`) {
		t.Fatalf("preflight %d %s", rec.Code, rec.Body)
	}
	_, err := h.ApplyAdminItemImage(t.Context(), "episode", AdminItemImageRequest{OriginalURL: "source", Type: "poster", ProviderID: "tmdb"})
	if err == nil || svc.calls != 1 || svc.request.ImageType != metadata.ImageStill || svc.request.SeasonNumber == nil || *svc.request.SeasonNumber != 0 || svc.request.EpisodeNumber == nil || *svc.request.EpisodeNumber != 2 {
		t.Fatalf("episode %v %+v", err, svc.request)
	}
}
func TestAdminImageSharedPublishesImmutableRevision(t *testing.T) {
	pool := catalogTransferPool(t)
	id := fmt.Sprintf("movie:curation-image-%d", time.Now().UnixNano())
	stored := "tmdb/movies/" + id + "/poster/original.rev.webp"
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title)VALUES($1,'movie','Synthetic image')`, id); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, stored)
	}()
	items := catalog.NewItemRepository(pool)
	detail := catalog.NewDetailService(items, catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool), catalog.NewPersonRepository(pool), nil)
	svc := &curationImageService{stored: stored}
	h := NewAdminImageHandler(items, nil, nil, nil, svc, nil, detail)
	out, err := h.ApplyAdminItemImage(t.Context(), id, AdminItemImageRequest{OriginalURL: "source", Type: "poster", ProviderID: "tmdb"})
	if err != nil || out.StoredPath != stored || out.Revision != "revision" {
		t.Fatalf("publish %+v %v", out, err)
	}
	var path, source string
	var locked []int
	if err := pool.QueryRow(t.Context(), `SELECT poster_path,poster_source_path,locked_fields FROM media_items WHERE content_id=$1`, id).Scan(&path, &source, &locked); err != nil {
		t.Fatal(err)
	}
	if path != stored || source != "source" || len(locked) != 1 || locked[0] != int(metadata.FieldImages) {
		t.Fatalf("persisted %s %s %v", path, source, locked)
	}
}

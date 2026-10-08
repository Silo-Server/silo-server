package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	localPosterURL   = "file:///media/Other/Show/poster.jpg"
	localBackdropURL = "file:///media/Other/Show/fanart.jpg"
	localPreviewURI  = "data:image/webp;base64,UklGRg=="
)

type localImageServiceFake struct {
	plainCalls     int
	localCalls     int
	localContentID string
	providerCalls  int
	applyLocal     []metadata.ApplyLocalItemImageRequest
	applyLocalErr  error
	plainImages    []metadata.RemoteImage
	previewCalls   int
}

func (f *localImageServiceFake) FetchItemImages(context.Context, map[string]string, string, string, int) ([]metadata.RemoteImage, map[string]string, error) {
	f.plainCalls++
	if f.plainImages != nil {
		return f.plainImages, nil, nil
	}
	return []metadata.RemoteImage{{ProviderID: "tmdb", URL: "https://image.example/poster.jpg", Type: metadata.ImagePoster}}, nil, nil
}

func (f *localImageServiceFake) FetchItemImagesWithLocal(_ context.Context, _ map[string]string, _, _ string, _ int, contentID string) ([]metadata.RemoteImage, map[string]string, error) {
	f.localCalls++
	f.localContentID = contentID
	return []metadata.RemoteImage{
		{ProviderID: "tmdb", URL: "https://image.example/poster.jpg", Type: metadata.ImagePoster},
		{ProviderID: "nfo", URL: localPosterURL, Type: metadata.ImagePoster},
		{ProviderID: "nfo", URL: localBackdropURL, Type: metadata.ImageBackdrop},
	}, nil, nil
}

func (f *localImageServiceFake) FetchSeasonImages(context.Context, map[string]string, string, int, int) ([]metadata.RemoteImage, map[string]string, error) {
	return nil, nil, nil
}

// LocalImagePreview previews the poster and fails for the backdrop, the way an
// oversized or unreadable file would.
func (f *localImageServiceFake) LocalImagePreview(_ context.Context, _ string, sourceURL string) (string, error) {
	f.previewCalls++
	if sourceURL == localPosterURL {
		return localPreviewURI, nil
	}
	return "", errors.New("local image exceeds 8388608 byte limit")
}

func (f *localImageServiceFake) ApplyItemImage(context.Context, metadata.ApplyItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	f.providerCalls++
	return nil, errors.New("synthetic provider failure")
}

func (f *localImageServiceFake) ApplyLocalItemImage(_ context.Context, req metadata.ApplyLocalItemImageRequest) (*metadata.ApplyItemImageResult, error) {
	f.applyLocal = append(f.applyLocal, req)
	return nil, f.applyLocalErr
}

func newLocalImageHandlerForTest(svc *localImageServiceFake) *AdminImageHandler {
	items := curationImageItems{
		"local-series": {ContentID: "local-series", Type: "series", DefaultMetadataLanguage: "fr"},
		"series":       {ContentID: "series", Type: "series", TmdbID: "42"},
		"book":         {ContentID: "book", Type: "audiobook"},
	}
	return NewAdminImageHandler(items, curationImageSeasons{}, curationImageEpisodes{}, nil, svc, nil, nil)
}

func TestAdminItemImagesOffersPreviewableLocalImages(t *testing.T) {
	svc := &localImageServiceFake{}
	h := newLocalImageHandlerForTest(svc)

	out, err := h.GetAdminItemImages(context.Background(), "local-series")
	if err != nil {
		t.Fatalf("GetAdminItemImages: %v", err)
	}
	if svc.localCalls != 1 || svc.plainCalls != 0 || svc.localContentID != "local-series" {
		t.Fatalf("fetch calls local=%d plain=%d content=%q, want one local listing for the item", svc.localCalls, svc.plainCalls, svc.localContentID)
	}
	var local []itemImageEntry
	for _, entry := range out.Images {
		if strings.HasPrefix(entry.OriginalURL, "file://") {
			local = append(local, entry)
		}
	}
	// The backdrop cannot be previewed, so it is not offered either.
	if len(local) != 1 {
		t.Fatalf("local choices = %+v, want only the poster", local)
	}
	if got := local[0]; got.ProviderID != "local" || got.URL != localPreviewURI || got.OriginalURL != localPosterURL || got.Type != "poster" {
		t.Fatalf("local choice = %+v", got)
	}
	if len(out.Images) != 2 {
		t.Fatalf("choices = %+v, want the provider poster and the local poster", out.Images)
	}
	// The admin is told why the backdrop is missing.
	// It is told by source only: the preview error stays in the log.
	if got := out.ProviderErrors["local"]; got != "Local image preview failed" {
		t.Fatalf("provider errors = %v, want the fixed local preview failure", out.ProviderErrors)
	}
}

// Only the movie and series listing runs sidecar discovery. A file:// URL a
// provider returns anywhere else is not read, previewed, or labeled local.
func TestAdminItemImagesDoesNotPreviewProviderFileURLs(t *testing.T) {
	svc := &localImageServiceFake{plainImages: []metadata.RemoteImage{
		{ProviderID: "plugin", URL: localPosterURL, Type: metadata.ImagePoster},
	}}
	h := newLocalImageHandlerForTest(svc)

	out, err := h.GetAdminItemImages(context.Background(), "book")
	if err != nil {
		t.Fatalf("GetAdminItemImages: %v", err)
	}
	if svc.previewCalls != 0 {
		t.Fatalf("previewed %d provider file URLs, want none", svc.previewCalls)
	}
	if len(out.Images) != 1 || out.Images[0].ProviderID != "plugin" || out.Images[0].URL != localPosterURL {
		t.Fatalf("choices = %+v, want the provider row unchanged", out.Images)
	}
}

// Frozen v1 keeps its provider-only list.
func TestAdminItemImagesV1ListOmitsLocalImages(t *testing.T) {
	svc := &localImageServiceFake{}
	router := chi.NewRouter()
	router.Get("/{id}/images", newLocalImageHandlerForTest(svc).HandleGetItemImages)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/local-series/images", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "file://") {
		t.Fatalf("v1 list %d %s", rec.Code, rec.Body)
	}
	if svc.localCalls != 0 || svc.plainCalls != 1 {
		t.Fatalf("fetch calls local=%d plain=%d, want the plain provider listing", svc.localCalls, svc.plainCalls)
	}
}

func TestAdminApplyLocalImageValidatesTheTarget(t *testing.T) {
	t.Run("movie or series uses the local apply", func(t *testing.T) {
		svc := &localImageServiceFake{applyLocalErr: metadata.ErrLocalImageNotOffered}
		h := newLocalImageHandlerForTest(svc)

		_, err := h.ApplyAdminItemImage(context.Background(), "local-series", AdminItemImageRequest{OriginalURL: localPosterURL, Type: "poster", ProviderID: "local"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400 for an image the item does not offer", err)
		}
		if svc.providerCalls != 0 || len(svc.applyLocal) != 1 {
			t.Fatalf("provider calls %d, local calls %d", svc.providerCalls, len(svc.applyLocal))
		}
		req := svc.applyLocal[0]
		if req.ContentID != "local-series" || req.ContentType != "series" || req.ImageType != metadata.ImagePoster || req.SourceURL != localPosterURL {
			t.Fatalf("local apply request = %+v", req)
		}
	})

	t.Run("cache failure is an internal error", func(t *testing.T) {
		svc := &localImageServiceFake{applyLocalErr: errors.New("local image missing: poster.jpg")}
		_, err := newLocalImageHandlerForTest(svc).ApplyAdminItemImage(context.Background(), "local-series", AdminItemImageRequest{OriginalURL: localPosterURL, Type: "poster"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want 500", err)
		}
	})

	t.Run("episodes do not take local images", func(t *testing.T) {
		svc := &localImageServiceFake{}
		_, err := newLocalImageHandlerForTest(svc).ApplyAdminItemImage(context.Background(), "episode", AdminItemImageRequest{OriginalURL: localPosterURL, Type: "still"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400", err)
		}
		if len(svc.applyLocal) != 0 || svc.providerCalls != 0 {
			t.Fatalf("local calls %d, provider calls %d, want none", len(svc.applyLocal), svc.providerCalls)
		}
	})

	t.Run("frozen v1 never takes the local path", func(t *testing.T) {
		svc := &localImageServiceFake{}
		router := chi.NewRouter()
		router.Post("/{id}/images/apply", newLocalImageHandlerForTest(svc).HandleApplyItemImage)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/local-series/images/apply", strings.NewReader(`{"original_url":"`+localPosterURL+`","type":"poster"}`)))
		if len(svc.applyLocal) != 0 || svc.providerCalls != 1 {
			t.Fatalf("v1 apply %d: local calls %d, provider calls %d", rec.Code, len(svc.applyLocal), svc.providerCalls)
		}
	})
}

// Only movies and series offer local artwork; other item types keep the
// provider-only list and refuse a local apply.
func TestAdminLocalImagesAreLimitedToMoviesAndSeries(t *testing.T) {
	svc := &localImageServiceFake{}
	h := newLocalImageHandlerForTest(svc)

	if _, err := h.GetAdminItemImages(context.Background(), "book"); err != nil {
		t.Fatalf("GetAdminItemImages: %v", err)
	}
	if svc.localCalls != 0 || svc.plainCalls != 1 {
		t.Fatalf("fetch calls local=%d plain=%d, want the plain provider listing", svc.localCalls, svc.plainCalls)
	}
	_, err := h.ApplyAdminItemImage(context.Background(), "book", AdminItemImageRequest{OriginalURL: localPosterURL, Type: "poster"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || len(svc.applyLocal) != 0 {
		t.Fatalf("err = %v, local calls %d, want 400 and none", err, len(svc.applyLocal))
	}
}

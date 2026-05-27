package jellycompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestProxyImageDefaultsToRevalidatingCachePolicy(t *testing.T) {
	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":  []string{"image/jpeg"},
			"ETag":          []string{`"abc"`},
			"Last-Modified": []string{"Tue, 12 May 2026 12:00:00 GMT"},
			"Expires":       []string{"Tue, 12 May 2026 12:05:00 GMT"},
		},
		Body: io.NopCloser(strings.NewReader("image-bytes")),
	}

	proxyImage(rec, resp)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != defaultImageProxyCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, defaultImageProxyCacheControl)
	}
	if got := rec.Header().Get("ETag"); got != `"abc"` {
		t.Fatalf("ETag = %q", got)
	}
	if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("Cache-Control unexpectedly immutable: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestProxyImageRelaysNotModified(t *testing.T) {
	rec := httptest.NewRecorder()
	resp := &http.Response{
		StatusCode: http.StatusNotModified,
		Header: http.Header{
			"ETag":          []string{`"abc"`},
			"Cache-Control": []string{"public, max-age=60"},
		},
		Body: io.NopCloser(strings.NewReader("")),
	}

	proxyImage(rec, resp)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if got := rec.Header().Get("ETag"); got != `"abc"` {
		t.Fatalf("ETag = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body length = %d, want 0", rec.Body.Len())
	}
}

func TestProxyImageURLForwardsConditionalHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"abc"` {
			t.Fatalf("If-None-Match = %q", got)
		}
		if got := r.Header.Get("If-Modified-Since"); got != "Tue, 12 May 2026 12:00:00 GMT" {
			t.Fatalf("If-Modified-Since = %q", got)
		}
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer upstream.Close()

	h := &ImagesHandler{httpClient: upstream.Client()}
	req := httptest.NewRequest(http.MethodGet, "/Items/1/Images/Primary", nil)
	req.Header.Set("If-None-Match", `"abc"`)
	req.Header.Set("If-Modified-Since", "Tue, 12 May 2026 12:00:00 GMT")
	rec := httptest.NewRecorder()

	h.proxyImageURL(rec, req, upstream.URL)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
}

func TestHandleItemImageAcceptsSignedTagWithoutSessionOrCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	item := &models.MediaItem{
		ContentID:       contentID,
		PosterPath:      upstream.URL,
		PosterThumbhash: "poster-thumbhash",
		UpdatedAt:       updatedAt,
	}
	cfg := &config.Config{Auth: config.AuthConfig{JWTSecret: "image-secret"}}
	tag := newMapper(codec, cfg).itemFromList(upstreamListItem{
		ContentID:       contentID,
		Type:            "movie",
		Title:           "Movie",
		PosterURL:       item.PosterPath,
		PosterPath:      item.PosterPath,
		PosterThumbhash: item.PosterThumbhash,
		UpdatedAt:       item.UpdatedAt,
	}, false, nil, nil).ImageTags["Primary"]
	h := &ImagesHandler{
		codec:      codec,
		httpClient: upstream.Client(),
		images:     NewImageCache(time.Hour, func() time.Time { return updatedAt }),
		itemRepo:   fakeImageItemRepo{item: item},
		imageTags:  newImageTagSigner(cfg.Auth.JWTSecret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?fillHeight=267&fillWidth=474&quality=96&tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "image-bytes" {
		t.Fatalf("body = %q, want image bytes", got)
	}
	if cached, ok := h.images.LookupSized(routeID, "Primary", "", compatRequestImageSize(req, "Primary")); !ok || cached == "" {
		t.Fatal("signed-tag image URL was not cached after resolution")
	}
}

func TestHandleItemImageRejectsUnsignedTagWhenSecretBlank(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	item := &models.MediaItem{
		ContentID:       contentID,
		PosterPath:      "https://cdn.example.test/poster.jpg",
		PosterThumbhash: "poster-thumbhash",
		UpdatedAt:       updatedAt,
	}
	tag := newMapper(codec, &config.Config{}).itemFromList(upstreamListItem{
		ContentID:       contentID,
		Type:            "movie",
		Title:           "Movie",
		PosterURL:       item.PosterPath,
		PosterPath:      item.PosterPath,
		PosterThumbhash: item.PosterThumbhash,
		UpdatedAt:       item.UpdatedAt,
	}, false, nil, nil).ImageTags["Primary"]
	h := &ImagesHandler{
		codec:      codec,
		httpClient: http.DefaultClient,
		itemRepo:   fakeImageItemRepo{item: item},
		imageTags:  newImageTagSigner(""),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s; want 401", rec.Code, rec.Body.String())
	}
}

func TestHandleItemImageAcceptsSignedCanonicalBackdropTagWithoutSessionOrCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("backdrop-bytes"))
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	contentID := "series-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	secret := "image-secret"
	tag := newImageTagSigner(secret).Tag(
		imageTagSeed(contentID, "Backdrop", compatCardImageSize, upstream.URL, "", time.Time{}),
		upstream.URL,
	)
	h := &ImagesHandler{
		codec:      codec,
		httpClient: upstream.Client(),
		images:     NewImageCache(time.Hour, time.Now),
		itemRepo: fakeImageItemRepo{item: &models.MediaItem{
			ContentID:    contentID,
			BackdropPath: upstream.URL,
		}},
		imageTags: newImageTagSigner(secret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Thumb?fillHeight=267&fillWidth=474&quality=96&tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Thumb")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "backdrop-bytes" {
		t.Fatalf("body = %q, want backdrop bytes", got)
	}
}

func TestHandleItemImageAcceptsLibraryPosterTagWithoutSessionOrCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("library-poster"))
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	libraryID := 1
	routeID := codec.EncodeIntID(EncodedIDLibrary, int64(libraryID))
	posterPath := "library-posters/1/original.jpg"
	secret := "image-secret"
	tag := newImageTagSigner(secret).Tag(
		imageTagSeed(routeID, "Primary", compatCardImageSize, posterPath, "", time.Time{}),
		"",
	)
	h := &ImagesHandler{
		codec:        codec,
		httpClient:   upstream.Client(),
		images:       NewImageCache(time.Hour, time.Now),
		folderRepo:   fakeImageFolderRepo{folder: &models.MediaFolder{ID: libraryID, PosterPath: posterPath}},
		posterSigner: fakeLibraryPosterPresigner{url: upstream.URL},
		imageTags:    newImageTagSigner(secret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?fillHeight=267&fillWidth=474&quality=96&tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "library-poster" {
		t.Fatalf("body = %q, want library poster", got)
	}
}

func TestHandleItemImageAcceptsLegacyCachedURLTagWithoutRouteFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("cached-image"))
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	routeID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	cache := NewImageCache(time.Hour, time.Now)
	cache.RememberSized(routeID, "Primary", upstream.URL, compatCardImageSize)
	h := &ImagesHandler{
		codec:      codec,
		httpClient: upstream.Client(),
		images:     cache,
		imageTags:  newImageTagSigner("image-secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tagValue(upstream.URL), nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "cached-image" {
		t.Fatalf("body = %q, want cached image", got)
	}
}

func TestHandleItemImageRevalidatesTagBeforeRouteCacheHit(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte("stale-image"))
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	item := &models.MediaItem{
		ContentID:       contentID,
		PosterPath:      upstream.URL,
		PosterThumbhash: "poster-thumbhash",
		UpdatedAt:       updatedAt,
	}
	cache := NewImageCache(time.Hour, func() time.Time { return updatedAt })
	cache.RememberSized(routeID, "Primary", upstream.URL, compatCardImageSize)
	tag := newMapper(codec, &config.Config{
		Auth: config.AuthConfig{JWTSecret: "old-secret"},
	}).itemFromList(upstreamListItem{
		ContentID:       contentID,
		Type:            "movie",
		Title:           "Movie",
		PosterURL:       item.PosterPath,
		PosterPath:      item.PosterPath,
		PosterThumbhash: item.PosterThumbhash,
		UpdatedAt:       item.UpdatedAt,
	}, false, nil, nil).ImageTags["Primary"]
	h := &ImagesHandler{
		codec:      codec,
		httpClient: upstream.Client(),
		images:     cache,
		itemRepo:   fakeImageItemRepo{item: item},
		imageTags:  newImageTagSigner("new-secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s; want 401", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("served cached image before validating the signed tag")
	}
}

type fakeImageItemRepo struct {
	item *models.MediaItem
}

func (r fakeImageItemRepo) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if r.item != nil && r.item.ContentID == contentID {
		return r.item, nil
	}
	return nil, catalog.ErrItemNotFound
}

func (r fakeImageItemRepo) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

type fakeImageFolderRepo struct {
	folder *models.MediaFolder
}

func (r fakeImageFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if r.folder != nil && r.folder.ID == id {
		return r.folder, nil
	}
	return nil, catalog.ErrFolderNotFound
}

type fakeLibraryPosterPresigner struct {
	url string
}

func (p fakeLibraryPosterPresigner) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return p.url, nil
}

func (p fakeLibraryPosterPresigner) Bucket() string {
	return "test-bucket"
}

func withImageRouteParams(r *http.Request, routeID, imageType string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	routeCtx.URLParams.Add("imageType", imageType)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

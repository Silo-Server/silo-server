package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestHandleItemImageAcceptsSignedTagWithoutSessionOrCache(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusTeapot)
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
		codec:     codec,
		images:    NewImageCache(time.Hour, func() time.Time { return updatedAt }),
		itemRepo:  fakeImageItemRepo{item: item},
		imageTags: newImageTagSigner(cfg.Auth.JWTSecret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?fillHeight=267&fillWidth=474&quality=96&tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	assertImageRedirect(t, rec, upstream.URL)
	if upstreamCalled {
		t.Fatal("compat image route proxied the upstream image instead of redirecting")
	}
	if cached, ok := h.images.LookupSized(routeID, "Primary", "", compatRequestImageSize(req, "Primary")); !ok || cached == "" {
		t.Fatal("signed-tag image URL was not cached after resolution")
	}
}

func TestHandleItemImageProxiesInfuseSignedTagWithoutSessionOrCache(t *testing.T) {
	upstreamCalled := false
	var gotIfNoneMatch string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("Cache-Control", "public, max-age=14400")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("ETag", `"poster-v1"`)
		w.WriteHeader(http.StatusOK)
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
		images:     NewImageCache(time.Hour, func() time.Time { return updatedAt }),
		itemRepo:   fakeImageItemRepo{item: item},
		imageTags:  newImageTagSigner(cfg.Auth.JWTSecret),
		httpClient: upstream.Client(),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?fillHeight=267&fillWidth=474&quality=96&tag="+compatImageProxyTag(tag), nil)
	req.Header.Set("If-None-Match", `"poster-v1"`)
	req.Header.Set("User-Agent", "Infuse-Direct/8.4.6")
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "image-bytes" {
		t.Fatalf("body = %q, want image-bytes", got)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want empty", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != compatImageRouteCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, compatImageRouteCacheControl)
	}
	if got := rec.Header().Get("CDN-Cache-Control"); got != "private, no-store, no-cache, max-age=0" {
		t.Fatalf("CDN-Cache-Control = %q, want private, no-store, no-cache, max-age=0", got)
	}
	if got := rec.Header().Get("X-Accel-Expires"); got != "0" {
		t.Fatalf("X-Accel-Expires = %q, want 0", got)
	}
	if got := gotIfNoneMatch; got != `"poster-v1"` {
		t.Fatalf("forwarded If-None-Match = %q, want poster-v1", got)
	}
	if !upstreamCalled {
		t.Fatal("Infuse compat image route did not proxy the upstream image")
	}
}

func TestHandleItemImageProxyRouteIDUsesCanonicalItemAndProxy(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.Header().Set("Content-Type", "image/webp")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxy-route-image"))
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	proxyRouteID := compatImageProxyRouteID(codec, routeID)
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
		images:     NewImageCache(time.Hour, func() time.Time { return updatedAt }),
		itemRepo:   fakeImageItemRepo{item: item},
		imageTags:  newImageTagSigner(cfg.Auth.JWTSecret),
		httpClient: upstream.Client(),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+proxyRouteID+"/Images/Primary?fillHeight=267&fillWidth=474&quality=96&tag="+compatImageProxyTag(tag), nil)
	req = withImageRouteParams(req, proxyRouteID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "proxy-route-image" {
		t.Fatalf("body = %q, want proxy-route-image", got)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want empty", got)
	}
	if cached, ok := h.images.LookupSized(routeID, "Primary", "", compatRequestImageSize(req, "Primary")); !ok || cached == "" {
		t.Fatal("proxy route image URL was not cached under the canonical route ID")
	}
	if !upstreamCalled {
		t.Fatal("proxy route did not fetch the upstream image")
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
		codec:     codec,
		itemRepo:  fakeImageItemRepo{item: item},
		imageTags: newImageTagSigner(""),
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
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusTeapot)
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
		codec:  codec,
		images: NewImageCache(time.Hour, time.Now),
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

	assertImageRedirect(t, rec, upstream.URL)
	if upstreamCalled {
		t.Fatal("compat image route proxied the upstream image instead of redirecting")
	}
}

func TestHandleItemImageAcceptsLibraryPosterTagWithoutSessionOrCache(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusTeapot)
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
		images:       NewImageCache(time.Hour, time.Now),
		folderRepo:   fakeImageFolderRepo{folder: &models.MediaFolder{ID: libraryID, PosterPath: posterPath}},
		posterSigner: fakeLibraryPosterPresigner{url: upstream.URL},
		imageTags:    newImageTagSigner(secret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?fillHeight=267&fillWidth=474&quality=96&tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	assertImageRedirect(t, rec, upstream.URL)
	if upstreamCalled {
		t.Fatal("compat image route proxied the upstream image instead of redirecting")
	}
}

func TestHandleItemImageAcceptsLegacyCachedURLTagWithoutRouteFallback(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	routeID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	cache := NewImageCache(time.Hour, time.Now)
	cache.RememberSized(routeID, "Primary", upstream.URL, compatCardImageSize)
	h := &ImagesHandler{
		codec:     codec,
		images:    cache,
		imageTags: newImageTagSigner("image-secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tagValue(upstream.URL), nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	assertImageRedirect(t, rec, upstream.URL)
	if upstreamCalled {
		t.Fatal("compat image route proxied the upstream image instead of redirecting")
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
		codec:     codec,
		images:    cache,
		itemRepo:  fakeImageItemRepo{item: item},
		imageTags: newImageTagSigner("new-secret"),
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

func TestRedirectImageURLRejectsNonHTTPURL(t *testing.T) {
	h := &ImagesHandler{}
	req := httptest.NewRequest(http.MethodGet, "/Items/1/Images/Primary", nil)
	rec := httptest.NewRecorder()

	h.redirectImageURL(rec, req, "catalog/poster.jpg")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s; want 502", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want empty", got)
	}
}

func assertImageRedirect(t *testing.T, rec *httptest.ResponseRecorder, wantLocation string) {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s; want 302", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != wantLocation {
		t.Fatalf("Location = %q, want %q", got, wantLocation)
	}
	if got := rec.Header().Get("Cache-Control"); got != compatImageRouteCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, compatImageRouteCacheControl)
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

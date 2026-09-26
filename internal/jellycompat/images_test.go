package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
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

// TestHandleItemImageReadsTagInAnyQueryCase covers jellyfin-kodi, which sends
// "Tag=" and no auth. Jellyfin binds query parameters case-insensitively, so a
// signed tag authorizes the request in any casing, even on a node whose image
// cache has never seen the item.
func TestHandleItemImageReadsTagInAnyQueryCase(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	posterURL := "https://cdn.example.test/poster.jpg"
	item := &models.MediaItem{
		ContentID:       contentID,
		PosterPath:      posterURL,
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

	for _, param := range []string{"tag", "Tag", "TAG"} {
		t.Run(param, func(t *testing.T) {
			h := &ImagesHandler{
				codec:     codec,
				images:    NewImageCache(time.Hour, func() time.Time { return updatedAt }),
				itemRepo:  fakeImageItemRepo{item: item},
				imageTags: newImageTagSigner(cfg.Auth.JWTSecret),
			}
			// The URL jellyfin-kodi builds in get_artwork.
			req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary/0?Format=original&"+param+"="+tag, nil)
			req = withImageRouteParams(req, routeID, "Primary")
			rec := httptest.NewRecorder()

			h.HandleItemImage(rec, req)

			assertImageRedirect(t, rec, posterURL)
		})
	}

	proxyReq := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?Tag="+compatImageProxyTag(tag), nil)
	if !shouldProxyCompatImageRequest(proxyReq) {
		t.Fatal("a proxy tag sent as Tag= did not select the proxy path")
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

	// An unsigned/invalid tag must not serve the image. Per the Jellyfin
	// contract (item-image GETs are anonymous, 200/404 only) the rejection
	// surfaces as a 404, not a 401.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
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

func TestLocalArtworkUsesRelativeRedirect(t *testing.T) {
	h := &ImagesHandler{}
	u := "/api/v2/artwork/provider/images/poster.webp?exp=123&sig=test"
	rec := httptest.NewRecorder()
	h.serveImageURL(rec, httptest.NewRequest(http.MethodGet, "/Items/id/Images/Primary", nil), u)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != u || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("redirect: %d %v", rec.Code, rec.Header())
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

	// A stale tag (signed with the old secret) must not serve the cached image.
	// Per the Jellyfin contract the rejection surfaces as a 404, not a 401.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
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

// TestHandleItemImageChapterReturns404WithoutSession verifies an anonymous
// chapter-image request (no auth, cold cache, no tag) degrades to a 404, not a
// 401: Silo never stores "Chapter" route art, so the cache misses and the
// session-fallback now returns NotFound per the Jellyfin contract.
func TestHandleItemImageChapterReturns404WithoutSession(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	h := &ImagesHandler{
		codec:     codec,
		images:    NewImageCache(time.Hour, time.Now),
		itemRepo:  fakeImageItemRepo{item: &models.MediaItem{ContentID: contentID}},
		imageTags: newImageTagSigner("image-secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Chapter/0", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	routeCtx.URLParams.Add("imageType", "Chapter")
	routeCtx.URLParams.Add("index", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want empty", got)
	}
}

// TestHandleItemImagePrimaryCacheHitRedirectsWithoutSession verifies a warm
// Primary cache entry serves an anonymous <img> request via redirect, never
// consulting auth.
func TestHandleItemImagePrimaryCacheHitRedirectsWithoutSession(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	upstreamURL := "https://cdn.example.test/poster.jpg"
	h := &ImagesHandler{
		codec:     codec,
		images:    NewImageCache(time.Hour, time.Now),
		itemRepo:  fakeImageItemRepo{item: &models.MediaItem{ContentID: contentID}},
		imageTags: newImageTagSigner("image-secret"),
	}
	h.images.RememberSized(routeID, "Primary", upstreamURL, compatCardImageSize)

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary", nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	assertImageRedirect(t, rec, upstreamURL)
}

// serveUserImage issues an avatar request for pathID with the chi "id" route
// param and no session in context.
func serveUserImage(h *ImagesHandler, method, pathID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/Users/"+pathID+"/Images/Primary", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", pathID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	h.HandleUserImage(rec, req)
	return rec
}

// TestHandleUserImageServesPlaceholderWithoutSession verifies the user-avatar
// route serves a placeholder PNG without a session: it is byte-stable per id and
// the palette varies across ids (the avatar is now drawn from a bounded fixed
// palette, so two distinct ids may collide — variety is asserted over a sample).
func TestHandleUserImageServesPlaceholderWithoutSession(t *testing.T) {
	h := &ImagesHandler{codec: NewResourceIDCodec()}
	id := PseudoUserID(1, "profile-1").String()

	rec := serveUserImage(h, http.MethodGet, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("expected a non-empty avatar body")
	}

	again := serveUserImage(h, http.MethodGet, id)
	if got := again.Body.Bytes(); string(got) != string(body) {
		t.Fatal("avatar bytes for the same path id must be stable across calls")
	}

	// The palette is bounded, so individual ids may collide; assert variety over
	// a sample instead of strict per-id divergence.
	distinct := map[string]struct{}{}
	for i := 0; i < 12; i++ {
		out := serveUserImage(h, http.MethodGet, PseudoUserID(i+10, "profile").String())
		distinct[out.Body.String()] = struct{}{}
	}
	if len(distinct) < 2 {
		t.Fatalf("expected the avatar palette to vary across ids; got %d distinct outputs", len(distinct))
	}
}

// TestHandleUserImageHeadRequest verifies the HEAD variant of the anonymous
// avatar route returns 200 + image/png (the body may be empty for HEAD).
func TestHandleUserImageHeadRequest(t *testing.T) {
	h := &ImagesHandler{codec: NewResourceIDCodec()}
	id := PseudoUserID(1, "profile-1").String()

	rec := serveUserImage(h, http.MethodHead, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
}

// TestHandlePersonImageClampsLargeRequestToProfileLadder verifies a large-ish
// person-image request resolves a w500 headshot. Profiles ride the {500, 300}
// ladder, so resolving them as posters would name a profile/w780 key that is
// never generated, and the server-side ladder fallback skips profile.
func TestHandlePersonImageClampsLargeRequestToProfileLadder(t *testing.T) {
	codec := NewResourceIDCodec()
	routeID := codec.EncodeIntID(EncodedIDPerson, 287)
	photoPath := "tmdb/people/287/profile/original.abc123.webp"

	resolver := &recordingImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)

	h := &ImagesHandler{
		codec:      codec,
		images:     NewImageCache(time.Hour, time.Now),
		personRepo: fakeImagePersonRepo{person: &models.Person{ID: 287, Name: "Brad Pitt", PhotoPath: photoPath}},
		detailSvc:  detailSvc,
		imageTags:  newImageTagSigner("image-secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?MaxWidth=900", nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.handlePersonImage(rec, req, &Session{}, routeID, "Primary", "", 287)

	if got := compatRequestImageSize(req, "Primary"); got != compatLargeImageSize {
		t.Fatalf("compatRequestImageSize = %q, want %q", got, compatLargeImageSize)
	}
	want := "tmdb/people/287/profile/w500.abc123.webp"
	if resolver.path != want {
		t.Fatalf("resolved path = %q, want %q", resolver.path, want)
	}
	assertImageRedirect(t, rec, "https://cdn.example.test/"+want)
}

type recordingImageResolver struct {
	path    string
	variant string
}

func (r *recordingImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.path = path
	r.variant = variant
	return "https://cdn.example.test/" + path
}

func (r *recordingImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

type fakeImagePersonRepo struct {
	person *models.Person
}

func (r fakeImagePersonRepo) Get(_ context.Context, id int64) (*models.Person, error) {
	if r.person != nil && r.person.ID == id {
		return r.person, nil
	}
	return nil, errors.New("person not found")
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

func (r fakeImagePersonRepo) EnsureAccessible(_ context.Context, id int64, filter catalog.AccessFilter) error {
	if r.person == nil || r.person.ID != id || (filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0) {
		return pgx.ErrNoRows
	}
	return nil
}

func TestPersonImageRechecksViewerBeforeSharedCache(t *testing.T) {
	codec := NewResourceIDCodec()
	routeID := codec.EncodeIntID(EncodedIDPerson, 287)
	resolver := &recordingImageResolver{}
	detail := &catalog.DetailService{}
	detail.SetImageResolver(resolver)
	h := &ImagesHandler{codec: codec, images: NewImageCache(time.Hour, time.Now), personRepo: fakeImagePersonRepo{person: &models.Person{ID: 287, PhotoPath: "tmdb/people/287/profile/original.abc123.webp"}}, detailSvc: detail,
		accessFilter: func(_ context.Context, _ int, profileID string) catalog.AccessFilter {
			if profileID == "visible" {
				return catalog.AccessFilter{}
			}
			return catalog.AccessFilter{AllowedLibraryIDs: []int{}}
		}}
	request := func(profile string, authenticated bool, tag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+tag, nil)
		req = withImageRouteParams(req, routeID, "Primary")
		if authenticated {
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 7, ProfileID: profile}))
		}
		rr := httptest.NewRecorder()
		h.HandleItemImage(rr, req)
		return rr
	}
	first := request("visible", true, "")
	if first.Code != http.StatusFound {
		t.Fatalf("visible first=%d %s", first.Code, first.Body.String())
	}
	if _, ok := h.images.LookupSized(personImageCacheRouteID(routeID, "tmdb/people/287/profile/original.abc123.webp"), "Primary", "", compatCardImageSize); !ok {
		t.Fatal("authorized request did not warm shared cache")
	}
	legacyTag := tagValue(first.Header().Get("Location"))
	for _, tc := range []struct {
		profile       string
		authenticated bool
		tag           string
	}{{"hidden", true, ""}, {"", false, ""}, {"hidden", true, legacyTag}, {"", false, legacyTag}} {
		rr := request(tc.profile, tc.authenticated, tc.tag)
		if rr.Code != http.StatusNotFound || rr.Header().Get("Location") != "" {
			t.Fatalf("cache disclosure for %+v: status=%d location=%q", tc, rr.Code, rr.Header().Get("Location"))
		}
	}
	if rr := request("visible", true, ""); rr.Code != http.StatusFound {
		t.Fatalf("authorized cache hit=%d", rr.Code)
	}
}

type countingImageResolver struct {
	paths []string
}

func (r *countingImageResolver) ResolveImageURL(_ context.Context, path string, _ string) string {
	r.paths = append(r.paths, path)
	return "https://cdn.example.test/" + path
}

func (r *countingImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

// TestHandleItemImagePresignsOnlyRequestedType checks that a tagged image
// request presigns the requested type, and its fallback only when the
// requested type has no artwork.
func TestHandleItemImagePresignsOnlyRequestedType(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	const (
		posterPath   = "tmdb/movies/1/poster/original.abc.webp"
		backdropPath = "tmdb/movies/1/backdrop/original.def.webp"
		logoPath     = "tmdb/movies/1/logo/original.ghi.webp"
	)
	signer := newImageTagSigner("image-secret")

	tests := []struct {
		name       string
		item       models.MediaItem
		imageType  string
		tagType    string
		tagPath    string
		wantPrefix []string
	}{
		{name: "primary", imageType: "Primary", tagType: "Primary", tagPath: posterPath, wantPrefix: []string{"tmdb/movies/1/poster/"}},
		{name: "backdrop", imageType: "Backdrop", tagType: "Backdrop", tagPath: backdropPath, wantPrefix: []string{"tmdb/movies/1/backdrop/"}},
		{name: "thumb", imageType: "Thumb", tagType: "Backdrop", tagPath: backdropPath, wantPrefix: []string{"tmdb/movies/1/backdrop/"}},
		{name: "logo", imageType: "Logo", tagType: "Logo", tagPath: logoPath, wantPrefix: []string{"tmdb/movies/1/logo/"}},
		{
			name:       "primary falls back to backdrop",
			item:       models.MediaItem{PosterPath: "-"},
			imageType:  "Primary",
			tagType:    "Primary",
			tagPath:    "-",
			wantPrefix: []string{"tmdb/movies/1/backdrop/"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &models.MediaItem{
				ContentID:    contentID,
				PosterPath:   posterPath,
				BackdropPath: backdropPath,
				LogoPath:     logoPath,
				UpdatedAt:    updatedAt,
			}
			if tt.item.PosterPath != "" {
				item.PosterPath = tt.item.PosterPath
			}
			resolver := &countingImageResolver{}
			detailSvc := &catalog.DetailService{}
			detailSvc.SetImageResolver(resolver)
			h := &ImagesHandler{
				codec:     codec,
				images:    NewImageCache(time.Hour, time.Now),
				itemRepo:  fakeImageItemRepo{item: item},
				detailSvc: detailSvc,
				imageTags: signer,
			}
			tag := signer.Tag(imageTagSeed(contentID, tt.tagType, compatCardImageSize, tt.tagPath, "", updatedAt), tt.tagPath)

			req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/"+tt.imageType+"?tag="+tag, nil)
			req = withImageRouteParams(req, routeID, tt.imageType)
			rec := httptest.NewRecorder()
			h.HandleItemImage(rec, req)

			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, body = %s; want 302", rec.Code, rec.Body.String())
			}
			if len(resolver.paths) != len(tt.wantPrefix) {
				t.Fatalf("presigned %d paths %v, want %d", len(resolver.paths), resolver.paths, len(tt.wantPrefix))
			}
			for i, prefix := range tt.wantPrefix {
				if !strings.HasPrefix(resolver.paths[i], prefix) {
					t.Fatalf("presign %d = %q, want prefix %q", i, resolver.paths[i], prefix)
				}
			}
		})
	}
}

// TestHandleItemImageServesKodiTagAfterRouteEviction covers the bounded cache
// on a large library: a Kodi-style request ("Tag=", no auth) for an item whose
// route entry was evicted still resolves through its signed tag.
func TestHandleItemImageServesKodiTagAfterRouteEviction(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	posterURL := "https://cdn.example.test/poster.jpg"
	item := &models.MediaItem{
		ContentID:       contentID,
		PosterPath:      posterURL,
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

	cache := NewImageCache(time.Hour, func() time.Time { return updatedAt })
	cache.RememberSized(routeID, "Primary", posterURL, compatCardImageSize)
	for i := range imageCacheMaxEntries {
		cache.RememberSized(fmt.Sprintf("filler-%d", i), "Primary", fmt.Sprintf("https://cdn.example.test/%d.jpg", i), compatCardImageSize)
	}
	if _, ok := cache.LookupSized(routeID, "Primary", "", compatCardImageSize); ok {
		t.Fatal("route entry survived a full cache of newer writes; the test no longer exercises eviction")
	}
	h := &ImagesHandler{
		codec:     codec,
		images:    cache,
		itemRepo:  fakeImageItemRepo{item: item},
		imageTags: newImageTagSigner(cfg.Auth.JWTSecret),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary/0?Format=original&Tag="+tag, nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	assertImageRedirect(t, rec, posterURL)
}

// TestSearchHintImageTagResolvesThroughTagCache pins why the cache keeps its
// tag map when image tags are signed: /Search/Hints emits URL-derived tags,
// which only the tag map can answer for a sessionless image request. The
// short-lived S3 cases cover a direct-S3 deployment whose
// s3.metadata_presign_expiry is five minutes or less.
func TestSearchHintImageTagResolvesThroughTagCache(t *testing.T) {
	now := fixedNow()
	s3URL := func(lifetime time.Duration) string {
		return fmt.Sprintf("https://bucket.s3.example.test/posters/movie-1.jpg?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=%s&X-Amz-Expires=%d&X-Amz-Signature=sig",
			now.Format("20060102T150405Z"), int(lifetime.Seconds()))
	}
	tests := []struct {
		name      string
		posterURL string
	}{
		{name: "unsigned passthrough", posterURL: "https://cdn.example.test/poster.jpg"},
		{name: "s3 presign 60s", posterURL: s3URL(time.Minute)},
		{name: "s3 presign 5m", posterURL: s3URL(5 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codec := NewResourceIDCodec()
			contentID := "movie-1"
			cfg := &config.Config{Auth: config.AuthConfig{JWTSecret: "image-secret"}}
			cache := NewImageCache(time.Hour, func() time.Time { return now })
			items := &ItemsHandler{
				content: &recordingSearchContentService{result: &upstreamBrowseResponse{Items: []upstreamListItem{{
					ContentID:  contentID,
					Type:       "movie",
					Title:      "Movie",
					PosterURL:  tt.posterURL,
					PosterPath: "posters/movie-1.jpg",
				}}}},
				userData: &mockUserDataService{},
				codec:    codec,
				mapper:   newMapper(codec, cfg),
				images:   cache,
			}
			searchReq := httptest.NewRequest(http.MethodGet, "/Search/Hints?SearchTerm=movie", nil)
			searchReq = searchReq.WithContext(context.WithValue(searchReq.Context(), compatSessionKey, &Session{
				StreamAppUserID: 1,
				ProfileID:       "profile-1",
			}))
			searchRec := httptest.NewRecorder()
			items.HandleSearchHints(searchRec, searchReq)
			var hints searchHintResultDTO
			if err := json.Unmarshal(searchRec.Body.Bytes(), &hints); err != nil || len(hints.SearchHints) != 1 {
				t.Fatalf("search hints: status %d, body %s, err %v", searchRec.Code, searchRec.Body.String(), err)
			}
			hint := hints.SearchHints[0]
			if hint.PrimaryImageTag == "" {
				t.Fatal("search hint has no primary image tag")
			}

			h := &ImagesHandler{
				codec:     codec,
				images:    cache,
				itemRepo:  fakeImageItemRepo{item: &models.MediaItem{ContentID: contentID, PosterPath: "posters/movie-1.jpg"}},
				imageTags: newImageTagSigner(cfg.Auth.JWTSecret),
			}
			req := httptest.NewRequest(http.MethodGet, "/Items/"+hint.ItemID+"/Images/Primary?tag="+hint.PrimaryImageTag, nil)
			req = withImageRouteParams(req, hint.ItemID, "Primary")
			rec := httptest.NewRecorder()

			h.HandleItemImage(rec, req)

			assertImageRedirect(t, rec, tt.posterURL)
		})
	}
}

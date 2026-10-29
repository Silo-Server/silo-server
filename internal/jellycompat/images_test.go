package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// TestItemLogoTagIsAdvertisedAndLogoServedAnonymously covers clients such as
// Neptune that check ImageTags for a logo and then fetch it without auth.
// TestHandleItemImagePresignsOnlyRequestedType covers the anonymous fetch.
func TestItemLogoTagIsAdvertisedAndLogoServedAnonymously(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	contentID := "series-1"
	updatedAt := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	item := &models.MediaItem{
		ContentID:  contentID,
		PosterPath: "metadb://poster/series-1",
		LogoPath:   upstream.URL,
		UpdatedAt:  updatedAt,
	}
	cfg := &config.Config{Auth: config.AuthConfig{JWTSecret: "image-secret"}}
	m := newMapper(codec, cfg)
	listItem := upstreamListItem{
		ContentID:  contentID,
		Type:       "series",
		Title:      "Series",
		PosterURL:  item.PosterPath,
		PosterPath: item.PosterPath,
		LogoURL:    item.LogoPath,
		LogoPath:   item.LogoPath,
		UpdatedAt:  item.UpdatedAt,
	}
	tag := m.itemFromList(listItem, false, nil, nil).ImageTags["Logo"]
	if tag == "" {
		t.Fatal("series with a logo did not advertise a Logo image tag")
	}

	detail := m.itemFromDetail(upstreamItemDetail{
		ContentID: contentID,
		Type:      "series",
		Title:     "Series",
		LogoURL:   item.LogoPath,
		LogoPath:  item.LogoPath,
		UpdatedAt: item.UpdatedAt,
	}, false, nil)
	if detail.ImageTags["Logo"] != tag {
		t.Fatalf("detail Logo tag = %q, want the list tag %q", detail.ImageTags["Logo"], tag)
	}

	withoutLogo := listItem
	withoutLogo.LogoURL, withoutLogo.LogoPath = "", ""
	if _, ok := m.itemFromList(withoutLogo, false, nil, nil).ImageTags["Logo"]; ok {
		t.Fatal("item without a logo advertised a Logo image tag")
	}
	episode := listItem
	episode.Type = "episode"
	if _, ok := m.itemFromList(episode, false, nil, nil).ImageTags["Logo"]; ok {
		t.Fatal("episode advertised its own Logo image tag")
	}
}

// TestHandleItemImageReadsTagInAnyQueryCase covers jellyfin-kodi, which sends
// "Tag=" and no auth. Jellyfin binds query parameters case-insensitively, so a
// proxy tag sent as "Tag=" must still select the proxy path.
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
	tag := movieListPrimaryImageTag(codec, cfg.Auth.JWTSecret, item)

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
	tag := movieListPrimaryImageTag(codec, cfg.Auth.JWTSecret, item)
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
	tag := movieListPrimaryImageTag(codec, cfg.Auth.JWTSecret, item)
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

// A proxied poster may reach the server's local network only when it is a
// direct URL to the artwork store. A metadata provider's URL, kept as the
// poster when caching it failed, reaches public addresses only.
func TestHandleItemImageProxyKeepsProviderURLsOffTheLocalNetwork(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("local-image"))
	}))
	defer upstream.Close()

	for _, tc := range []struct {
		name      string
		storeBase string
		wantCode  int
		wantHits  int32
	}{
		{name: "provider URL", storeBase: "http://storage.invalid", wantCode: http.StatusBadGateway, wantHits: 0},
		{name: "artwork store URL", storeBase: upstream.URL, wantCode: http.StatusOK, wantHits: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			codec := NewResourceIDCodec()
			contentID := "movie-1"
			routeID := codec.EncodeStringID(EncodedIDItem, contentID)
			updatedAt := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
			item := &models.MediaItem{ContentID: contentID, PosterPath: upstream.URL + "/poster.webp", UpdatedAt: updatedAt}
			cfg := &config.Config{Auth: config.AuthConfig{JWTSecret: "image-secret"}}
			tag := movieListPrimaryImageTag(codec, cfg.Auth.JWTSecret, item)
			h := &ImagesHandler{
				codec:        codec,
				images:       NewImageCache(time.Hour, func() time.Time { return updatedAt }),
				itemRepo:     fakeImageItemRepo{item: item},
				imageTags:    newImageTagSigner(cfg.Auth.JWTSecret),
				httpClient:   newCompatImageClient(),
				artworkStore: blobstoretest.NewDirect(tc.storeBase),
			}

			req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?tag="+compatImageProxyTag(tag), nil)
			req = withImageRouteParams(req, routeID, "Primary")
			rec := httptest.NewRecorder()
			h.HandleItemImage(rec, req)

			if rec.Code != tc.wantCode || hits.Load() != tc.wantHits {
				t.Fatalf("status = %d, upstream hits = %d, body = %q; want %d and %d hits", rec.Code, hits.Load(), rec.Body.String(), tc.wantCode, tc.wantHits)
			}
			if tc.wantCode != http.StatusOK && strings.Contains(rec.Body.String(), "local-image") {
				t.Fatalf("body leaked the local response: %q", rec.Body.String())
			}
		})
	}
}

// A stored artwork URL can point at any host, so a proxied body is served only
// as an image, and never in a form a browser would render as a document.
func TestProxyImageServesOnlyImages(t *testing.T) {
	for _, tc := range []struct {
		name            string
		declared        string
		body            []byte
		wantCode        int
		wantContentType string
	}{
		{name: "declared image", declared: "image/webp", body: []byte("webp-bytes"), wantCode: http.StatusOK, wantContentType: "image/webp"},
		{name: "image labeled octet-stream", declared: "application/octet-stream", body: avatarPalette[0], wantCode: http.StatusOK, wantContentType: "image/png"},
		{name: "html", declared: "text/html", body: []byte("<html><script>alert(1)</script></html>"), wantCode: http.StatusBadGateway},
		{name: "unlabeled html", body: []byte("<html><script>alert(1)</script></html>"), wantCode: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(tc.body))}
			if tc.declared != "" {
				resp.Header.Set("Content-Type", tc.declared)
			}
			rec := httptest.NewRecorder()
			proxyImage(rec, resp)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, body = %q; want %d", rec.Code, rec.Body.String(), tc.wantCode)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
				t.Fatalf("Content-Security-Policy = %q, want default-src 'none'; sandbox", got)
			}
			if tc.wantCode != http.StatusOK {
				if strings.Contains(rec.Body.String(), "script") {
					t.Fatalf("body leaked the upstream document: %q", rec.Body.String())
				}
				return
			}
			if got := rec.Header().Get("Content-Type"); got != tc.wantContentType {
				t.Fatalf("Content-Type = %q, want %q", got, tc.wantContentType)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.body) {
				t.Fatalf("body = %q, want the upstream bytes", rec.Body.String())
			}
		})
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

func TestHandleItemImageServesCurrentImageForTagSignedWithOldSecret(t *testing.T) {
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
	cache.RememberSized(routeID, "Primary", "https://cdn.example.test/previous-poster.jpg", compatCardImageSize)
	tag := movieListPrimaryImageTag(codec, "old-secret", item)
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

	// A tag signed with an old secret is only a stale cache hint. The item's
	// current image is served, not the older URL this node's route cache holds.
	assertImageRedirect(t, rec, upstream.URL)
	if called {
		t.Fatal("compat image route proxied the upstream image instead of redirecting")
	}
}

// TestHandleItemImageHidesExcludedMediaTypesFromAnonymousRequests keeps the
// anonymous path to what the compat surface exposes: audiobook and podcast
// artwork stays behind the native and ABS APIs.
func TestHandleItemImageHidesExcludedMediaTypesFromAnonymousRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	codec := NewResourceIDCodec()
	contentID := "audiobook-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	h := &ImagesHandler{
		codec:     codec,
		images:    NewImageCache(time.Hour, time.Now),
		itemRepo:  fakeImageItemRepo{item: &models.MediaItem{ContentID: contentID, Type: "audiobook", PosterPath: upstream.URL}},
		imageTags: newImageTagSigner("image-secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary", nil)
	req = withImageRouteParams(req, routeID, "Primary")
	rec := httptest.NewRecorder()

	h.HandleItemImage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
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

	// Roku probes artwork with HEAD and shows it only on a 200. Following a
	// redirect would send the HEAD to a URL presigned for GET. Locally served
	// artwork resolves to Silo's own signed artwork route.
	for _, imageURL := range []string{upstreamURL, "/api/v2/artwork/poster-token"} {
		h.images.RememberSized(routeID, "Primary", imageURL, compatCardImageSize)
		req = httptest.NewRequest(http.MethodHead, "/Items/"+routeID+"/Images/Primary", nil)
		req = withImageRouteParams(req, routeID, "Primary")
		rec = httptest.NewRecorder()
		h.HandleItemImage(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" || rec.Body.Len() != 0 {
			t.Fatalf("HEAD %s = %d, Location %q, %d body bytes; want 200 with no redirect or body", imageURL, rec.Code, rec.Header().Get("Location"), rec.Body.Len())
		}
	}
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

// movieListPrimaryImageTag is the Primary image tag a list response advertises
// for the movie item, with image tags signed by secret.
func movieListPrimaryImageTag(codec *ResourceIDCodec, secret string, item *models.MediaItem) string {
	return newMapper(codec, &config.Config{Auth: config.AuthConfig{JWTSecret: secret}}).itemFromList(upstreamListItem{
		ContentID:       item.ContentID,
		Type:            "movie",
		Title:           "Movie",
		PosterURL:       item.PosterPath,
		PosterPath:      item.PosterPath,
		PosterThumbhash: item.PosterThumbhash,
		UpdatedAt:       item.UpdatedAt,
	}, false, nil, nil).ImageTags["Primary"]
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

// TestHandleItemImageInfosListsAdvertisedArtwork covers the Roku details
// screen, which picks its poster from /Items/{id}/Images: every entry names an
// image type and tag the item's ImageTags already advertise.
func TestHandleItemImageInfosListsAdvertisedArtwork(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{Auth: config.AuthConfig{JWTSecret: "image-secret"}}),
		images:   NewImageCache(time.Hour, time.Now),
		userData: &mockUserDataService{},
		content: &stubContentService{detail: &upstreamItemDetail{
			ContentID: "movie-1", Type: "movie", Title: "Movie",
			PosterURL: "https://cdn.example.test/poster.jpg", PosterPath: "metadb://poster/movie-1",
			BackdropURL: "https://cdn.example.test/backdrop.jpg", BackdropPath: "metadb://backdrop/movie-1",
			LogoURL: "https://cdn.example.test/logo.png", LogoPath: "metadb://logo/movie-1",
		}},
	}
	routeID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	req := httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token"}))
	rec := httptest.NewRecorder()
	h.HandleItemImageInfos(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var infos []imageInfoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &infos); err != nil {
		t.Fatal(err)
	}
	item := h.mapper.itemFromDetail(*h.content.(*stubContentService).detail, false, nil)
	want := map[string]string{"Backdrop/0": item.BackdropImageTags[0], "Logo": item.ImageTags["Logo"], "Primary": item.ImageTags["Primary"]}
	got := map[string]string{}
	for _, info := range infos {
		key := info.ImageType
		if info.ImageIndex != nil {
			key += "/" + strconv.Itoa(*info.ImageIndex)
		}
		got[key] = info.ImageTag
	}
	if !maps.Equal(got, want) {
		t.Fatalf("image infos = %v, want %v", got, want)
	}

	// An id /Items/{id} cannot resolve answers the same way here.
	req = httptest.NewRequest(http.MethodGet, "/Items/not-an-id/Images", nil)
	routeCtx = chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "not-an-id")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token"}))
	rec = httptest.NewRecorder()
	h.HandleItemImageInfos(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", rec.Code)
	}
}

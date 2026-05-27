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

func withImageRouteParams(r *http.Request, routeID, imageType string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	routeCtx.URLParams.Add("imageType", imageType)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

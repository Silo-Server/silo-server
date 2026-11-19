package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeExtraBackdrops map[string][]string

func (f fakeExtraBackdrops) ExtraBackdropPaths(context.Context, []string) (map[string][]string, error) {
	return f, nil
}

func extendedArtworkImagesHandler(item *models.MediaItem, extras []string) (*ImagesHandler, *countingImageResolver) {
	resolver := &countingImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	return &ImagesHandler{
		codec:     NewResourceIDCodec(),
		images:    NewImageCache(time.Hour, time.Now),
		itemRepo:  fakeImageItemRepo{item: item},
		detailSvc: detailSvc,
		backdrops: fakeExtraBackdrops{item.ContentID: extras},
		imageTags: newImageTagSigner("image-secret"),
	}, resolver
}

func requestItemImage(h *ImagesHandler, routeID, imageType, index string) *httptest.ResponseRecorder {
	target := "/Items/" + routeID + "/Images/" + imageType
	if index != "" {
		target += "/" + index
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	routeCtx.URLParams.Add("imageType", imageType)
	if index != "" {
		routeCtx.URLParams.Add("index", index)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	h.HandleItemImage(rec, req)
	return rec
}

func TestHandleItemImageServesExtendedArtwork(t *testing.T) {
	item := &models.MediaItem{
		ContentID: "movie-1", Type: "movie",
		PosterPath:   "local/movies/movie-1/aa/poster/original.r.webp",
		BackdropPath: "local/movies/movie-1/bb/backdrop/original.r.webp",
		BannerPath:   "local/movies/movie-1/cc/banner/original.r.webp",
		ThumbPath:    "local/movies/movie-1/dd/thumb/original.r.webp",
		ClearArtPath: "local/movies/movie-1/ee/clearart/original.r.webp",
		DiscPath:     "local/movies/movie-1/ff/disc/original.r.webp",
	}
	extras := []string{"local/movies/movie-1/gg-1/backdrop/original.r1.webp", "local/movies/movie-1/hh-2/backdrop/original.r2.webp"}
	h, _ := extendedArtworkImagesHandler(item, extras)
	routeID := h.codec.EncodeStringID(EncodedIDItem, item.ContentID)

	for _, tc := range []struct{ imageType, index, wantDir string }{
		{"Banner", "", "/cc/banner/"},
		{"Thumb", "", "/dd/thumb/"},
		{"Art", "", "/ee/clearart/"},
		{"Disc", "0", "/ff/disc/"},
		{"Backdrop", "", "/bb/backdrop/"},
		{"Backdrop", "0", "/bb/backdrop/"},
		{"Backdrop", "1", "/gg-1/backdrop/"},
		{"Backdrop", "2", "/hh-2/backdrop/"},
		// A repeat answers from the route cache, which keys each index apart.
		{"Backdrop", "1", "/gg-1/backdrop/"},
		{"Backdrop", "", "/bb/backdrop/"},
	} {
		rec := requestItemImage(h, routeID, tc.imageType, tc.index)
		if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), tc.wantDir) {
			t.Fatalf("%s/%s = %d %q, want a redirect into %s", tc.imageType, tc.index, rec.Code, rec.Header().Get("Location"), tc.wantDir)
		}
	}
	if rec := requestItemImage(h, routeID, "Backdrop", "3"); rec.Code != http.StatusNotFound {
		t.Fatalf("Backdrop/3 = %d %q, want 404", rec.Code, rec.Header().Get("Location"))
	}
}

func TestHandleItemImageExtendedArtworkFallbacks(t *testing.T) {
	item := &models.MediaItem{
		ContentID: "movie-1", Type: "movie",
		PosterPath:   "local/movies/movie-1/aa/poster/original.r.webp",
		BackdropPath: "local/movies/movie-1/bb/backdrop/original.r.webp",
	}
	h, _ := extendedArtworkImagesHandler(item, nil)
	routeID := h.codec.EncodeStringID(EncodedIDItem, item.ContentID)

	// Thumb stays an alias for the backdrop when there is no thumb.
	if rec := requestItemImage(h, routeID, "Thumb", ""); rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/bb/backdrop/") {
		t.Fatalf("Thumb = %d %q, want the backdrop", rec.Code, rec.Header().Get("Location"))
	}
	// Banner, Art and Disc have no stand-in, and there is no second backdrop.
	for _, tc := range [][2]string{{"Banner", ""}, {"Art", ""}, {"Disc", ""}, {"Backdrop", "1"}} {
		if rec := requestItemImage(h, routeID, tc[0], tc[1]); rec.Code != http.StatusNotFound {
			t.Fatalf("%s/%s = %d %q, want 404", tc[0], tc[1], rec.Code, rec.Header().Get("Location"))
		}
	}
}

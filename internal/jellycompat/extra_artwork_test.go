package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeExtraBackdrops map[string][]string

func (f fakeExtraBackdrops) ExtraBackdropPaths(context.Context, []string) (map[string][]string, error) {
	return f, nil
}

func TestItemTagsAdvertiseExtendedArtwork(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	updatedAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	movie := upstreamListItem{
		ContentID: "movie-1", Type: "movie", Title: "Film", UpdatedAt: updatedAt,
		BackdropURL: "https://cdn.example.test/b0", BackdropPath: "local/movies/movie-1/aa/backdrop/original.r0.webp",
		BannerPath: "local/movies/movie-1/bb/banner/original.r.webp", ThumbPath: "local/movies/movie-1/cc/thumb/original.r.webp",
		ClearArtPath: "local/movies/movie-1/dd/clearart/original.r.webp", DiscPath: "local/movies/movie-1/ee/disc/original.r.webp",
		ExtraBackdropPaths: []string{"local/movies/movie-1/ff-1/backdrop/original.r1.webp", "local/movies/movie-1/gg-2/backdrop/original.r2.webp"},
	}
	dto := m.itemFromList(movie, false, nil, nil)
	for _, key := range []string{"Banner", "Thumb", "Art", "Disc"} {
		if dto.ImageTags[key] == "" {
			t.Errorf("ImageTags[%s] missing: %v", key, dto.ImageTags)
		}
	}
	if len(dto.BackdropImageTags) != 3 || len(slices.Compact(slices.Clone(dto.BackdropImageTags))) != 3 {
		t.Fatalf("BackdropImageTags = %q, want three distinct tags", dto.BackdropImageTags)
	}

	// A changed image changes its tag.
	moved := movie
	moved.ExtraBackdropPaths = []string{movie.ExtraBackdropPaths[0], "local/movies/movie-1/hh-2/backdrop/original.r3.webp"}
	if again := m.itemFromList(moved, false, nil, nil); again.BackdropImageTags[2] == dto.BackdropImageTags[2] || again.BackdropImageTags[1] != dto.BackdropImageTags[1] {
		t.Fatalf("tags after replacing the second extra backdrop = %q, before %q", again.BackdropImageTags, dto.BackdropImageTags)
	}

	// Nothing extra for an item without the art, an episode, or extras with no
	// primary backdrop to number them after.
	bare := m.itemFromList(upstreamListItem{ContentID: "movie-2", Type: "movie", BackdropURL: "https://cdn.example.test/b"}, false, nil, nil)
	if len(bare.ImageTags) != 0 || len(bare.BackdropImageTags) != 1 {
		t.Fatalf("bare movie tags = %v / %q", bare.ImageTags, bare.BackdropImageTags)
	}
	episode := movie
	episode.Type = "episode"
	if ep := m.itemFromList(episode, false, nil, nil); ep.ImageTags["Banner"] != "" || len(ep.BackdropImageTags) != 1 {
		t.Fatalf("episode tags = %v / %q", ep.ImageTags, ep.BackdropImageTags)
	}
	noPrimary := movie
	noPrimary.BackdropURL, noPrimary.BackdropPath = "", ""
	if np := m.itemFromList(noPrimary, false, nil, nil); len(np.BackdropImageTags) != 0 {
		t.Fatalf("extras advertised without a primary backdrop: %q", np.BackdropImageTags)
	}
}

func TestItemDetailTagsAdvertiseEveryBackdrop(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	detail := upstreamItemDetail{
		ContentID: "series-1", Type: "series", Title: "Show",
		BackdropURL:        "https://cdn.example.test/b0?sig=one",
		BannerPath:         "local/series/series-1/aa/banner/original.r.webp",
		ThumbPath:          "local/series/series-1/bb/thumb/original.r.webp",
		ClearArtPath:       "local/series/series-1/cc/clearart/original.r.webp",
		ExtraBackdropPaths: []string{"local/series/series-1/dd-1/backdrop/original.r.webp", "local/series/series-1/ee-2/backdrop/original.r.webp"},
	}
	dto := m.itemFromDetail(detail, false, nil)
	if len(dto.BackdropImageTags) != 3 {
		t.Fatalf("BackdropImageTags = %q, want 3", dto.BackdropImageTags)
	}
	for _, key := range []string{"Banner", "Thumb", "Art"} {
		if dto.ImageTags[key] == "" {
			t.Errorf("ImageTags[%s] missing: %v", key, dto.ImageTags)
		}
	}
	if _, ok := dto.ImageTags["Disc"]; ok {
		t.Fatal("Disc advertised without disc art")
	}
	// The tags follow the cached image, not each presigned URL.
	detail.BackdropURL = "https://cdn.example.test/b0?sig=two"
	again := m.itemFromDetail(detail, false, nil)
	if !slices.Equal(again.BackdropImageTags[1:], dto.BackdropImageTags[1:]) || again.ImageTags["Banner"] != dto.ImageTags["Banner"] {
		t.Fatalf("tags changed with the signature: %q / %q", again.BackdropImageTags, dto.BackdropImageTags)
	}
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

package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

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

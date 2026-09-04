package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

func boolPointer(value bool) *bool { return &value }

func TestSelectTextlessPosterPrefersExplicitSignal(t *testing.T) {
	images := []metadata.RemoteImage{
		{URL: "neutral-high", Type: metadata.ImagePoster, Language: "", Width: 1000, Height: 1500, Rating: 9.9},
		{URL: "explicit", Type: metadata.ImagePoster, Language: "en", Width: 1000, Height: 1500, Rating: 5, IncludesText: boolPointer(false)},
		{URL: "with-text", Type: metadata.ImagePoster, Language: "", Width: 1000, Height: 1500, Rating: 10, IncludesText: boolPointer(true)},
	}

	if got := selectTextlessPoster(images); got != "explicit" {
		t.Fatalf("selectTextlessPoster() = %q, want explicit", got)
	}
}

func TestSelectTextlessPosterFallsBackToBestNeutralPortrait(t *testing.T) {
	images := []metadata.RemoteImage{
		{URL: "localized", Type: metadata.ImagePoster, Language: "en", Width: 1000, Height: 1500, Rating: 10},
		{URL: "landscape", Type: metadata.ImagePoster, Language: "", Width: 1600, Height: 900, Rating: 10},
		{URL: "neutral-low", Type: metadata.ImagePoster, Language: "", Width: 1000, Height: 1500, Rating: 5},
		{URL: "neutral-high", Type: metadata.ImagePoster, Language: "  ", Width: 1000, Height: 1500, Rating: 8},
	}

	if got := selectTextlessPoster(images); got != "neutral-high" {
		t.Fatalf("selectTextlessPoster() = %q, want neutral-high", got)
	}
}

type deniedTextlessPosterItems struct{}

func (deniedTextlessPosterItems) GetByID(context.Context, string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: "hidden", Type: "movie", TmdbID: "1"}, nil
}

func (deniedTextlessPosterItems) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return catalog.ErrItemNotFound
}

type countingTextlessPosterImages struct{ calls int }

func (f *countingTextlessPosterImages) FetchItemImages(context.Context, map[string]string, string, string, int) ([]metadata.RemoteImage, map[string]string, error) {
	f.calls++
	return nil, nil, nil
}

func TestTextlessPosterHandlerChecksAccessBeforeProviderFetch(t *testing.T) {
	images := &countingTextlessPosterImages{}
	handler := &TextlessPosterHandler{
		accessFilter: func(http.ResponseWriter, *http.Request) (catalog.AccessFilter, bool) {
			return catalog.AccessFilter{}, true
		},
		items:  deniedTextlessPosterItems{},
		images: images,
		cache:  newTextlessPosterCache(4),
	}

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "hidden")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/items/hidden/images/textless-poster", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	handler.HandleGet(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if images.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", images.calls)
	}
}

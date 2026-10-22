package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	personalPosterPath   = "tmdb/movies/550/poster/original.abc123.webp"
	personalBackdropPath = "tmdb/movies/550/backdrop/original.abc123.webp"
	personalLogoPath     = "tmdb/movies/550/logo/original.png"
)

// newPersonalDataImageHandler wires a PersonalDataHandler whose image resolver
// echoes the variant hint and the object key it was handed, so a test can assert
// on both halves of what the handler decided.
func newPersonalDataImageHandler(t *testing.T, store userstore.UserStore) *PersonalDataHandler {
	t.Helper()
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(&countingItemListImageResolver{})
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"movie-1": {
				ContentID:    "movie-1",
				Type:         "movie",
				Title:        "Fight Club",
				PosterPath:   personalPosterPath,
				BackdropPath: personalBackdropPath,
				LogoPath:     personalLogoPath,
			},
		},
	})
	handler.SetDetailService(detailSvc)
	return handler
}

func listFavoritesURLs(t *testing.T, handler *PersonalDataHandler, query string) itemListResponse {
	t.Helper()
	req := newAuthorizedProfileRequestWithRole(http.MethodGet, "/favorites"+query, "", "user", "profile-1")
	rr := httptest.NewRecorder()
	handler.HandleListFavorites(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body itemsListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1: %s", len(body.Items), rr.Body.String())
	}
	return body.Items[0]
}

func seedFavorite(t *testing.T, store userstore.UserStore) {
	t.Helper()
	if _, err := store.AddFavorite(context.Background(), "profile-1", "movie-1"); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
}

// Without the parameter the personal lists must keep their historical, and
// deliberately asymmetric, per-slot sizes: a w500 poster beside a w300 backdrop.
func TestPersonalListsUnsetImageSizeUnchanged(t *testing.T) {
	store := newProfileTestStore(t)
	seedFavorite(t, store)
	handler := newPersonalDataImageHandler(t, store)

	item := listFavoritesURLs(t, handler, "")

	if !strings.Contains(item.PosterURL, ":featured:") || !strings.Contains(item.PosterURL, "/poster/w500.") {
		t.Errorf("poster URL = %q, want the featured w500 poster", item.PosterURL)
	}
	if !strings.Contains(item.BackdropURL, ":card:") || !strings.Contains(item.BackdropURL, "/backdrop/w300.") {
		t.Errorf("backdrop URL = %q, want the card w300 backdrop", item.BackdropURL)
	}
	// Card images resolve in batches, not one lookup each.
	if !strings.HasPrefix(item.PosterURL, "batch:") || !strings.HasPrefix(item.BackdropURL, "batch:") {
		t.Errorf("poster %q, backdrop %q: want both resolved in a batch", item.PosterURL, item.BackdropURL)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	personalPosterPath   = "tmdb/movies/550/poster/original.abc123.webp"
	personalBackdropPath = "tmdb/movies/550/backdrop/original.abc123.webp"
	personalStillPath    = "tvdb/series/1/seasons/1/episodes/1/still/original.webp"
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
	if err := store.AddFavorite(context.Background(), "profile-1", "movie-1"); err != nil {
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
}

// An explicit size applies to every image in the response, collapsing the
// per-slot asymmetry.
func TestPersonalListsHonorImageSize(t *testing.T) {
	tests := []struct {
		size        string
		hint        string
		wantPoster  string
		wantBackdro string
	}{
		{"small", ":card:", "/poster/w300.", "/backdrop/w300."},
		{"medium", ":featured:", "/poster/w500.", "/backdrop/w1920."},
		{"large", ":large:", "/poster/w780.", "/backdrop/w1920."},
		{"original", ":original:", "/poster/original.", "/backdrop/original."},
	}

	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			store := newProfileTestStore(t)
			seedFavorite(t, store)
			handler := newPersonalDataImageHandler(t, store)

			item := listFavoritesURLs(t, handler, "?image_size="+tt.size)

			if !strings.Contains(item.PosterURL, tt.wantPoster) || !strings.Contains(item.PosterURL, tt.hint) {
				t.Errorf("poster URL = %q, want %s with hint %s", item.PosterURL, tt.wantPoster, tt.hint)
			}
			if !strings.Contains(item.BackdropURL, tt.wantBackdro) || !strings.Contains(item.BackdropURL, tt.hint) {
				t.Errorf("backdrop URL = %q, want %s with hint %s", item.BackdropURL, tt.wantBackdro, tt.hint)
			}
		})
	}
}

func TestPersonalListsRejectInvalidImageSize(t *testing.T) {
	store := newProfileTestStore(t)
	handler := newPersonalDataImageHandler(t, store)

	handlers := map[string]http.HandlerFunc{
		"/favorites": handler.HandleListFavorites,
		"/watchlist": handler.HandleListWatchlist,
		"/history":   handler.HandleListHistory,
	}
	for path, handle := range handlers {
		t.Run(path, func(t *testing.T) {
			req := newAuthorizedProfileRequestWithRole(http.MethodGet, path+"?image_size=enormous", "", "user", "profile-1")
			rr := httptest.NewRecorder()
			handle(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "invalid_image_size") {
				t.Fatalf("body = %s, want an invalid_image_size code", rr.Body.String())
			}
		})
	}
}

// A valid size must not disturb the endpoints' existing success path.
func TestPersonalListsAcceptValidImageSize(t *testing.T) {
	store := newProfileTestStore(t)
	handler := newPersonalDataImageHandler(t, store)

	handlers := map[string]http.HandlerFunc{
		"/favorites": handler.HandleListFavorites,
		"/watchlist": handler.HandleListWatchlist,
		"/history":   handler.HandleListHistory,
	}
	for path, handle := range handlers {
		t.Run(path, func(t *testing.T) {
			req := newAuthorizedProfileRequestWithRole(http.MethodGet, path+"?image_size=large", "", "user", "profile-1")
			rr := httptest.NewRecorder()
			handle(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// The episode branch of the personal lists resolves a still into the backdrop
// slot; it rides the still ladder, so a backdrop-only width would name a key
// that was never generated.
func TestPersonalListsEpisodeStillHonorsImageSize(t *testing.T) {
	for _, tt := range []struct {
		size string
		want string
	}{
		{"", "/still/w300."},
		{"small", "/still/w300."},
		{"large", "/still/w780."},
	} {
		name := tt.size
		if name == "" {
			name = "unset"
		}
		t.Run(name, func(t *testing.T) {
			size, err := imagesize.Parse(tt.size)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.size, err)
			}
			if got := sizedCardPath(personalStillPath, "still", size); !strings.Contains(got, tt.want) {
				t.Fatalf("still path = %q, want %s", got, tt.want)
			}
		})
	}
}

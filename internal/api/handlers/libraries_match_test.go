package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestLibraryMatchUnmatchedItems_NilPool verifies that the unmatched-items
// endpoint returns a clear error when the database pool is not configured.
func TestLibraryMatchUnmatchedItems_NilPool(t *testing.T) {
	h := &LibraryHandler{}

	r := chi.NewRouter()
	r.Get("/libraries/unmatched-items", h.HandleListUnmatchedItems)

	req := httptest.NewRequest(http.MethodGet, "/libraries/unmatched-items", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for nil pool, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding error response: %v", err)
	}
	if resp.Error != "internal_error" {
		t.Errorf("expected error code 'internal_error', got %q", resp.Error)
	}
}

// TestLibraryMatchUnmatchedItems_ResponseShape verifies that the
// unmatchedItemResponse type has the expected JSON field tags for the
// admin maintenance page. This is a compile-time structure test.
func TestLibraryMatchUnmatchedItems_ResponseShape(t *testing.T) {
	item := unmatchedItemResponse{
		ContentID:   "abc-123",
		Title:       "Test Movie",
		Year:        2024,
		ContentType: "movie",
		LibraryID:   1,
		LibraryName: "Movies",
		Status:      "unmatched",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	expectedKeys := []string{
		"content_id", "title", "year", "content_type",
		"library_id", "library_name", "status",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("expected JSON key %q in response, got keys: %v", key, keys(m))
		}
	}
}

// TestLibraryMatchStaleIDs_NilRepo verifies that the stale-IDs endpoint
// returns an empty array when the repository is nil, confirming backward
// compatibility after the new match endpoints are introduced.
func TestLibraryMatchStaleIDs_NilRepo(t *testing.T) {
	h := &LibraryHandler{} // StaleIDRepo is nil

	r := chi.NewRouter()
	r.Get("/libraries/stale-ids", h.HandleListStaleIDs)

	req := httptest.NewRequest(http.MethodGet, "/libraries/stale-ids", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for nil StaleIDRepo, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp []staleMediaIDResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty stale IDs list, got %d entries", len(resp))
	}
}

// TestLibraryMatchStaleIDs_ResponseShape verifies the stale media ID response
// JSON structure still contains the expected fields.
func TestLibraryMatchStaleIDs_ResponseShape(t *testing.T) {
	item := staleMediaIDResponse{
		ContentID:   "content-1",
		LibraryID:   2,
		LibraryName: "TV Shows",
		Title:       "Test Show",
		Year:        2023,
		ContentType: "series",
		Provider:    "tvdb",
		ProviderID:  "12345",
		FirstSeenAt: "2023-01-01T00:00:00Z",
		LastSeenAt:  "2023-06-01T00:00:00Z",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	expectedKeys := []string{
		"content_id", "library_id", "library_name", "title",
		"year", "content_type", "provider", "provider_id",
		"first_seen_at", "last_seen_at",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("expected JSON key %q in response, got keys: %v", key, keys(m))
		}
	}
}

// TestLibraryMatchRematch_DeprecatedStillRoutes verifies that the deprecated
// HandleRematchStaleID handler method still exists and can be registered as a
// route alongside the new match endpoints. This is a compile-time routing test.
func TestLibraryMatchRematch_DeprecatedStillRoutes(t *testing.T) {
	h := &LibraryHandler{}

	r := chi.NewRouter()
	// Register both old and new endpoints to prove they coexist.
	r.Post("/libraries/stale-ids/{contentID}/rematch", h.HandleRematchStaleID)
	r.Get("/libraries/unmatched-items", h.HandleListUnmatchedItems)
	r.Get("/libraries/stale-ids", h.HandleListStaleIDs)

	// Verify the routes are registered by checking that a walk finds them.
	found := map[string]bool{}
	chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found[method+" "+route] = true
		return nil
	})

	expectedRoutes := []string{
		"POST /libraries/stale-ids/{contentID}/rematch",
		"GET /libraries/unmatched-items",
		"GET /libraries/stale-ids",
	}
	for _, route := range expectedRoutes {
		if !found[route] {
			t.Errorf("expected route %q to be registered, registered routes: %v", route, found)
		}
	}
}

func TestLibraryMetadataMatchQueueHandlers_NilFolderRepo(t *testing.T) {
	h := &LibraryHandler{MovieMatchQueueRepo: noopMovieMatchQueue{}}

	tests := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{
			name:    "get",
			method:  http.MethodGet,
			path:    "/libraries/1/metadata-match-queue",
			handler: h.HandleGetMetadataMatchQueue,
		},
		{
			name:    "retry",
			method:  http.MethodPost,
			path:    "/libraries/1/metadata-match-queue/retry",
			handler: h.HandleRetryMetadataMatchQueue,
		},
		{
			name:    "cancel",
			method:  http.MethodDelete,
			path:    "/libraries/1/metadata-match-queue",
			handler: h.HandleCancelMetadataMatchQueue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			r.Method(tt.method, "/libraries/{id}/metadata-match-queue", tt.handler)
			r.Method(tt.method, "/libraries/{id}/metadata-match-queue/retry", tt.handler)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503 for nil folderRepo, got %d: %s", rec.Code, rec.Body.String())
			}

			var resp errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decoding error response: %v", err)
			}
			if resp.Error != "unavailable" {
				t.Errorf("expected error code 'unavailable', got %q", resp.Error)
			}
		})
	}
}

type noopMovieMatchQueue struct{}

func (noopMovieMatchQueue) SyncForFolder(context.Context, int) error {
	return nil
}

func (noopMovieMatchQueue) DeleteByFolder(context.Context, int) (int, error) {
	return 0, nil
}

func (noopMovieMatchQueue) CountByFolder(context.Context, int) (int, error) {
	return 0, nil
}

func (noopMovieMatchQueue) ListByFolder(context.Context, int, int, int) ([]models.MovieMatchQueueEntry, int, error) {
	return nil, 0, nil
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

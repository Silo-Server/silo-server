package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// inProgressStubMediaStore backs the /me/items-in-progress shape tests.
type inProgressStubMediaStore struct {
	noopMediaStore
	libs []AudiobookLibrary
	byID map[string]*models.MediaItem
}

func (s *inProgressStubMediaStore) ListAudiobookLibraries(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return s.libs, nil
}

func (s *inProgressStubMediaStore) GetAudiobooksByIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]*models.MediaItem, error) {
	out := make(map[string]*models.MediaItem, len(ids))
	for _, id := range ids {
		if it, ok := s.byID[id]; ok {
			out[id] = it
		}
	}
	return out, nil
}

// TestItemsInProgress_Unauthenticated_401 covers the auth guard: no
// ctxAuth in the request context (bearerAuth middleware never ran).
func TestItemsInProgress_Unauthenticated_401(t *testing.T) {
	h := New(Dependencies{MediaStore: &inProgressStubMediaStore{}})
	req := httptest.NewRequest(http.MethodGet, "/api/me/items-in-progress", nil)
	rec := httptest.NewRecorder()
	h.handleItemsInProgress(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

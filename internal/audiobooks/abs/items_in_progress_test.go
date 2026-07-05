package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// inProgressFakeProgressStore returns a fixed set of progress rows.
type inProgressFakeProgressStore struct {
	fakeProgressStore
	rows []ProgressRow
}

func (f *inProgressFakeProgressStore) ListProgressForAudiobooks(context.Context, string, string, int) ([]ProgressRow, error) {
	return f.rows, nil
}

// TestItemsInProgress_EnvelopeAndItemShape asserts the response matches
// real ABS MeController.getAllLibraryItemsInProgress: envelope key
// "libraryItems", each entry is the minified library item spread with a
// flat "progressLastUpdate" field — no nested "userMediaProgress" object.
func TestItemsInProgress_EnvelopeAndItemShape(t *testing.T) {
	updatedAt := time.Now()
	media := &inProgressStubMediaStore{
		libs: []AudiobookLibrary{{ID: 1, Name: "Audiobooks", Type: "audiobooks"}},
		byID: map[string]*models.MediaItem{
			"book-1": {ContentID: "book-1", Title: "In Progress Book"},
		},
	}
	progress := &inProgressFakeProgressStore{
		rows: []ProgressRow{
			{
				UserID:         "1",
				ContentID:      "book-1",
				CurrentSeconds: 120,
				ProgressPct:    0.25,
				IsFinished:     false,
				UpdatedAt:      updatedAt,
			},
		},
	}
	h := New(Dependencies{MediaStore: media, ProgressStore: progress})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/items-in-progress", nil, nil, "1", "", h.handleItemsInProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items, ok := got["libraryItems"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("libraryItems = %v, want 1 entry", got["libraryItems"])
	}
	entry, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("entry not an object: %v", items[0])
	}

	if entry["id"] != "book-1" {
		t.Errorf("id = %v, want book-1", entry["id"])
	}
	if _, hasMedia := entry["media"]; !hasMedia {
		t.Errorf("entry missing minified 'media' key: %v", entry)
	}
	lastUpdate, ok := entry["progressLastUpdate"].(float64)
	if !ok {
		t.Fatalf("entry missing progressLastUpdate: %v", entry)
	}
	if int64(lastUpdate) != updatedAt.UnixMilli() {
		t.Errorf("progressLastUpdate = %v, want %v", int64(lastUpdate), updatedAt.UnixMilli())
	}
	if _, hasWrapper := entry["userMediaProgress"]; hasWrapper {
		t.Errorf("entry has userMediaProgress wrapper, real ABS flattens progress instead: %v", entry)
	}
}

// TestItemsInProgress_NoProgressStore_ReturnsEmptyEnvelope covers the
// no-store-configured fallback.
func TestItemsInProgress_NoProgressStore_ReturnsEmptyEnvelope(t *testing.T) {
	h := New(Dependencies{MediaStore: &inProgressStubMediaStore{}})
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/items-in-progress", nil, nil, "1", "", h.handleItemsInProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items, ok := got["libraryItems"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("libraryItems = %v, want empty array", got["libraryItems"])
	}
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

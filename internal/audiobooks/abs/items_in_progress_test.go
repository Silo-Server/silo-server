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
	libs        []AudiobookLibrary
	byID        map[string]*models.MediaItem
	libraryByID map[string]int64
	files       map[string][]*models.MediaFile
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

func (s *inProgressStubMediaStore) GetAudiobookByID(_ context.Context, id string, _ catalog.AccessFilter) (*models.MediaItem, error) {
	return s.byID[id], nil
}

func (s *inProgressStubMediaStore) GetItemType(ctx context.Context, id string, access catalog.AccessFilter) (string, error) {
	return itemTypeFromLookup(s.GetAudiobookByID(ctx, id, access))
}

func (s *inProgressStubMediaStore) GetMediaFiles(_ context.Context, contentID string, _ catalog.AccessFilter) ([]*models.MediaFile, error) {
	return s.files[contentID], nil
}

func (s *inProgressStubMediaStore) GetMediaFilesByContentIDs(_ context.Context, contentIDs []string, _ catalog.AccessFilter) (map[string][]*models.MediaFile, error) {
	out := make(map[string][]*models.MediaFile, len(contentIDs))
	for _, id := range contentIDs {
		if files, ok := s.files[id]; ok {
			out[id] = files
		}
	}
	return out, nil
}

func (s *inProgressStubMediaStore) GetItemLibraryIDs(_ context.Context, contentIDs []string, _ catalog.AccessFilter) (map[string]int64, error) {
	out := make(map[string]int64, len(contentIDs))
	for _, id := range contentIDs {
		if libraryID, ok := s.libraryByID[id]; ok {
			out[id] = libraryID
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
			testBookID: {ContentID: testBookID, Title: "In Progress Book"},
		},
	}
	progress := &inProgressFakeProgressStore{
		rows: []ProgressRow{
			{
				UserID:         "1",
				ContentID:      testBookID,
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

	if entry["id"] != testBookID {
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

func TestItemsInProgressUsesActualEbookLibraryMembership(t *testing.T) {
	updatedAt := time.Now()
	media := &inProgressStubMediaStore{
		libs: []AudiobookLibrary{
			{ID: 17, Name: "First Books", Type: mediaTypeEbook},  //nolint:goconst // Stable fixture label.
			{ID: 18, Name: "Second Books", Type: mediaTypeEbook}, //nolint:goconst // Stable fixture label.
		},
		byID: map[string]*models.MediaItem{
			testEbookID: {ContentID: testEbookID, Type: mediaTypeEbook, Title: "Reader Test"}, //nolint:goconst // Stable fixture label.
		},
		libraryByID: map[string]int64{testEbookID: 18},
	}
	ebookProgress := &recordingEbookProgressStore{rows: []EbookProgress{{
		UserID: "1", ProfileID: testProfileID, ContentID: testEbookID, Progress: 0.4, UpdatedAt: updatedAt,
	}}}
	h := New(Dependencies{MediaStore: media, EbookProgressStore: ebookProgress})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/items-in-progress", nil, nil, "1", testProfileID, h.handleItemsInProgress)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, _ := got["libraryItems"].([]any)
	if len(items) != 1 {
		t.Fatalf("libraryItems = %#v, want one", got["libraryItems"])
	}
	entry, _ := items[0].(map[string]any)
	if entry["libraryId"] != "18" {
		t.Fatalf("libraryId = %v, want 18", entry["libraryId"])
	}
}

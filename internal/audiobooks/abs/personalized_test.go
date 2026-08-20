package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// personalizedStubStore serves one distinct book per shelf and counts the
// ebook enrichment batches, so the Home tab's query budget is observable.
type personalizedStubStore struct {
	noopMediaStore
	lib        AudiobookLibrary
	batchFiles atomic.Int32
	batchPrim  atomic.Int32
}

func personalizedBook(id string) []*models.MediaItem {
	return []*models.MediaItem{{ContentID: id, Title: "Title-" + id, Type: mediaTypeEbook}}
}

func (s *personalizedStubStore) ListAudiobookLibraries(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return []AudiobookLibrary{s.lib}, nil
}

func (s *personalizedStubStore) ListContinueListening(context.Context, string, string, AudiobookLibrary, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return personalizedBook("book-continue"), nil
}

func (s *personalizedStubStore) ListRecentlyAdded(context.Context, AudiobookLibrary, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return personalizedBook("book-recent"), nil
}

func (s *personalizedStubStore) ListDiscover(context.Context, AudiobookLibrary, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return personalizedBook("book-discover"), nil
}

func (s *personalizedStubStore) ListFinished(context.Context, string, string, AudiobookLibrary, int, catalog.AccessFilter) ([]*models.MediaItem, error) {
	return personalizedBook("book-finished"), nil
}

func (s *personalizedStubStore) ListLibrarySeries(context.Context, AudiobookLibrary, int, int, catalog.AccessFilter) ([]SeriesSummary, int, error) {
	return []SeriesSummary{{ID: "s1", Name: "Series One", NumBooks: 2}}, 1, nil
}

func (s *personalizedStubStore) GetMediaFilesByContentIDs(_ context.Context, contentIDs []string, _ catalog.AccessFilter) (map[string][]*models.MediaFile, error) {
	s.batchFiles.Add(1)
	out := make(map[string][]*models.MediaFile, len(contentIDs))
	for i, id := range contentIDs {
		out[id] = []*models.MediaFile{{ID: i + 1, FilePath: "/books/" + id + ".epub"}}
	}
	return out, nil
}

func (s *personalizedStubStore) GetPrimaryEbookFileIDs(context.Context, []string) (map[string]EbookPrimarySelection, error) {
	s.batchPrim.Add(1)
	return map[string]EbookPrimarySelection{}, nil
}

func personalizedShelves(t *testing.T, h *Handler) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/libraries/5/personalized", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "42"}))
	rec := httptest.NewRecorder()
	h.handlePersonalized(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var shelves []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &shelves); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return shelves
}

// TestPersonalizedEnrichesEbookShelvesOnce pins the Home tab's query budget:
// ebook enrichment runs once over the union of the shelves, not once per
// shelf. It also pins shelf order and the ebook-specific labels, which the
// concurrent rewrite must not disturb.
func TestPersonalizedEnrichesEbookShelvesOnce(t *testing.T) {
	store := &personalizedStubStore{lib: AudiobookLibrary{ID: 5, Name: "Ebooks", Type: libraryTypeEbooks}}
	h := New(Dependencies{MediaStore: store})

	shelves := personalizedShelves(t, h)

	if store.batchFiles.Load() != 1 || store.batchPrim.Load() != 1 {
		t.Fatalf("ebook enrichment batches = files:%d primaries:%d, want 1 each across all shelves",
			store.batchFiles.Load(), store.batchPrim.Load())
	}
	wantIDs := []string{"continue-reading", "recently-added", "recent-series", "discover", "read-again"}
	if len(shelves) != len(wantIDs) {
		t.Fatalf("shelves = %d, want %d: %v", len(shelves), len(wantIDs), shelves)
	}
	for i, want := range wantIDs {
		if shelves[i]["id"] != want {
			t.Errorf("shelf %d id = %v, want %v", i, shelves[i]["id"], want)
		}
	}
	if shelves[0]["label"] != "Continue Reading" || shelves[4]["label"] != "Read Again" {
		t.Errorf("ebook shelf labels = %v / %v", shelves[0]["label"], shelves[4]["label"])
	}
	// Each book shelf still carries its own single entity after the union pass.
	for _, i := range []int{0, 1, 3, 4} {
		entities, _ := shelves[i]["entities"].([]any)
		if len(entities) != 1 {
			t.Fatalf("shelf %v entities = %d, want 1", shelves[i]["id"], len(entities))
		}
		entry, _ := entities[0].(map[string]any)
		if entry["id"] == nil {
			t.Fatalf("shelf %v entity has no id: %#v", shelves[i]["id"], entry)
		}
	}
}

// TestPersonalizedAudiobookLibraryUsesListenLabels covers the other branch of
// the shared shelf construction.
func TestPersonalizedAudiobookLibraryUsesListenLabels(t *testing.T) {
	store := &personalizedStubStore{lib: AudiobookLibrary{ID: 5, Name: "Audiobooks", Type: "audiobooks"}}
	h := New(Dependencies{MediaStore: store})

	shelves := personalizedShelves(t, h)

	if store.batchFiles.Load() != 0 {
		t.Errorf("audiobook library ran ebook enrichment %d times, want 0", store.batchFiles.Load())
	}
	if shelves[0]["id"] != "continue-listening" || shelves[4]["id"] != "listen-again" {
		t.Fatalf("audiobook shelf ids = %v / %v", shelves[0]["id"], shelves[4]["id"])
	}
}

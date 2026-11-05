package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// --- Fakes ---

type fakeMatchItemLookup struct {
	items map[string]*models.MediaItem
}

func (f *fakeMatchItemLookup) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if item, ok := f.items[contentID]; ok {
		return item, nil
	}
	return nil, fmt.Errorf("item not found: %s", contentID)
}

type fakeMatchFolderLookup struct {
	folders   map[string]int
	folderIDs map[string][]int
}

func (f *fakeMatchFolderLookup) GetFolderIDForItem(_ context.Context, contentID string) (int, error) {
	if fid, ok := f.folders[contentID]; ok {
		return fid, nil
	}
	return 0, fmt.Errorf("no folder for %s", contentID)
}

func (f *fakeMatchFolderLookup) GetFolderIDsForItem(_ context.Context, contentID string) ([]int, error) {
	if ids, ok := f.folderIDs[contentID]; ok {
		return ids, nil
	}
	if fid, ok := f.folders[contentID]; ok {
		return []int{fid}, nil
	}
	return nil, fmt.Errorf("no folders for %s", contentID)
}

type fakeMatchMetadataService struct {
	searchResults []metadata.MatchCandidate
	searchErr     error
	processResult *metadata.ProcessResult
	processErr    error
	lastProcess   metadata.ProcessRequest
}

func (f *fakeMatchMetadataService) SearchAndNormalize(_ context.Context, _ metadata.SearchQuery, _ int) ([]metadata.MatchCandidate, error) {
	return f.searchResults, f.searchErr
}

func (f *fakeMatchMetadataService) Process(_ context.Context, req metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	f.lastProcess = req
	return f.processResult, f.processErr
}

// --- Helpers ---

// --- Tests ---

// capturingMetadataService wraps a fake to capture the query passed to SearchAndNormalize.
type capturingMetadataService struct {
	inner        *fakeMatchMetadataService
	lastQuery    metadata.SearchQuery
	lastFolderID int
}

func (c *capturingMetadataService) SearchAndNormalize(ctx context.Context, query metadata.SearchQuery, folderID int) ([]metadata.MatchCandidate, error) {
	c.lastQuery = query
	c.lastFolderID = folderID
	return c.inner.SearchAndNormalize(ctx, query, folderID)
}

func (c *capturingMetadataService) Process(ctx context.Context, req metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	return c.inner.Process(ctx, req)
}

// A match can move the item to a new content ID. Cached home rails still list
// the old ID, so both IDs must reach the other API nodes.
func TestAdminMatchApply_PublishesOldAndNewContentIDs(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"movie-tmdb-124905": {ContentID: "movie-tmdb-124905", Title: "Godzilla", Year: 2014, Type: "movie"},
		},
	}
	metaSvc := &fakeMatchMetadataService{
		processResult: &metadata.ProcessResult{ContentID: "movie-tmdb-929", Updated: true},
	}
	bus := &recordingEventBus{}
	h := NewAdminMatchHandler(items, nil, metaSvc)
	h.EventBus = bus

	if _, err := h.ApplyAdminItemMatch(context.Background(), "movie-tmdb-124905", AdminMatchApplyRequest{
		ProviderIDs: map[string]string{"tmdb": "929"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var got []string
	for _, event := range bus.events {
		if event.Type == cache.EventCatalogItemChanged {
			got = append(got, event.Payload)
		}
	}
	if len(got) != 2 || got[0] != "movie-tmdb-124905" || got[1] != "movie-tmdb-929" {
		t.Fatalf("catalog item changes = %v, want old then new content ID", got)
	}
}

// A search falls back to the item's own title, year, and type, normalizes
// provider IDs, and searches the library the caller names.
func TestSearchAdminItemMatchesBuildsTheQuery(t *testing.T) {
	items := &fakeMatchItemLookup{items: map[string]*models.MediaItem{
		"item-2":  {ContentID: "item-2", Title: "Inception", Year: 2010, Type: "movie"},
		"ebook-1": {ContentID: "ebook-1", Title: "Example Book", Year: 2024, Type: "ebook"},
	}}
	folders := &fakeMatchFolderLookup{folderIDs: map[string][]int{"ebook-1": {7, 8}}}

	capture := &capturingMetadataService{inner: &fakeMatchMetadataService{searchResults: []metadata.MatchCandidate{}}}
	if _, err := NewAdminMatchHandler(items, nil, capture).SearchAdminItemMatches(t.Context(), "item-2", AdminMatchSearchRequest{}); err != nil {
		t.Fatal(err)
	}
	if capture.lastQuery.Title != "Inception" || capture.lastQuery.Year != 2010 {
		t.Fatalf("query = %+v, want the item's title and year", capture.lastQuery)
	}

	capture = &capturingMetadataService{inner: &fakeMatchMetadataService{searchResults: []metadata.MatchCandidate{}}}
	libraryID := 8
	if _, err := NewAdminMatchHandler(items, folders, capture).SearchAdminItemMatches(t.Context(), "ebook-1", AdminMatchSearchRequest{
		ProviderIDs: map[string]string{" ISBN ": " 9780000000001 ", "goodreads": " "},
		TmdbID:      "12345",
		LibraryID:   &libraryID,
	}); err != nil {
		t.Fatal(err)
	}
	query := capture.lastQuery
	if capture.lastFolderID != 8 || query.ContentType != "ebook" || query.ProviderIDs["isbn"] != "9780000000001" || query.ProviderIDs["tmdb"] != "12345" {
		t.Fatalf("folder %d query %+v", capture.lastFolderID, query)
	}
	if _, ok := query.ProviderIDs["goodreads"]; ok {
		t.Fatalf("blank provider IDs should be ignored, got %v", query.ProviderIDs)
	}
}

// Apply identifies the existing item by its content ID with normalized
// provider IDs: a key like " TMDB " must not slip past the provider-ID
// handling downstream, and a request with no usable ID is refused.
func TestApplyAdminItemMatchNormalizesProviderIDs(t *testing.T) {
	items := &fakeMatchItemLookup{items: map[string]*models.MediaItem{"item-5": {ContentID: "item-5", Title: "Test", Type: "movie"}}}

	metaSvc := &fakeMatchMetadataService{processResult: &metadata.ProcessResult{ContentID: "item-5", Updated: true}}
	result, err := NewAdminMatchHandler(items, nil, metaSvc).ApplyAdminItemMatch(t.Context(), "item-5", AdminMatchApplyRequest{
		ProviderIDs: map[string]string{" TMDB ": " 157336 ", "blank": "   "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentID != "item-5" || !result.Updated {
		t.Fatalf("result = %+v", result)
	}
	process := metaSvc.lastProcess
	if process.ContentID != "item-5" || process.Mode != metadata.ModeIdentify || len(process.ProviderIDs) != 1 || process.ProviderIDs["tmdb"] != "157336" {
		t.Fatalf("Process request = %+v, want identify of item-5 with tmdb=157336 only", process)
	}

	metaSvc = &fakeMatchMetadataService{}
	_, err = NewAdminMatchHandler(items, nil, metaSvc).ApplyAdminItemMatch(t.Context(), "item-5", AdminMatchApplyRequest{
		ProviderIDs: map[string]string{"tmdb": "   ", " ": "123"},
	})
	requireAdminError(t, err, http.StatusBadRequest, "")
	if metaSvc.lastProcess.ContentID != "" {
		t.Fatalf("all-blank provider IDs reached Process: %+v", metaSvc.lastProcess)
	}
}

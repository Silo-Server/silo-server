package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

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

func buildMatchRouter(h *AdminMatchHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/admin/items/{id}/match/search", h.HandleSearchItemMatchCandidates)
	r.Post("/admin/items/{id}/match/apply", h.HandleApplyItemMatch)
	return r
}

// --- Tests ---

func TestAdminMatchSearch_ReturnsCandidatesArray(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"item-1": {ContentID: "item-1", Title: "The Matrix", Year: 1999, Type: "movie"},
		},
	}
	folders := &fakeMatchFolderLookup{
		folders: map[string]int{"item-1": 10},
	}
	metaSvc := &fakeMatchMetadataService{
		searchResults: []metadata.MatchCandidate{
			{
				Title:          "The Matrix",
				Year:           1999,
				ContentType:    "movie",
				ProviderIDs:    map[string]string{"tmdb": "603", "imdb": "tt0133093"},
				Sources:        []string{"tmdb"},
				AgreementHints: []string{"agreed_by_tmdb_and_tvdb"},
			},
			{
				Title:       "The Matrix Reloaded",
				Year:        2003,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "604"},
				Sources:     []string{"tmdb"},
			},
		},
	}

	h := NewAdminMatchHandler(items, folders, metaSvc)
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchSearchRequest{Title: "The Matrix"})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-1/match/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp matchSearchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(resp.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(resp.Candidates))
	}

	// Verify candidate structure.
	c0 := resp.Candidates[0]
	if c0.Title != "The Matrix" {
		t.Errorf("expected title 'The Matrix', got %q", c0.Title)
	}
	if c0.ProviderIDs["tmdb"] != "603" {
		t.Errorf("expected tmdb=603, got %q", c0.ProviderIDs["tmdb"])
	}
	if len(c0.AgreementHints) != 1 || c0.AgreementHints[0] != "agreed_by_tmdb_and_tvdb" {
		t.Errorf("expected agreement_hints, got %v", c0.AgreementHints)
	}
}

func TestAdminMatchSearch_ItemNotFound(t *testing.T) {
	items := &fakeMatchItemLookup{items: map[string]*models.MediaItem{}}
	h := NewAdminMatchHandler(items, nil, &fakeMatchMetadataService{})
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchSearchRequest{Title: "foo"})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/nonexistent/match/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminMatchSearch_FallsBackToItemMetadata(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"item-2": {ContentID: "item-2", Title: "Inception", Year: 2010, Type: "movie"},
		},
	}
	var capturedQuery metadata.SearchQuery
	metaSvc := &fakeMatchMetadataService{
		searchResults: []metadata.MatchCandidate{},
	}
	// Wrap to capture the query.
	captureSvc := &capturingMetadataService{
		inner: metaSvc,
	}

	h := NewAdminMatchHandler(items, nil, captureSvc)
	router := buildMatchRouter(h)

	// Send empty title/year -- handler should use item's metadata.
	body, _ := json.Marshal(matchSearchRequest{})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-2/match/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	capturedQuery = captureSvc.lastQuery
	if capturedQuery.Title != "Inception" {
		t.Errorf("expected title fallback to 'Inception', got %q", capturedQuery.Title)
	}
	if capturedQuery.Year != 2010 {
		t.Errorf("expected year fallback to 2010, got %d", capturedQuery.Year)
	}
}

func TestAdminMatchSearch_AcceptsGenericProviderIDsAndLibraryID(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"ebook-1": {ContentID: "ebook-1", Title: "Example Book", Year: 2024, Type: "ebook"},
		},
	}
	folders := &fakeMatchFolderLookup{
		folderIDs: map[string][]int{"ebook-1": {7, 8}},
	}
	captureSvc := &capturingMetadataService{
		inner: &fakeMatchMetadataService{searchResults: []metadata.MatchCandidate{}},
	}

	h := NewAdminMatchHandler(items, folders, captureSvc)
	router := buildMatchRouter(h)

	libraryID := 8
	body, _ := json.Marshal(matchSearchRequest{
		ProviderIDs: map[string]string{
			" ISBN ":    " 9780000000001 ",
			"goodreads": " ",
		},
		TmdbID:    "12345",
		LibraryID: &libraryID,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/ebook-1/match/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if captureSvc.lastFolderID != 8 {
		t.Fatalf("folderID = %d, want 8", captureSvc.lastFolderID)
	}
	if captureSvc.lastQuery.ContentType != "ebook" {
		t.Fatalf("ContentType = %q, want ebook", captureSvc.lastQuery.ContentType)
	}
	if got := captureSvc.lastQuery.ProviderIDs["isbn"]; got != "9780000000001" {
		t.Fatalf("ProviderIDs[isbn] = %q, want 9780000000001", got)
	}
	if got := captureSvc.lastQuery.ProviderIDs["tmdb"]; got != "12345" {
		t.Fatalf("ProviderIDs[tmdb] = %q, want 12345", got)
	}
	if _, ok := captureSvc.lastQuery.ProviderIDs["goodreads"]; ok {
		t.Fatalf("blank provider IDs should be ignored, got %v", captureSvc.lastQuery.ProviderIDs)
	}
}

func TestAdminMatchApply_PreservesContentID(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"item-3": {ContentID: "item-3", Title: "Interstellar", Year: 2014, Type: "movie"},
		},
	}
	metaSvc := &fakeMatchMetadataService{
		processResult: &metadata.ProcessResult{
			ContentID: "item-3",
			Updated:   true,
		},
	}

	h := NewAdminMatchHandler(items, nil, metaSvc)
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchApplyRequest{
		ProviderIDs: map[string]string{"tmdb": "157336"},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-3/match/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp matchApplyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// The original content_id must be preserved (not replaced with a new ID).
	if resp.ContentID != "item-3" {
		t.Errorf("expected content_id 'item-3' preserved, got %q", resp.ContentID)
	}
	if !resp.Updated {
		t.Error("expected updated=true")
	}

	// Verify the Process call used ModeIdentify with the original content_id.
	if metaSvc.lastProcess.ContentID != "item-3" {
		t.Errorf("expected Process called with content_id 'item-3', got %q", metaSvc.lastProcess.ContentID)
	}
	if metaSvc.lastProcess.Mode != metadata.ModeIdentify {
		t.Errorf("expected ModeIdentify, got %d", metaSvc.lastProcess.Mode)
	}
	if metaSvc.lastProcess.ProviderIDs["tmdb"] != "157336" {
		t.Errorf("expected provider_ids tmdb=157336, got %v", metaSvc.lastProcess.ProviderIDs)
	}
}

func TestAdminMatchApply_RequiresProviderIDs(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"item-4": {ContentID: "item-4", Title: "Test", Type: "movie"},
		},
	}
	h := NewAdminMatchHandler(items, nil, &fakeMatchMetadataService{})
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchApplyRequest{ProviderIDs: map[string]string{}})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-4/match/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty provider_ids, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminMatchApply_NormalizesProviderIDKeys(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"item-5": {ContentID: "item-5", Title: "Test", Type: "movie"},
		},
	}
	metaSvc := &fakeMatchMetadataService{
		processResult: &metadata.ProcessResult{ContentID: "item-5", Updated: true},
	}
	h := NewAdminMatchHandler(items, nil, metaSvc)
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchApplyRequest{
		ProviderIDs: map[string]string{
			" TMDB ": " 157336 ",
			"blank":  "   ",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-5/match/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := metaSvc.lastProcess.ProviderIDs["tmdb"]; got != "157336" {
		t.Errorf("Process provider_ids[tmdb] = %q, want 157336 (keys/values must be normalized)", got)
	}
	if _, ok := metaSvc.lastProcess.ProviderIDs[" TMDB "]; ok {
		t.Errorf("Process received raw key %q, want normalized keys only: %v", " TMDB ", metaSvc.lastProcess.ProviderIDs)
	}
	if _, ok := metaSvc.lastProcess.ProviderIDs["blank"]; ok {
		t.Errorf("blank provider IDs should be dropped, got %v", metaSvc.lastProcess.ProviderIDs)
	}
}

func TestAdminMatchApply_RejectsAllBlankProviderIDs(t *testing.T) {
	items := &fakeMatchItemLookup{
		items: map[string]*models.MediaItem{
			"item-6": {ContentID: "item-6", Title: "Test", Type: "movie"},
		},
	}
	h := NewAdminMatchHandler(items, nil, &fakeMatchMetadataService{})
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchApplyRequest{
		ProviderIDs: map[string]string{"tmdb": "   ", " ": "123"},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/item-6/match/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for all-blank provider_ids, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminMatchApply_ItemNotFound(t *testing.T) {
	items := &fakeMatchItemLookup{items: map[string]*models.MediaItem{}}
	h := NewAdminMatchHandler(items, nil, &fakeMatchMetadataService{})
	router := buildMatchRouter(h)

	body, _ := json.Marshal(matchApplyRequest{ProviderIDs: map[string]string{"tmdb": "123"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/items/nonexistent/match/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

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

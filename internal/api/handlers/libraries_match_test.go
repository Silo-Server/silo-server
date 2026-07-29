package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
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

func (noopMovieMatchQueue) CountStatesByFolder(context.Context, int) (int, int, error) {
	return 0, 0, nil
}
func (noopMovieMatchQueue) CountStatesByFolders(context.Context, []int) (map[int]metadata.MatchQueueStateCounts, error) {
	return map[int]metadata.MatchQueueStateCounts{}, nil
}

func (noopMovieMatchQueue) ListByFolder(context.Context, int, int, int) ([]models.MovieMatchQueueEntry, int, error) {
	return nil, 0, nil
}

func (noopMovieMatchQueue) RetryNowByFolder(context.Context, int) (int, error) {
	return 0, nil
}

type fakeMovieMatchQueue struct {
	pending      int
	parked       int
	entries      []models.MovieMatchQueueEntry
	retryFolders []int
	bulkCalls    int
}

func (f *fakeMovieMatchQueue) SyncForFolder(context.Context, int) error { return nil }
func (f *fakeMovieMatchQueue) DeleteByFolder(context.Context, int) (int, error) {
	return 0, nil
}
func (f *fakeMovieMatchQueue) CountStatesByFolder(context.Context, int) (int, int, error) {
	return f.pending, f.parked, nil
}
func (f *fakeMovieMatchQueue) CountStatesByFolders(_ context.Context, folderIDs []int) (map[int]metadata.MatchQueueStateCounts, error) {
	f.bulkCalls++
	counts := make(map[int]metadata.MatchQueueStateCounts, len(folderIDs))
	for _, folderID := range folderIDs {
		counts[folderID] = metadata.MatchQueueStateCounts{Pending: f.pending, Parked: f.parked}
	}
	return counts, nil
}
func (f *fakeMovieMatchQueue) ListByFolder(context.Context, int, int, int) ([]models.MovieMatchQueueEntry, int, error) {
	return f.entries, len(f.entries), nil
}
func (f *fakeMovieMatchQueue) RetryNowByFolder(_ context.Context, folderID int) (int, error) {
	f.retryFolders = append(f.retryFolders, folderID)
	return len(f.retryFolders), nil
}

type fakeSeriesMatchQueue struct {
	pending      int
	parked       int
	entries      []models.SeriesRootMatchQueueEntry
	retryFolders []int
	bulkCalls    int
}

func (f *fakeSeriesMatchQueue) SyncForFolder(context.Context, int) error { return nil }
func (f *fakeSeriesMatchQueue) DeleteByFolder(context.Context, int) (int, error) {
	return 0, nil
}
func (f *fakeSeriesMatchQueue) CountStatesByFolder(context.Context, int) (int, int, error) {
	return f.pending, f.parked, nil
}
func (f *fakeSeriesMatchQueue) CountStatesByFolders(_ context.Context, folderIDs []int) (map[int]metadata.MatchQueueStateCounts, error) {
	f.bulkCalls++
	counts := make(map[int]metadata.MatchQueueStateCounts, len(folderIDs))
	for _, folderID := range folderIDs {
		counts[folderID] = metadata.MatchQueueStateCounts{Pending: f.pending, Parked: f.parked}
	}
	return counts, nil
}
func (f *fakeSeriesMatchQueue) ListByFolder(context.Context, int, int, int) ([]models.SeriesRootMatchQueueEntry, int, error) {
	return f.entries, len(f.entries), nil
}
func (f *fakeSeriesMatchQueue) RetryNowByFolder(_ context.Context, folderID int) (int, error) {
	f.retryFolders = append(f.retryFolders, folderID)
	return len(f.retryFolders), nil
}

type fakeRawMatchBacklog struct {
	count     int
	bulkCalls int
}

func (f *fakeRawMatchBacklog) CountUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode) (int, error) {
	return f.count, nil
}
func (f *fakeRawMatchBacklog) CountUnmatchedMatchBacklogByFolders(_ context.Context, folderIDs []int, _ scanner.RawMatchBacklogMode) (map[int]int, error) {
	f.bulkCalls++
	counts := make(map[int]int, len(folderIDs))
	for _, folderID := range folderIDs {
		counts[folderID] = f.count
	}
	return counts, nil
}
func (f *fakeRawMatchBacklog) ListUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode, int, int) ([]*models.MediaFile, int, error) {
	return nil, 0, nil
}
func (f *fakeRawMatchBacklog) SuppressUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode) (int, error) {
	return 0, nil
}
func (f *fakeRawMatchBacklog) RetryUnmatchedMatchBacklogByFolder(context.Context, int, scanner.RawMatchBacklogMode) (int, error) {
	return 0, nil
}

// TestMetadataMatchQueueStatus_AggregatesStates verifies the pending/parked
// aggregation rules the admin UI depends on: parked rows stay out of
// pending_count, raw-backlog files count as pending, and the per-queue totals
// still equal pending + parked so movie_count/series_count semantics are
// unchanged from the pre-state API.
func TestMetadataMatchQueueStatus_AggregatesStates(t *testing.T) {
	h := &LibraryHandler{
		MovieMatchQueueRepo:  &fakeMovieMatchQueue{pending: 2, parked: 1},
		SeriesMatchQueueRepo: &fakeSeriesMatchQueue{pending: 3, parked: 2},
		RawMatchBacklogRepo:  &fakeRawMatchBacklog{count: 4},
	}

	status, err := h.metadataMatchQueueStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("metadataMatchQueueStatus() error = %v", err)
	}
	if status.LibraryID != 7 {
		t.Errorf("LibraryID = %d, want 7", status.LibraryID)
	}
	if status.MovieCount != 3 || status.SeriesCount != 5 || status.RawFileCount != 4 {
		t.Errorf("per-queue counts = %d/%d/%d, want 3/5/4", status.MovieCount, status.SeriesCount, status.RawFileCount)
	}
	if status.PendingCount != 9 {
		t.Errorf("PendingCount = %d, want 9 (2+3 pending + 4 raw)", status.PendingCount)
	}
	if status.ParkedCount != 3 {
		t.Errorf("ParkedCount = %d, want 3", status.ParkedCount)
	}
	if status.TotalCount != 12 {
		t.Errorf("TotalCount = %d, want 12", status.TotalCount)
	}
}

func TestMetadataMatchQueueStatusesUsesOneAggregateCallPerQueue(t *testing.T) {
	movie := &fakeMovieMatchQueue{pending: 1}
	series := &fakeSeriesMatchQueue{parked: 1}
	raw := &fakeRawMatchBacklog{count: 1}
	h := &LibraryHandler{
		MovieMatchQueueRepo:  movie,
		SeriesMatchQueueRepo: series,
		RawMatchBacklogRepo:  raw,
	}

	statuses, err := h.metadataMatchQueueStatuses(context.Background(), []int{3, 7, 11})
	if err != nil {
		t.Fatalf("metadataMatchQueueStatuses() error = %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("status count = %d, want 3", len(statuses))
	}
	if movie.bulkCalls != 1 || series.bulkCalls != 1 || raw.bulkCalls != 1 {
		t.Fatalf("bulk calls = movie:%d series:%d raw:%d, want one each", movie.bulkCalls, series.bulkCalls, raw.bulkCalls)
	}
}

// TestWakeMetadataMatcher_RetriesBothQueues verifies the provider-chain-change
// wake path resets parked work in both queue repos for the changed library.
func TestWakeMetadataMatcher_RetriesBothQueues(t *testing.T) {
	movie := &fakeMovieMatchQueue{}
	series := &fakeSeriesMatchQueue{}
	h := &LibraryHandler{MovieMatchQueueRepo: movie, SeriesMatchQueueRepo: series}

	h.wakeMetadataMatcher(context.Background(), 42)

	if len(movie.retryFolders) != 1 || movie.retryFolders[0] != 42 {
		t.Errorf("movie retry folders = %v, want [42]", movie.retryFolders)
	}
	if len(series.retryFolders) != 1 || series.retryFolders[0] != 42 {
		t.Errorf("series retry folders = %v, want [42]", series.retryFolders)
	}
}

// TestMovieMatchQueueEntryResponse_SerializesStateFields pins the JSON contract
// for the queue-state fields the admin UI reads. Additive-only API rule: these
// names must never change once shipped.
func TestMovieMatchQueueEntryResponse_SerializesStateFields(t *testing.T) {
	parkedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	entry := libraryMovieMatchQueueEntryResponse{
		MediaFileID:               11,
		MediaFolderID:             7,
		FilePath:                  "/movies/a.mkv",
		State:                     "parked",
		FailureKind:               "candidate_rejected",
		FailureDetail:             json.RawMessage(`{"message":"below threshold"}`),
		DeterministicAttemptCount: 3,
		MatcherRevision:           8,
		ParkedAt:                  &parkedAt,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for key, want := range map[string]any{
		"state":                       "parked", //nolint:goconst // JSON contract key.
		"failure_kind":                "candidate_rejected",
		"deterministic_attempt_count": float64(3),
		"matcher_revision":            float64(8),
	} {
		if m[key] != want {
			t.Errorf("JSON %q = %v, want %v", key, m[key], want)
		}
	}
	if _, ok := m["parked_at"]; !ok {
		t.Error("expected parked_at in JSON output")
	}
	if _, ok := m["failure_detail"]; !ok {
		t.Error("expected failure_detail in JSON output")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

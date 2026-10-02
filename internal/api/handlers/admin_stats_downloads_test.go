package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubDownloadsStatsSource struct {
	stats       *AdminDownloadsStats
	err         error
	gotLimit    int
	invalidated int
	callCount   int
}

func (s *stubDownloadsStatsSource) Get(_ context.Context, limit int) (*AdminDownloadsStats, error) {
	s.callCount++
	s.gotLimit = limit
	return s.stats, s.err
}

func (s *stubDownloadsStatsSource) Invalidate() { s.invalidated++ }

func TestHandleGetDownloadsStatsClampsLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{name: "default", query: "", wantLimit: adminDownloadsStatsDefaultLimit},
		{name: "explicit value passes through", query: "?limit=5", wantLimit: 5},
		{name: "zero clamps up", query: "?limit=0", wantLimit: adminDownloadsStatsMinLimit},
		{name: "oversized clamps down", query: "?limit=999", wantLimit: adminDownloadsStatsMaxLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &stubDownloadsStatsSource{stats: &AdminDownloadsStats{}}
			handler := &AdminHandler{DownloadsStatsSource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetDownloadsStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/downloads"+tt.query, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}
			if source.gotLimit != tt.wantLimit {
				t.Fatalf("limit = %d, want %d", source.gotLimit, tt.wantLimit)
			}
		})
	}
}

func TestHandleGetDownloadsStatsRejectsNonNumericLimit(t *testing.T) {
	t.Parallel()

	source := &stubDownloadsStatsSource{stats: &AdminDownloadsStats{}}
	handler := &AdminHandler{DownloadsStatsSource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetDownloadsStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/downloads?limit=all", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if source.callCount != 0 {
		t.Fatalf("source was queried %d times for an invalid request", source.callCount)
	}
}

func TestHandleGetDownloadsStatsRefreshInvalidates(t *testing.T) {
	t.Parallel()

	source := &stubDownloadsStatsSource{stats: &AdminDownloadsStats{}}
	handler := &AdminHandler{DownloadsStatsSource: source}

	rec := httptest.NewRecorder()
	handler.HandleGetDownloadsStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/downloads?refresh=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if source.invalidated != 1 {
		t.Fatalf("invalidated = %d, want 1", source.invalidated)
	}
}

func TestHandleGetDownloadsStatsSourceFailureIs500(t *testing.T) {
	t.Parallel()

	source := &stubDownloadsStatsSource{err: errors.New("boom")}
	handler := &AdminHandler{DownloadsStatsSource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetDownloadsStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/downloads", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHandleGetDownloadsStatsWithoutDatabase(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{}
	rec := httptest.NewRecorder()
	handler.HandleGetDownloadsStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/downloads", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A deployment where nobody downloads must answer zeros with an empty array,
// not nulls: the widget maps over top_users directly and reads all-zero
// headline numbers as its empty state.
func TestAdminDownloadsStatsEmptyListSerializesAsArray(t *testing.T) {
	t.Parallel()

	source := &stubDownloadsStatsSource{stats: &AdminDownloadsStats{
		Limit:    10,
		TopUsers: []AdminDownloadsUser{},
	}}
	handler := &AdminHandler{DownloadsStatsSource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetDownloadsStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/downloads", nil))

	var body struct {
		TopUsers []AdminDownloadsUser `json:"top_users"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.TopUsers == nil {
		t.Fatalf("top_users decoded as null: %s", rec.Body.String())
	}
}

func TestAdminDownloadsStatsProviderInvalidateClearsEveryVariant(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminDownloadsStatsProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	keys := []string{
		adminDownloadsStatsCachePrefix + "10",
		adminDownloadsStatsCachePrefix + "25",
	}
	for _, key := range keys {
		provider.cache.Set(key, &AdminDownloadsStats{}, time.Minute)
	}

	provider.Invalidate()

	for _, key := range keys {
		if _, ok := provider.cache.Get(key); ok {
			t.Fatalf("%s survived Invalidate", key)
		}
	}
}

func TestAdminDownloadsStatsProviderWithoutPool(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminDownloadsStatsProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	if _, err := provider.Get(context.Background(), 10); err == nil {
		t.Fatal("expected an error from a provider with no pool")
	}
}

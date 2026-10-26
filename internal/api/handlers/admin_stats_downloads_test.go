package handlers

import (
	"context"
	"encoding/json"
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

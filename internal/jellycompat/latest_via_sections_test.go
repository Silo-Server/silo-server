package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// seriesEpisodeSource is an episodeListSource that returns a fixed episode set
// per series via ListBySeriesIDs (the only method the series watch-state rollup
// uses). Other methods are unused by this path.
type seriesEpisodeSource struct {
	bySeries  map[string][]*models.Episode
	listCalls int
}

func (s *seriesEpisodeSource) ListBySeason(context.Context, string, int) ([]*models.Episode, error) {
	return nil, nil
}

func (s *seriesEpisodeSource) ListBySeriesGroupedBySeason(context.Context, string) (map[int][]*models.Episode, error) {
	return nil, nil
}

func (s *seriesEpisodeSource) ListBySeriesIDs(_ context.Context, ids []string) (map[string][]*models.Episode, error) {
	s.listCalls++
	out := make(map[string][]*models.Episode, len(ids))
	for _, id := range ids {
		if eps, ok := s.bySeries[id]; ok {
			out[id] = eps
		}
	}
	return out, nil
}

func (s *seriesEpisodeSource) GetByIDs(context.Context, []string) ([]*models.Episode, error) {
	return nil, nil
}

// completedProgressStoreProvider hands back a fixed completedProgressStore.
type completedProgressStoreProvider struct {
	store userstore.UserStore
}

func (p *completedProgressStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p *completedProgressStoreProvider) Close() error { return nil }

// TestLatestLimitZeroQueriesNothing: Latest has no total to report, so
// Limit=0 answers an empty array without browsing (the stub's BrowseItems
// panics).
func TestLatestLimitZeroQueriesNothing(t *testing.T) {
	h := NewItemsHandler(&stubContentService{}, nil, NewResourceIDCodec(), &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/Items/Latest?Limit=0&IsPlayed=false&EnableTotalRecordCount=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token", ProfileID: "profile-1", StreamAppUserID: 1}))
	rec := httptest.NewRecorder()
	h.HandleLatest(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("Limit=0 Latest = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// limitRecordingReader records the limit each row read was asked for.
type limitRecordingReader struct {
	stubRecommendationsReader
	limits []int
}

func (r *limitRecordingReader) GetForYouMain(_ context.Context, _ int, _ string, limit int, _ catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	r.limits = append(r.limits, limit)
	return nil, nil
}

func (r *limitRecordingReader) GetForYouRows(_ context.Context, _ int, _ string, limit int, _ catalog.AccessFilter) ([]recommendations.ForYouRow, error) {
	r.limits = append(r.limits, limit)
	return nil, nil
}

func (r *limitRecordingReader) GetSimilarUsersLiked(_ context.Context, _ int, _ string, limit int, _ catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	r.limits = append(r.limits, limit)
	return nil, nil
}

// The frozen v1 row reads keep answering at most 20 items per row, the
// most the reader served before it allowed v2's 50; the shared service
// passes a v2 limit through.
func TestV1RecommendationRowReadsKeepTheirTwentyItemCap(t *testing.T) {
	reader := &limitRecordingReader{}
	h := NewRecommendationsHandler(nil, reader, nil, nil, nil, false)
	for _, handle := range []http.HandlerFunc{h.HandleForYouMain, h.HandleForYouRows, h.HandleSimilarUsers} {
		for _, tc := range []struct {
			query string
			want  int
		}{{"", 20}, {"?limit=50", 20}, {"?limit=5", 5}} {
			reader.limits = nil
			req := httptest.NewRequest(http.MethodGet, "/recommendations"+tc.query, nil)
			req = req.WithContext(authenticatedRecsContext(req.Context(), nil))
			rec := httptest.NewRecorder()
			handle(rec, req)
			if rec.Code != http.StatusOK || len(reader.limits) != 1 || reader.limits[0] != tc.want {
				t.Fatalf("query %q: status %d, reader limits %v; want %d", tc.query, rec.Code, reader.limits, tc.want)
			}
		}
	}

	reader.limits = nil
	ctx := authenticatedRecsContext(context.Background(), nil)
	if _, err := h.ForYouMain(ctx, 7, "profile-1", 50, catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SimilarUsersLiked(ctx, 7, "profile-1", 50, catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	if len(reader.limits) != 2 || reader.limits[0] != 50 || reader.limits[1] != 50 {
		t.Fatalf("service limits = %v, want [50 50]", reader.limits)
	}
}

type failingStoreProvider struct{}

func (failingStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, errors.New("user store unavailable")
}

func (failingStoreProvider) Close() error { return nil }

// A user store failure leaves the list unfiltered instead of answering the
// watched set from Postgres progress, which a SQLite user store does not
// keep. The repo has no pool, so a fallback read would panic.
func TestWatchedFilterDoesNotFallBackToPostgres(t *testing.T) {
	h := NewRecommendationsHandler(nil, nil, failingStoreProvider{}, nil, recommendations.NewRepo(nil), false)
	items := []recommendations.ScoredItem{{MediaItemID: "a"}, {MediaItemID: "b"}}
	got := h.excludeWatchedRecommendations(context.Background(), 7, "profile-1", items)
	if len(got) != 2 {
		t.Fatalf("items = %v, want both left in", got)
	}
	if _, err := h.watchedItemIDSet(context.Background(), 7, "profile-1"); err == nil {
		t.Fatal("watched set error was swallowed")
	}
}

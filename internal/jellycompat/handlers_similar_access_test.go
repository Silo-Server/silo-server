package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// similarRecommender answers every Similar request with neighbors and
// records the seeds it was asked about.
type similarRecommender struct {
	recommendations.Recommender
	neighbors []recommendations.ScoredItem
	seeds     []string
}

func (r *similarRecommender) SimilarItems(_ context.Context, itemID string, _ int) ([]recommendations.ScoredItem, error) {
	r.seeds = append(r.seeds, itemID)
	return r.neighbors, nil
}

func similarRequest(t *testing.T, h *ItemsHandler, seed string) queryResultDTO {
	t.Helper()
	id := h.codec.EncodeStringID(EncodedIDItem, seed)
	req := httptest.NewRequest(http.MethodGet, "/Items/"+id+"/Similar", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"}))
	rec := httptest.NewRecorder()
	h.HandleSimilar(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// A Similar list built from an item the session may not see would confirm
// that the item exists, so the recommender is not asked about it.
func TestHandleSimilarSkipsRecommenderForHiddenSeed(t *testing.T) {
	codec := NewResourceIDCodec()
	recommender := &similarRecommender{neighbors: []recommendations.ScoredItem{{MediaItemID: "movie-visible"}}}
	h := &ItemsHandler{
		codec:       codec,
		mapper:      newMapper(codec, nil),
		content:     &stubContentService{detail: &upstreamItemDetail{}},
		recommender: recommender,
		// The access-filtered loader finds only the visible neighbor.
		itemRepo: &countingItemRepo{itemsByID: map[string]*models.MediaItem{
			"movie-visible": {ContentID: "movie-visible", Type: "movie", Title: "Visible"},
		}},
	}

	if got := similarRequest(t, h, "movie-hidden"); got.TotalRecordCount != 0 || len(got.Items) != 0 {
		t.Fatalf("hidden seed answered %+v, want an empty list", got)
	}
	if len(recommender.seeds) != 0 {
		t.Fatalf("recommender asked about %v, want no call for a hidden seed", recommender.seeds)
	}

	h.itemRepo.(*countingItemRepo).itemsByID["movie-seed"] = &models.MediaItem{ContentID: "movie-seed", Type: "movie", Title: "Seed"}
	if got := similarRequest(t, h, "movie-seed"); got.TotalRecordCount != 1 {
		t.Fatalf("visible seed answered %d items, want the visible neighbor", got.TotalRecordCount)
	}
	if len(recommender.seeds) != 1 || recommender.seeds[0] != "movie-seed" {
		t.Fatalf("recommender seeds = %v, want [movie-seed]", recommender.seeds)
	}
}

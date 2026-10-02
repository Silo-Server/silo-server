package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

func TestHandleTasteProfile_NilEngineReturnsEmpty(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, nil, nil, nil, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/recommendations/taste-profile", nil)
	req = req.WithContext(authenticatedRecsContext(req.Context(), nil))
	rec := httptest.NewRecorder()

	handler.HandleTasteProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var summary recommendations.TasteProfileSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if summary.TopGenres == nil || summary.FavoriteDirectors == nil || summary.SignalCounts == nil {
		t.Fatalf("empty summary must serialize as arrays/objects, got %+v", summary)
	}
	if len(summary.TopGenres) != 0 || len(summary.FavoriteDirectors) != 0 || len(summary.SignalCounts) != 0 {
		t.Fatalf("summary = %+v, want empty collections", summary)
	}
}

func TestHandleBecauseWatched_NilEngineReturnsEmpty(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, nil, nil, nil, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/recommendations/because-watched/movie-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("item_id", "movie-1")
	req = req.WithContext(authenticatedRecsContext(req.Context(), rctx))
	rec := httptest.NewRecorder()

	handler.HandleBecauseWatched(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp scoredItemsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Items == nil {
		t.Fatalf("items must be an empty array, got null")
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items = %+v, want empty", resp.Items)
	}
}

func TestHandleSimilar_NilEngineReturnsEmpty(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, nil, nil, nil, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/recommendations/similar/movie-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("item_id", "movie-1")
	req = req.WithContext(authenticatedRecsContext(req.Context(), rctx))
	rec := httptest.NewRecorder()

	handler.HandleSimilar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp scoredItemsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items = %+v, want empty", resp.Items)
	}
}

func authenticatedRecsContext(parent context.Context, rctx *chi.Context) context.Context {
	ctx := parent
	if rctx != nil {
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7})
	return apimw.SetProfileID(ctx, "profile-1")
}

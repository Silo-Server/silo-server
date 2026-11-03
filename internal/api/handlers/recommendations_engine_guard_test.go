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

func authenticatedRecsContext(parent context.Context, rctx *chi.Context) context.Context {
	ctx := parent
	if rctx != nil {
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7})
	return apimw.SetProfileID(ctx, "profile-1")
}

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

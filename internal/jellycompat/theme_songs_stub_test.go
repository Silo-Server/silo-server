package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestHandleThemeSongsStub_IncludesOwnerID verifies the empty ThemeMediaResult
// carries OwnerId: jellyfin-sdk-kotlin models it as non-nullable, so a plain
// query-result envelope would fail client deserialization.
func TestHandleThemeSongsStub_IncludesOwnerID(t *testing.T) {
	h := &ItemsHandler{}

	itemID := "f27caa37e5142225cceded48f6553502"
	req := httptest.NewRequest("GET", "/Items/"+itemID+"/ThemeSongs", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", itemID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleThemeSongsStub(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items            []json.RawMessage `json:"Items"`
		TotalRecordCount int               `json:"TotalRecordCount"`
		OwnerID          *string           `json:"OwnerId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.Items == nil || len(body.Items) != 0 || body.TotalRecordCount != 0 {
		t.Errorf("expected empty Items array; got body=%s", rec.Body.String())
	}
	if body.OwnerID == nil || *body.OwnerID != itemID {
		t.Errorf("expected OwnerId %q; got body=%s", itemID, rec.Body.String())
	}
}

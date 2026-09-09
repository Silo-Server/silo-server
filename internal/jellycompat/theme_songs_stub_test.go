package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestHandleThemeSongsStub_IncludesOwnerID verifies the empty ThemeMediaResult
// carries OwnerId: jellyfin-sdk-kotlin models it as non-nullable, so a plain
// query-result envelope would fail client deserialization.
func TestHandleThemeSongsStub_IncludesOwnerID(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, content: &countingContentService{}}

	itemID := codec.EncodeStringID(EncodedIDItem, "movie")
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

type themeOwnerContent struct {
	countingContentService
	requestedID string
	hidden      bool
}

func (s *themeOwnerContent) GetItemDetail(_ context.Context, _ *Session, id string, _ *int) (*upstreamItemDetail, error) {
	s.requestedID = id
	if s.hidden {
		return nil, &HTTPError{StatusCode: 404, Message: "Item not found"}
	}
	return &upstreamItemDetail{ContentID: id, Type: "season"}, nil
}

func TestThemeRoutesAcceptVisibleSeason(t *testing.T) {
	for _, route := range []string{"ThemeSongs", "ThemeVideos", "ThemeMedia"} {
		for _, hidden := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/hidden=%v", route, hidden), func(t *testing.T) {
				codec := NewResourceIDCodec()
				content := &themeOwnerContent{hidden: hidden}
				h := &ItemsHandler{codec: codec, content: content}
				id := codec.EncodeStringID(EncodedIDSeason, "season-1")
				router := chi.NewRouter()
				handler := h.HandleThemeSongsStub
				if route == "ThemeMedia" {
					handler = h.HandleThemeMedia
				}
				router.Get("/Items/{id}/"+route, handler)
				req := httptest.NewRequest("GET", "/Items/"+id+"/"+route, nil)
				req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"}))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if content.requestedID != "season-1" {
					t.Fatalf("season visibility lookup: %q", content.requestedID)
				}
				if hidden {
					if rec.Code != 404 {
						t.Fatalf("hidden season: %d", rec.Code)
					}
					return
				}
				if rec.Code != 200 {
					t.Fatalf("visible season: %d %s", rec.Code, rec.Body.String())
				}
				results := map[string]themeMediaResultDTO{}
				if route == "ThemeMedia" {
					if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
						t.Fatal(err)
					}
					if len(results) != 3 {
						t.Fatalf("theme envelopes: %+v", results)
					}
				} else {
					var result themeMediaResultDTO
					if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					results[route] = result
				}
				for _, result := range results {
					if result.OwnerID != id || result.Items == nil || len(result.Items) != 0 || result.TotalRecordCount != 0 {
						t.Fatalf("theme envelope: %+v", result)
					}
				}
			})
		}
	}
}

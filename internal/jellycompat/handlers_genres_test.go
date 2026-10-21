package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// genresContentService serves fixed genre filters; other methods panic.
type genresContentService struct {
	countingContentService
	genres []string
}

func (s *genresContentService) ListItemFilters(context.Context, *Session, url.Values) (*upstreamItemFiltersResponse, error) {
	return &upstreamItemFiltersResponse{Genres: s.genres}, nil
}

func TestHandleGenreByName(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{
		content:  &genresContentService{genres: []string{"Action", "Science Fiction"}},
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   NewImageCache(time.Hour, time.Now),
	}

	router := chi.NewRouter()
	router.Get("/Genres/{name}", h.HandleGenreByName)

	req := httptest.NewRequest("GET", "/Genres/"+url.PathEscape("science fiction"), nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "p1"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto baseItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dto.Type != "Genre" || dto.Name != "Science Fiction" {
		t.Fatalf("expected canonical genre DTO, got %+v", dto)
	}
	if dto.ID != codec.EncodeStringID(EncodedIDGenre, "Science Fiction") {
		t.Fatalf("genre ID does not round-trip: %q", dto.ID)
	}

	req = httptest.NewRequest("GET", "/Genres/Nope", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "p1"}))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for unknown genre, got %d", rec.Code)
	}
}

// Jellyfin returns every genre when Limit is absent and none for Limit=0; the
// default page size of 24 is for item browsing.
func TestHandleGenresListsAllWithoutLimit(t *testing.T) {
	genres := make([]string, 30)
	for i := range genres {
		genres[i] = fmt.Sprintf("Genre %02d", i)
	}
	h := &ItemsHandler{content: &genresContentService{genres: genres}, codec: NewResourceIDCodec()}
	for _, tc := range []struct {
		query     string
		wantItems int
		wantTotal int
		wantFirst string
	}{
		{"", 30, 30, "Genre 00"},
		{"?StartIndex=2", 28, 30, "Genre 02"},
		{"?StartIndex=2&Limit=5", 5, 30, "Genre 02"},
		{"?Limit=5", 5, 30, "Genre 00"},
		{"?Limit=0", 0, 30, ""},
	} {
		req := httptest.NewRequest("GET", "/Genres"+tc.query, nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "p1"}))
		rec := httptest.NewRecorder()
		h.HandleGenres(rec, req)
		var result queryResultDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("%q: %v: %s", tc.query, err, rec.Body.String())
		}
		first := ""
		if len(result.Items) > 0 {
			first = result.Items[0].Name
		}
		if len(result.Items) != tc.wantItems || result.TotalRecordCount != tc.wantTotal || first != tc.wantFirst {
			t.Errorf("%q: items=%d total=%d first=%q, want %d, %d and %q", tc.query, len(result.Items), result.TotalRecordCount, first, tc.wantItems, tc.wantTotal, tc.wantFirst)
		}
	}
}

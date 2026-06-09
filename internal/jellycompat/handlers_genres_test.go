package jellycompat

import (
	"context"
	"encoding/json"
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

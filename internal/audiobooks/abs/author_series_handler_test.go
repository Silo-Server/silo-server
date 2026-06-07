package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type authorSeriesStubMediaStore struct {
	noopMediaStore
	author Author
	series Series
}

func (s *authorSeriesStubMediaStore) GetAuthorByID(_ context.Context, id string, _ catalog.AccessFilter) (Author, error) {
	if id != s.author.ID {
		return Author{}, ErrNotFound
	}
	return s.author, nil
}

func (s *authorSeriesStubMediaStore) GetSeriesByName(_ context.Context, name string, _ catalog.AccessFilter) (Series, error) {
	if name != s.series.ID && name != s.series.Name {
		return Series{}, ErrNotFound
	}
	return s.series, nil
}

func TestAuthor_Detail_ReturnsBooks(t *testing.T) {
	media := &authorSeriesStubMediaStore{
		author: Author{ID: "42", Name: "Brandon Sanderson", Books: []*models.MediaItem{
			{ContentID: "book-1", Title: "Mistborn"},
			{ContentID: "book-2", Title: "Stormlight"},
		}},
	}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/authors/42", map[string]string{"id": "42"}, nil, "1", "", h.handleAuthorDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got["name"] != "Brandon Sanderson" {
		t.Errorf("name = %v", got["name"])
	}
	books, _ := got["books"].([]any)
	if len(books) != 2 {
		t.Errorf("books len = %d, want 2", len(books))
	}
}

func TestAuthor_Detail_Unknown_404(t *testing.T) {
	media := &authorSeriesStubMediaStore{author: Author{ID: "42"}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/authors/99", map[string]string{"id": "99"}, nil, "1", "", h.handleAuthorDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSeries_Detail_ReturnsBooks(t *testing.T) {
	media := &authorSeriesStubMediaStore{
		series: Series{ID: "mistborn", Name: "Mistborn", Books: []*models.MediaItem{
			{ContentID: "b1", Title: "Final Empire"},
			{ContentID: "b2", Title: "Well of Ascension"},
		}},
	}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/series/mistborn", map[string]string{"id": "mistborn"}, nil, "1", "", h.handleSeriesDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got["name"] != "Mistborn" {
		t.Errorf("name = %v", got["name"])
	}
	books, _ := got["books"].([]any)
	if len(books) != 2 {
		t.Errorf("books len = %d, want 2", len(books))
	}
}

func TestSeries_Detail_Unknown_404(t *testing.T) {
	media := &authorSeriesStubMediaStore{series: Series{ID: "mistborn", Name: "Mistborn"}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/series/unknown", map[string]string{"id": "unknown"}, nil, "1", "", h.handleSeriesDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

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
	// Real ABS Author.toOldJSON key set (+ numBooks).
	for _, k := range []string{"id", "asin", "name", "description", "imagePath", "libraryId", "addedAt", "updatedAt", "numBooks"} {
		if _, ok := got[k]; !ok {
			t.Errorf("author object missing key %q", k)
		}
	}
	// Author items are real-ABS minified library items under libraryItems.
	items, _ := got["libraryItems"].([]any)
	if len(items) != 2 {
		t.Errorf("libraryItems len = %d, want 2", len(items))
	}
	if len(items) > 0 {
		b0, _ := items[0].(map[string]any)
		if _, ok := b0["ino"]; !ok {
			t.Errorf("author libraryItem missing minified key 'ino' (thin stub regression)")
		}
	}
}

type libAuthorsStub struct {
	noopMediaStore
	authors []AuthorSummary
}

func (s *libAuthorsStub) ListLibraryAuthors(_ context.Context, _ int64, _, _ int, _ string, _ bool, _ catalog.AccessFilter) ([]AuthorSummary, int, error) {
	return s.authors, len(s.authors), nil
}

// TestLibraryAuthors_EnvelopeBranchesOnPagination guards the real ABS
// LibraryController.getAuthors shape: bare { authors: [...] } when NOT
// paginated, paged { results, total, ... } when limit+page are present.
func TestLibraryAuthors_EnvelopeBranchesOnPagination(t *testing.T) {
	media := &libAuthorsStub{authors: []AuthorSummary{
		{ID: "1", Name: "Alpha", NumBooks: 2},
		{ID: "2", Name: "Beta", NumBooks: 1},
	}}
	h := New(Dependencies{MediaStore: media})
	params := map[string]string{"libraryId": VirtualLibraryID}

	// Non-paginated → { authors: [...] }
	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/"+VirtualLibraryID+"/authors", params, nil, "1", "", h.handleLibraryAuthors)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	authors, ok := got["authors"].([]any)
	if !ok {
		t.Fatalf("non-paginated response missing 'authors' key; got keys %v", keysOf(got))
	}
	if _, isPaged := got["results"]; isPaged {
		t.Errorf("non-paginated response must NOT carry paged 'results'")
	}
	if len(authors) != 2 {
		t.Errorf("authors len = %d, want 2", len(authors))
	}
	if a0, _ := authors[0].(map[string]any); a0 != nil {
		if _, ok := a0["asin"]; !ok {
			t.Errorf("author object missing 'asin' (thin shape regression)")
		}
	}

	// limit present but NO page → still bare { authors: [...] }. Both limit and
	// page are required to trigger the paged envelope; a limit-only request
	// (e.g. Prologue's ?limit=100) must decode via `authors`, not `results`.
	recLimitOnly := dispatchABSWithParams(http.MethodGet, "/api/libraries/"+VirtualLibraryID+"/authors?limit=100", params, nil, "1", "", h.handleLibraryAuthors)
	if recLimitOnly.Code != http.StatusOK {
		t.Fatalf("limit-only status = %d; body=%s", recLimitOnly.Code, recLimitOnly.Body.String())
	}
	var gotLimitOnly map[string]any
	if err := json.Unmarshal(recLimitOnly.Body.Bytes(), &gotLimitOnly); err != nil {
		t.Fatalf("decode limit-only: %v", err)
	}
	if _, ok := gotLimitOnly["authors"].([]any); !ok {
		t.Errorf("limit-only (no page) response missing bare 'authors' key; got keys %v", keysOf(gotLimitOnly))
	}
	if _, isPaged := gotLimitOnly["results"]; isPaged {
		t.Errorf("limit-only (no page) response must NOT carry paged 'results'")
	}

	// Paginated → paged envelope
	rec2 := dispatchABSWithParams(http.MethodGet, "/api/libraries/"+VirtualLibraryID+"/authors?limit=10&page=0", params, nil, "1", "", h.handleLibraryAuthors)
	var got2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &got2); err != nil {
		t.Fatalf("decode paged: %v", err)
	}
	if _, ok := got2["results"]; !ok {
		t.Errorf("paginated response missing 'results'; got keys %v", keysOf(got2))
	}
	if _, ok := got2["total"]; !ok {
		t.Errorf("paginated response missing 'total'")
	}
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
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
	// Real ABS Series.toOldJSON key set (+ numBooks/books).
	for _, k := range []string{"id", "name", "nameIgnorePrefix", "description", "addedAt", "updatedAt", "libraryId", "numBooks"} {
		if _, ok := got[k]; !ok {
			t.Errorf("series object missing key %q", k)
		}
	}
	books, _ := got["books"].([]any)
	if len(books) != 2 {
		t.Errorf("books len = %d, want 2", len(books))
	}
	if len(books) > 0 {
		b0, _ := books[0].(map[string]any)
		if _, ok := b0["ino"]; !ok {
			t.Errorf("series book missing minified key 'ino' (thin stub regression)")
		}
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

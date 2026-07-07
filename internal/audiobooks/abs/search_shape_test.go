package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// searchStubMediaStore backs the /libraries/{id}/search shape tests. It
// embeds noopMediaStore so only the methods the handler exercises need
// overriding.
type searchStubMediaStore struct {
	noopMediaStore
	libs    []AudiobookLibrary
	results []*models.MediaItem
	authors []AuthorSummary
	series  []SeriesSummary
}

func (s *searchStubMediaStore) ListAudiobookLibraries(context.Context, catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return s.libs, nil
}

func (s *searchStubMediaStore) SearchAudiobooks(_ context.Context, _ int64, _ string, _ int, _ catalog.AccessFilter) ([]*models.MediaItem, error) {
	return s.results, nil
}

func (s *searchStubMediaStore) ListLibraryAuthors(context.Context, int64, int, int, string, bool, catalog.AccessFilter) ([]AuthorSummary, int, error) {
	return s.authors, len(s.authors), nil
}

func (s *searchStubMediaStore) ListLibrarySeries(context.Context, int64, int, int, catalog.AccessFilter) ([]SeriesSummary, int, error) {
	return s.series, len(s.series), nil
}

func newSearchHarness() *Handler {
	store := &searchStubMediaStore{
		libs: []AudiobookLibrary{{ID: 1, Name: "Audiobooks", Type: "audiobooks"}},
		results: []*models.MediaItem{
			{ContentID: "book-1", Title: "The Search Result"},
		},
		authors: []AuthorSummary{{ID: "a1", Name: "Search Author", NumBooks: 3}},
		series: []SeriesSummary{{
			ID: "s1", Name: "Search Series", NumBooks: 2,
			Books: []SeriesBookPreview{{ContentID: "book-1", Title: "The Search Result"}},
		}},
	}
	return New(Dependencies{MediaStore: store})
}

// TestLibrarySearch_BucketKeysPresent asserts the response has every bucket
// key real ABS's libraryItemsBookFilters.search() returns (book, narrators,
// tags, genres, series, authors), plus our extra podcast bucket. A missing
// key crashes strict ABS clients that decode the whole envelope up front.
func TestLibrarySearch_BucketKeysPresent(t *testing.T) {
	h := newSearchHarness()
	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/1/search?q=search&limit=10",
		map[string]string{"libraryId": "1"}, nil, "1", "", h.handleLibrarySearch)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"book", "podcast", "narrators", "tags", "genres", "series", "authors"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing bucket key %q in response: %v", key, got)
		}
	}
}

// TestLibrarySearch_BookEntryHasLibraryItem asserts each "book" bucket entry
// is `{ libraryItem: <expanded LibraryItem> }`, matching real ABS
// (itemMatches.push({ libraryItem: libraryItem.toOldJSONExpanded() })).
func TestLibrarySearch_BookEntryHasLibraryItem(t *testing.T) {
	h := newSearchHarness()
	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/1/search?q=search",
		map[string]string{"libraryId": "1"}, nil, "1", "", h.handleLibrarySearch)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	books, ok := got["book"].([]any)
	if !ok || len(books) != 1 {
		t.Fatalf("book bucket = %v, want 1 entry", got["book"])
	}
	entry, ok := books[0].(map[string]any)
	if !ok {
		t.Fatalf("book entry not an object: %v", books[0])
	}
	li, ok := entry["libraryItem"].(map[string]any)
	if !ok {
		t.Fatalf("book entry missing libraryItem sub-object: %v", entry)
	}
	if li["id"] != "book-1" {
		t.Errorf("libraryItem.id = %v, want book-1", li["id"])
	}
	if _, hasMatchKey := entry["matchKey"]; hasMatchKey {
		t.Errorf("book entry has matchKey, real ABS does not emit it here: %v", entry)
	}
}

// TestLibrarySearch_EmptyQuery_ReturnsEmptyBuckets covers the q="" short
// circuit — real ABS 400s on a missing q, but Silo has historically
// returned the empty-bucket envelope for an empty query; keep that
// behavior and just assert the keys survive.
func TestLibrarySearch_EmptyQuery_ReturnsEmptyBuckets(t *testing.T) {
	h := newSearchHarness()
	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/1/search?q=",
		map[string]string{"libraryId": "1"}, nil, "1", "", h.handleLibrarySearch)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"book", "podcast", "narrators", "tags", "genres", "series", "authors"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing bucket key %q on empty query: %v", key, got)
		}
	}
}

// TestLibrarySearch_AuthorsAndSeriesMatched asserts a query matching the
// stubbed author/series name populates those buckets with real-ABS-shaped
// entries (series wrapped as { series, books }).
func TestLibrarySearch_AuthorsAndSeriesMatched(t *testing.T) {
	h := newSearchHarness()
	rec := dispatchABSWithParams(http.MethodGet, "/api/libraries/1/search?q=search",
		map[string]string{"libraryId": "1"}, nil, "1", "", h.handleLibrarySearch)

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	authors, _ := got["authors"].([]any)
	if len(authors) != 1 {
		t.Fatalf("authors bucket = %v, want 1 entry", got["authors"])
	}
	series, _ := got["series"].([]any)
	if len(series) != 1 {
		t.Fatalf("series bucket = %v, want 1 entry", got["series"])
	}
	seriesEntry, ok := series[0].(map[string]any)
	if !ok {
		t.Fatalf("series entry not an object: %v", series[0])
	}
	if _, ok := seriesEntry["series"]; !ok {
		t.Errorf("series entry missing nested 'series' key: %v", seriesEntry)
	}
	if _, ok := seriesEntry["books"]; !ok {
		t.Errorf("series entry missing 'books' key: %v", seriesEntry)
	}
}

package abs

import (
	"context"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// filterDataCountingStore counts every aggregate the filter sheet issues.
type filterDataCountingStore struct {
	noopMediaStore
	authors, series, genres, narrators, publishers, languages atomic.Int32
}

func (s *filterDataCountingStore) ListLibraryAuthors(context.Context, AudiobookLibrary, int, int, string, bool, catalog.AccessFilter) ([]AuthorSummary, int, error) {
	s.authors.Add(1)
	return []AuthorSummary{{ID: "a1", Name: "Author One"}}, 1, nil
}

func (s *filterDataCountingStore) ListLibrarySeries(context.Context, AudiobookLibrary, int, int, catalog.AccessFilter) ([]SeriesSummary, int, error) {
	s.series.Add(1)
	return []SeriesSummary{{ID: "s1", Name: "Series One"}}, 1, nil
}

func (s *filterDataCountingStore) ListLibraryGenres(context.Context, AudiobookLibrary, catalog.AccessFilter) ([]string, error) {
	s.genres.Add(1)
	return []string{"Fantasy"}, nil
}

func (s *filterDataCountingStore) ListLibraryNarrators(context.Context, AudiobookLibrary, catalog.AccessFilter) ([]string, error) {
	s.narrators.Add(1)
	return []string{"Narrator One"}, nil
}

func (s *filterDataCountingStore) ListLibraryPublishers(context.Context, AudiobookLibrary, catalog.AccessFilter) ([]string, error) {
	s.publishers.Add(1)
	return []string{"Publisher One"}, nil
}

func (s *filterDataCountingStore) ListLibraryLanguages(context.Context, AudiobookLibrary, catalog.AccessFilter) ([]string, error) {
	s.languages.Add(1)
	return []string{"en"}, nil
}

func (s *filterDataCountingStore) total() int32 {
	return s.authors.Load() + s.series.Load() + s.genres.Load() +
		s.narrators.Load() + s.publishers.Load() + s.languages.Load()
}

// TestBuildFilterDataAggregatesEveryKind pins the payload the ABS filter sheet
// decodes: each kind is present and populated from its own aggregate.
func TestBuildFilterDataAggregatesEveryKind(t *testing.T) {
	store := &filterDataCountingStore{}
	h := New(Dependencies{MediaStore: store})
	lib := AudiobookLibrary{ID: 7, Name: "Audiobooks", Type: "audiobooks"}

	req := httptest.NewRequest("GET", "/api/libraries/7?include=filterdata", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "42"}))
	got := h.buildFilterData(req, lib)

	if !reflect.DeepEqual(got[authorsKey], []AuthorObj{{ID: "a1", Name: "Author One"}}) {
		t.Errorf("authors = %#v", got[authorsKey])
	}
	if !reflect.DeepEqual(got[seriesWireKey], []SeriesObj{{ID: "s1", Name: "Series One"}}) {
		t.Errorf("series = %#v", got[seriesWireKey])
	}
	for key, want := range map[string][]string{
		genresKey:    {"Fantasy"},
		narratorsKey: {"Narrator One"},
		"publishers": {"Publisher One"},
		"languages":  {"en"},
	} {
		if !reflect.DeepEqual(got[key], want) {
			t.Errorf("%s = %#v, want %#v", key, got[key], want)
		}
	}
	if got[tagsKey] == nil {
		t.Error("tags must be present as an empty array, not absent")
	}
	if n := store.total(); n != 6 {
		t.Errorf("aggregate queries = %d, want 6 (one per kind)", n)
	}
}

// TestBuildFilterDataMemoizesPerLibraryAndAccess pins the memo: repeating the
// same library open must not re-run six full-library aggregates, while a
// different library or a different viewer's access filter must.
func TestBuildFilterDataMemoizesPerLibraryAndAccess(t *testing.T) {
	store := &filterDataCountingStore{}
	h := New(Dependencies{MediaStore: store})
	lib := AudiobookLibrary{ID: 7, Name: "Audiobooks", Type: "audiobooks"}

	build := func(userID string, lib AudiobookLibrary) map[string]any {
		req := httptest.NewRequest("GET", "/api/libraries/7?include=filterdata", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: userID}))
		return h.buildFilterData(req, lib)
	}

	first := build("42", lib)
	second := build("42", lib)
	if store.total() != 6 {
		t.Fatalf("aggregate queries after a repeat open = %d, want 6 (cache hit)", store.total())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached payload differs from the freshly built one\nfirst:  %#v\nsecond: %#v", first, second)
	}

	// A different principal resolves to a different access filter, which must
	// not read another viewer's memoized sheet.
	build("43", lib)
	if store.total() != 12 {
		t.Fatalf("aggregate queries after a second viewer = %d, want 12", store.total())
	}

	build("42", AudiobookLibrary{ID: 8, Name: "More Audiobooks", Type: "audiobooks"})
	if store.total() != 18 {
		t.Fatalf("aggregate queries after a second library = %d, want 18", store.total())
	}
}

// TestBuildFilterDataSkipsNarratorsForEbookLibraries keeps the concurrent
// rewrite honest about the one conditional aggregate.
func TestBuildFilterDataSkipsNarratorsForEbookLibraries(t *testing.T) {
	store := &filterDataCountingStore{}
	h := New(Dependencies{MediaStore: store})

	req := httptest.NewRequest("GET", "/api/libraries/9?include=filterdata", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, ctxAuth{UserID: "42"}))
	got := h.buildFilterData(req, AudiobookLibrary{ID: 9, Name: "Ebooks", Type: libraryTypeEbooks})

	if store.narrators.Load() != 0 {
		t.Errorf("ebook library queried narrators %d times, want 0", store.narrators.Load())
	}
	if narrators, _ := got[narratorsKey].([]string); len(narrators) != 0 {
		t.Errorf("narrators = %#v, want empty", got[narratorsKey])
	}
}

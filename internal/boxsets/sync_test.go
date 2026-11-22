package boxsets

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

type fakeStore struct {
	due         []catalog.MovieCollectionLookup
	recorded    map[string]*int64
	collections map[int64]catalog.TMDBMovieCollection
	touched     []int64
	// language is the language every collection is due in.
	language string
}

func (s *fakeStore) MoviesNeedingCollectionLookup(_ context.Context, _ time.Time, limit int, skip []string) ([]catalog.MovieCollectionLookup, error) {
	var out []catalog.MovieCollectionLookup
	for _, m := range s.due {
		if _, done := s.recorded[m.ContentID]; !done && !slices.Contains(skip, m.ContentID) && len(out) < limit {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *fakeStore) RecordMovieCollection(_ context.Context, contentID string, _ int64, collectionID *int64) error {
	s.recorded[contentID] = collectionID
	return nil
}

func (s *fakeStore) CollectionsNeedingFetch(_ context.Context, _ time.Time, _ int, skip []int64) ([]catalog.CollectionFetch, error) {
	var ids []int64
	for _, id := range s.recorded {
		if id == nil || slices.Contains(ids, *id) || slices.Contains(s.touched, *id) || slices.Contains(skip, *id) {
			continue
		}
		if _, stored := s.collections[*id]; !stored {
			ids = append(ids, *id)
		}
	}
	slices.Sort(ids)
	out := make([]catalog.CollectionFetch, len(ids))
	for i, id := range ids {
		out[i] = catalog.CollectionFetch{ID: id, Language: s.language}
	}
	return out, nil
}

func (s *fakeStore) UpsertCollection(_ context.Context, c catalog.TMDBMovieCollection) error {
	s.collections[c.ID] = c
	return nil
}

func (s *fakeStore) CollectionArtworkNeedingCache(_ context.Context, _ int, skip []int64) ([]catalog.CollectionArtworkDue, error) {
	var due []catalog.CollectionArtworkDue
	for _, id := range slices.Sorted(maps.Keys(s.collections)) {
		if slices.Contains(skip, id) {
			continue
		}
		c := s.collections[id]
		if c.PosterSourcePath != "" && c.PosterPath == "" {
			due = append(due, catalog.CollectionArtworkDue{CollectionID: id, Slot: catalog.BoxSetPosterSlot, SourcePath: c.PosterSourcePath})
		}
		if c.BackdropSourcePath != "" && c.BackdropPath == "" {
			due = append(due, catalog.CollectionArtworkDue{CollectionID: id, Slot: catalog.BoxSetBackdropSlot, SourcePath: c.BackdropSourcePath})
		}
	}
	return due, nil
}

func (s *fakeStore) SetCollectionArtwork(_ context.Context, id int64, slot catalog.CollectionArtworkSlot, _, path, thumbhash string) error {
	c := s.collections[id]
	if slot == catalog.BoxSetPosterSlot {
		c.PosterPath, c.PosterThumbhash = path, thumbhash
	} else {
		c.BackdropPath, c.BackdropThumbhash = path, thumbhash
	}
	s.collections[id] = c
	return nil
}

func (s *fakeStore) DeleteUnusedCollections(context.Context) (int64, error) { return 0, nil }

// TouchCollection marks only a stored collection fetched, as the
// repository's UPDATE does.
func (s *fakeStore) TouchCollection(_ context.Context, id int64, _ string) error {
	if _, stored := s.collections[id]; stored {
		s.touched = append(s.touched, id)
	}
	return nil
}

type fakeTMDB struct {
	movies      map[int]*tmdb.CollectionRef
	collections map[int]*tmdb.Collection
	movieErr    error
	// failing movies answer with an error; lookups counts every request.
	failing map[int]bool
	lookups map[int]int
	// onCollection runs on each collection fetch.
	onCollection func()
	// translated answers collection requests in a language other than
	// English; collections answers the others.
	translated map[int]*tmdb.Collection
	// languages records the languages each collection was asked for in.
	languages map[int][]string
	// englishErr answers collection requests in English.
	englishErr error
}

func (f *fakeTMDB) GetMovieCollection(_ context.Context, id int) (*tmdb.CollectionRef, error) {
	if f.lookups != nil {
		f.lookups[id]++
	}
	if f.movieErr != nil || f.failing[id] {
		return nil, cmp.Or(f.movieErr, errors.New("tmdb error"))
	}
	ref, ok := f.movies[id]
	if !ok {
		return nil, tmdb.ErrNotFound
	}
	return ref, nil
}

func (f *fakeTMDB) GetCollection(_ context.Context, id int, language string) (*tmdb.Collection, error) {
	if f.languages != nil {
		f.languages[id] = append(f.languages[id], language)
	}
	if f.onCollection != nil {
		f.onCollection()
	}
	english := language == "" || language == "en"
	if english && f.englishErr != nil {
		return nil, f.englishErr
	}
	c, ok := f.collections[id]
	if t, translated := f.translated[id]; translated && !english {
		c, ok = t, true
	}
	if !ok {
		return nil, tmdb.ErrNotFound
	}
	return c, nil
}

func newFakeStore(due ...catalog.MovieCollectionLookup) *fakeStore {
	return &fakeStore{due: due, recorded: map[string]*int64{}, collections: map[int64]catalog.TMDBMovieCollection{}}
}

func noReport(float64, string) {}

func TestRunStoresMembershipAndCollectionsInReleaseOrder(t *testing.T) {
	store := newFakeStore(
		catalog.MovieCollectionLookup{ContentID: "hp1", TMDBID: 671},
		catalog.MovieCollectionLookup{ContentID: "heat", TMDBID: 949},
		catalog.MovieCollectionLookup{ContentID: "gone", TMDBID: 1},
	)
	store.language = "de"
	client := &fakeTMDB{
		languages: map[int][]string{},
		// TMDB leaves the fields a translation lacks empty.
		translated: map[int]*tmdb.Collection{1241: {ID: 1241, Name: "Harry Potter Filmreihe", PosterPath: "/de.jpg", Parts: []tmdb.CollectionPart{
			{ID: 674, ReleaseDate: "2005-11-16"},
			{ID: 999, ReleaseDate: ""},
			{ID: 671, ReleaseDate: "2001-11-16"},
			{ID: 673, ReleaseDate: "2004-05-31"},
		}}},
		movies: map[int]*tmdb.CollectionRef{671: {ID: 1241, Name: "Harry Potter Collection"}, 949: nil},
		collections: map[int]*tmdb.Collection{1241: {ID: 1241, Name: "Harry Potter Collection", Overview: "The wizarding saga.", PosterPath: "/p.jpg", Parts: []tmdb.CollectionPart{
			{ID: 674, ReleaseDate: "2005-11-16"},
			{ID: 999, ReleaseDate: ""},
			{ID: 671, ReleaseDate: "2001-11-16"},
			{ID: 673, ReleaseDate: "2004-05-31"},
		}}},
	}
	res, err := NewSyncer(store, client).Run(t.Context(), noReport)
	if err != nil {
		t.Fatal(err)
	}
	if res.MoviesChecked != 3 || res.CollectionsFetched != 1 || res.Failures != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := store.recorded["hp1"]; got == nil || *got != 1241 {
		t.Fatalf("hp1 collection = %v, want 1241", got)
	}
	// A movie in no collection, and one TMDB no longer has, are recorded as
	// in none, so they are not looked up again on every run.
	for _, id := range []string{"heat", "gone"} {
		if got, ok := store.recorded[id]; !ok || got != nil {
			t.Fatalf("%s recorded = %v, %v; want in no collection", id, got, ok)
		}
	}
	stored := store.collections[1241]
	// The collection is stored in the language it is due in, and the
	// overview the translation lacks comes from the fallback language,
	// English by default.
	if !slices.Equal(client.languages[1241], []string{"de", "en"}) || stored.Language != "de" {
		t.Fatalf("fetched in %q, stored as %q; want de, then the English fallback", client.languages[1241], stored.Language)
	}
	if stored.Name != "Harry Potter Filmreihe" || stored.Overview != "The wizarding saga." {
		t.Fatalf("name, overview = %q, %q; want the German name and the English overview", stored.Name, stored.Overview)
	}
	if want := []int64{671, 673, 674, 999}; !slices.Equal(stored.MemberTMDBIDs, want) {
		t.Fatalf("members = %v, want release order %v with the undated part last", stored.MemberTMDBIDs, want)
	}
	if stored.PosterSourcePath != "https://image.tmdb.org/t/p/original/de.jpg" || stored.BackdropSourcePath != "" {
		t.Fatalf("artwork sources = %q, %q", stored.PosterSourcePath, stored.BackdropSourcePath)
	}
}

// A failed fallback answer fails the fetch rather than storing the
// translation without its overview, so the collection is tried again.
func TestRunRetriesWhenTheFallbackFails(t *testing.T) {
	store := newFakeStore(catalog.MovieCollectionLookup{ContentID: "hp1", TMDBID: 671})
	store.language = "de"
	client := &fakeTMDB{
		movies:     map[int]*tmdb.CollectionRef{671: {ID: 1241}},
		translated: map[int]*tmdb.Collection{1241: {ID: 1241, Name: "Harry Potter Filmreihe", Parts: []tmdb.CollectionPart{{ID: 671}}}},
		englishErr: errors.New("tmdb timeout"),
	}
	res, err := NewSyncer(store, client).Run(t.Context(), noReport)
	if err != nil {
		t.Fatal(err)
	}
	if _, stored := store.collections[1241]; stored || res.Failures != 1 {
		t.Fatalf("stored = %v, failures = %d; want nothing stored and one failure", stored, res.Failures)
	}
}

// With the fallback language set to the original language, the text a
// translation lacks stays empty, as it does for item metadata.
func TestRunFillsNothingWhenTheFallbackIsOff(t *testing.T) {
	store := newFakeStore(catalog.MovieCollectionLookup{ContentID: "hp1", TMDBID: 671})
	store.language = "de"
	client := &fakeTMDB{
		languages:   map[int][]string{},
		movies:      map[int]*tmdb.CollectionRef{671: {ID: 1241}},
		translated:  map[int]*tmdb.Collection{1241: {ID: 1241, Name: "Harry Potter Filmreihe", Parts: []tmdb.CollectionPart{{ID: 671}}}},
		collections: map[int]*tmdb.Collection{1241: {ID: 1241, Name: "Harry Potter Collection", Overview: "The wizarding saga.", Parts: []tmdb.CollectionPart{{ID: 671}}}},
	}
	if _, err := NewSyncer(store, client).WithFallbackLanguage(func() string { return "" }).Run(t.Context(), noReport); err != nil {
		t.Fatal(err)
	}
	if stored := store.collections[1241]; !slices.Equal(client.languages[1241], []string{"de"}) || stored.Overview != "" {
		t.Fatalf("asked in %q, stored overview %q; want only de and no overview", client.languages[1241], stored.Overview)
	}
}

type fakeCacher struct {
	requests []metadata.CacheImageRequest
	fail     bool
}

func (c *fakeCacher) CacheImage(_ context.Context, req metadata.CacheImageRequest) (*metadata.CacheImageResult, error) {
	c.requests = append(c.requests, req)
	if c.fail {
		return nil, errors.New("store down")
	}
	base := "tmdb/collections/" + req.ContentID + "/" + map[metadata.ImageType]string{metadata.ImagePoster: "poster", metadata.ImageBackdrop: "backdrop"}[req.ImageType]
	return &metadata.CacheImageResult{BasePath: base, Revision: "abc", Ext: ".webp", Thumbhash: "th-" + req.ContentID}, nil
}

package boxsets

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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

func (s *fakeStore) CollectionsNeedingFetch(_ context.Context, _ time.Time, _ int, skip []int64) ([]int64, error) {
	var out []int64
	for _, id := range s.recorded {
		if id == nil || slices.Contains(out, *id) || slices.Contains(s.touched, *id) || slices.Contains(skip, *id) {
			continue
		}
		if _, stored := s.collections[*id]; !stored {
			out = append(out, *id)
		}
	}
	slices.Sort(out)
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

// TouchCollection marks only a stored collection fetched, as the
// repository's UPDATE does.
func (s *fakeStore) TouchCollection(_ context.Context, id int64) error {
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

func (f *fakeTMDB) GetCollection(_ context.Context, id int) (*tmdb.Collection, error) {
	c, ok := f.collections[id]
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
	client := &fakeTMDB{
		movies: map[int]*tmdb.CollectionRef{671: {ID: 1241, Name: "Harry Potter Collection"}, 949: nil},
		collections: map[int]*tmdb.Collection{1241: {ID: 1241, Name: "Harry Potter Collection", PosterPath: "/p.jpg", Parts: []tmdb.CollectionPart{
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
	if want := []int64{671, 673, 674, 999}; !slices.Equal(stored.MemberTMDBIDs, want) {
		t.Fatalf("members = %v, want release order %v with the undated part last", stored.MemberTMDBIDs, want)
	}
	if stored.PosterSourcePath != "https://image.tmdb.org/t/p/original/p.jpg" || stored.BackdropSourcePath != "" {
		t.Fatalf("artwork sources = %q, %q", stored.PosterSourcePath, stored.BackdropSourcePath)
	}
}

func TestRunStopsAfterRepeatedFailures(t *testing.T) {
	var due []catalog.MovieCollectionLookup
	for i := range maxFailures + 10 {
		due = append(due, catalog.MovieCollectionLookup{ContentID: string(rune('a' + i)), TMDBID: int64(i + 1)})
	}
	store := newFakeStore(due...)
	res, err := NewSyncer(store, &fakeTMDB{movieErr: errors.New("tmdb down")}).Run(t.Context(), noReport)
	if err == nil {
		t.Fatal("want an error once TMDB keeps failing")
	}
	if res.Failures != maxFailures || len(store.recorded) != 0 {
		t.Fatalf("result = %+v, recorded = %d; want %d failures and nothing recorded", res, len(store.recorded), maxFailures)
	}
}

// A movie whose lookup keeps failing is tried once in a run, so it costs one
// failure rather than one per batch, and the movies behind it are still
// looked up.
func TestRunTriesAFailingMovieOncePerRun(t *testing.T) {
	var due []catalog.MovieCollectionLookup
	for i := range 3*batchSize + 1 {
		due = append(due, catalog.MovieCollectionLookup{ContentID: fmt.Sprintf("m%04d", i), TMDBID: int64(i + 1)})
	}
	store := newFakeStore(due...)
	client := &fakeTMDB{movies: map[int]*tmdb.CollectionRef{}, failing: map[int]bool{1: true}, lookups: map[int]int{}}
	res, err := NewSyncer(store, client).Run(t.Context(), noReport)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failures != 1 || client.lookups[1] != 1 || res.MoviesChecked != len(due)-1 {
		t.Fatalf("result = %+v, lookups of the failing movie = %d", res, client.lookups[1])
	}
}

// A collection TMDB does not serve must not keep the run fetching it: one
// never stored stays due for a later run, and this run still ends.
func TestRunEndsWhenACollectionIsMissing(t *testing.T) {
	store := newFakeStore(catalog.MovieCollectionLookup{ContentID: "m", TMDBID: 5})
	client := &fakeTMDB{movies: map[int]*tmdb.CollectionRef{5: {ID: 77}}, collections: map[int]*tmdb.Collection{}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	res, err := NewSyncer(store, client).Run(ctx, noReport)
	if err != nil {
		t.Fatal(err)
	}
	if res.CollectionsFetched != 0 || len(store.collections) != 0 {
		t.Fatalf("result = %+v, stored = %v", res, store.collections)
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

func TestRunCachesCollectionArtwork(t *testing.T) {
	store := newFakeStore()
	store.collections[1241] = catalog.TMDBMovieCollection{ID: 1241, PosterSourcePath: "https://image.tmdb.org/t/p/original/p.jpg", BackdropSourcePath: "https://image.tmdb.org/t/p/original/b.jpg"}
	cacher := &fakeCacher{}
	// The collection is stored and current, so only the artwork phase runs.
	store.recorded["m"] = new(int64(1241))
	res, err := NewSyncer(store, &fakeTMDB{}).WithImages(func() metadata.ImageCacher { return cacher }).Run(t.Context(), noReport)
	if err != nil {
		t.Fatal(err)
	}
	if res.ImagesCached != 2 || len(cacher.requests) != 2 {
		t.Fatalf("result = %+v, requests = %+v", res, cacher.requests)
	}
	for _, req := range cacher.requests {
		if req.ProviderID != "tmdb" || req.ContentType != "collections" || req.ContentID != "1241" {
			t.Fatalf("request = %+v", req)
		}
	}
	c := store.collections[1241]
	if c.PosterPath != metadata.CachedImageOriginalPath(&metadata.CacheImageResult{BasePath: "tmdb/collections/1241/poster", Revision: "abc", Ext: ".webp"}) || c.PosterThumbhash != "th-1241" || c.BackdropPath == "" {
		t.Fatalf("stored = %+v", c)
	}
}

func TestRunLeavesArtworkOnTheProviderWhenCachingIsOff(t *testing.T) {
	store := newFakeStore()
	store.collections[1241] = catalog.TMDBMovieCollection{ID: 1241, PosterSourcePath: "https://image.tmdb.org/t/p/original/p.jpg"}
	store.recorded["m"] = new(int64(1241))
	res, err := NewSyncer(store, &fakeTMDB{}).WithImages(func() metadata.ImageCacher { return nil }).Run(t.Context(), noReport)
	if err != nil || res.ImagesCached != 0 || store.collections[1241].PosterPath != "" {
		t.Fatalf("res = %+v, err = %v, stored = %+v", res, err, store.collections[1241])
	}
}

func TestRunEndsWhenArtworkCachingFails(t *testing.T) {
	store := newFakeStore()
	store.collections[1241] = catalog.TMDBMovieCollection{ID: 1241, PosterSourcePath: "https://image.tmdb.org/t/p/original/p.jpg"}
	store.recorded["m"] = new(int64(1241))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	res, err := NewSyncer(store, &fakeTMDB{}).WithImages(func() metadata.ImageCacher { return &fakeCacher{fail: true} }).Run(ctx, noReport)
	if err != nil || res.Failures != 1 || res.ImagesCached != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

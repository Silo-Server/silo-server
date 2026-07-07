package sections

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

type fakeSectionLister struct {
	configs []json.RawMessage
	err     error
}

func (f fakeSectionLister) ListTrendingDiscoverConfigs(context.Context) ([]json.RawMessage, error) {
	return f.configs, f.err
}

type savedSnap struct {
	contentIDs []string
	entryCount int
	status     string
}

type attemptRec struct {
	status  string
	message string
}

type fakeSnapshotStore struct {
	saved    map[string]savedSnap
	attempts map[string]attemptRec
}

func newFakeSnapshotStore() *fakeSnapshotStore {
	return &fakeSnapshotStore{saved: map[string]savedSnap{}, attempts: map[string]attemptRec{}}
}

func (f *fakeSnapshotStore) SaveSuccess(_ context.Context, source, window string, contentIDs []string, entryCount int, status string, _ time.Time) error {
	f.saved[source+"|"+window] = savedSnap{contentIDs: contentIDs, entryCount: entryCount, status: status}
	return nil
}

func (f *fakeSnapshotStore) RecordAttempt(_ context.Context, source, window, status, message string, _ time.Time) error {
	f.attempts[source+"|"+window] = attemptRec{status: status, message: message}
	return nil
}

type fakeTMDB struct {
	entries []catalog.TMDBCollectionEntry
	err     error
}

func (f fakeTMDB) GetCollectionPreset(context.Context, string, string, string, int) ([]catalog.TMDBCollectionEntry, error) {
	return f.entries, f.err
}

type fakeTrakt struct {
	byMediaType map[string][]catalog.TraktCollectionEntry
	errByType   map[string]error
}

func (f fakeTrakt) GetUserList(context.Context, string, string, int, string) ([]catalog.TraktCollectionEntry, error) {
	return nil, nil
}

func (f fakeTrakt) GetCollectionPreset(_ context.Context, _, mediaType string, _ int, _ string) ([]catalog.TraktCollectionEntry, error) {
	if err := f.errByType[mediaType]; err != nil {
		return nil, err
	}
	return f.byMediaType[mediaType], nil
}

type fakeResolver struct {
	byType map[string]*catalog.ExternalIDLookup
}

func (f fakeResolver) GetByExternalIDs(_ context.Context, _ catalog.ExternalIDBatch, itemType string) (*catalog.ExternalIDLookup, error) {
	if lk, ok := f.byType[itemType]; ok {
		return lk, nil
	}
	return &catalog.ExternalIDLookup{ByTMDB: map[string]string{}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}}, nil
}

func tmdbConfig(t *testing.T, source, window string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(recipes.TrendingDiscoverParams{Source: source, Window: window})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

func TestRefresherSavesOrderedContentIDs(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots: store,
		Resolver: fakeResolver{byType: map[string]*catalog.ExternalIDLookup{
			"movie":  {ByTMDB: map[string]string{"10": "c-movie"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
			"series": {ByTMDB: map[string]string{"20": "c-series"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
		}},
		TMDBTrending: fakeTMDB{entries: []catalog.TMDBCollectionEntry{
			{ID: 10, MediaType: "movie"},
			{ID: 20, MediaType: "tv"},
		}},
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	data, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var result TrendingRefreshResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Combos != 1 || result.Refreshed != 1 || result.Failed != 0 || result.Empty != 0 {
		t.Fatalf("result = %+v; want {Combos:1 Refreshed:1 Empty:0 Failed:0}", result)
	}

	got := store.saved["tmdb|week"]
	want := []string{"c-movie", "c-series"}
	if len(got.contentIDs) != len(want) || got.contentIDs[0] != want[0] || got.contentIDs[1] != want[1] {
		t.Fatalf("saved content IDs = %v; want %v", got.contentIDs, want)
	}
	if got.status != "ok" || got.entryCount != 2 {
		t.Fatalf("saved snap = %+v; want status ok, entryCount 2", got)
	}
}

func TestRefresherFailurePreservesLastGood(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:     fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots:    store,
		Resolver:     fakeResolver{},
		TMDBTrending: fakeTMDB{err: errors.New("tmdb 503")},
		Clock:        recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	data, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if _, ok := store.saved["tmdb|week"]; ok {
		t.Fatal("SaveSuccess must not be called on fetch failure (would clear last-good)")
	}
	att, ok := store.attempts["tmdb|week"]
	if !ok || att.status != "error" {
		t.Fatalf("attempt = %+v, ok=%v; want status error", att, ok)
	}

	var result TrendingRefreshResult
	_ = json.Unmarshal(data, &result)
	if result.Failed != 1 {
		t.Fatalf("result.Failed = %d; want 1", result.Failed)
	}
}

func TestRefresherEmptyProviderPreservesLastGood(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots: store,
		Resolver:  fakeResolver{},
		// TMDBTrending nil => provider unconfigured => empty entries, no error.
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	data, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, ok := store.saved["tmdb|week"]; ok {
		t.Fatal("SaveSuccess must not be called when provider returns no entries")
	}
	att := store.attempts["tmdb|week"]
	if att.status != "empty" {
		t.Fatalf("attempt status = %q; want empty", att.status)
	}
	var result TrendingRefreshResult
	_ = json.Unmarshal(data, &result)
	if result.Empty != 1 {
		t.Fatalf("result.Empty = %d; want 1", result.Empty)
	}
}

func TestRefresherSkipsPersonEntries(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots: store,
		Resolver: fakeResolver{byType: map[string]*catalog.ExternalIDLookup{
			// "99" is present in the movie lookup to simulate a person ID that
			// collides with an unrelated library movie's TMDB ID.
			"movie":  {ByTMDB: map[string]string{"10": "c-movie", "99": "c-person-collision"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
			"series": {ByTMDB: map[string]string{"20": "c-series"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
		}},
		TMDBTrending: fakeTMDB{entries: []catalog.TMDBCollectionEntry{
			{ID: 10, MediaType: "movie"},
			{ID: 99, MediaType: "person"},
			{ID: 20, MediaType: "tv"},
		}},
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got := store.saved["tmdb|week"].contentIDs
	want := []string{"c-movie", "c-series"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("content IDs = %v; want %v (person entry must be skipped)", got, want)
	}
}

func TestRefresherTraktInterleavesMoviesAndShows(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "trakt", "week")}},
		Snapshots: store,
		Resolver: fakeResolver{byType: map[string]*catalog.ExternalIDLookup{
			"movie":  {ByTMDB: map[string]string{"1": "m1", "2": "m2"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
			"series": {ByTMDB: map[string]string{"3": "s1"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
		}},
		TraktTrending: fakeTrakt{byMediaType: map[string][]catalog.TraktCollectionEntry{
			"movie": {{TMDBID: 1, MediaType: "movie"}, {TMDBID: 2, MediaType: "movie"}},
			"tv":    {{TMDBID: 3, MediaType: "tv"}},
		}},
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Interleaved order: movie[0], show[0], movie[1] => m1, s1, m2. A plain
	// concat would have buried s1 after all movies.
	got := store.saved["trakt|week"].contentIDs
	want := []string{"m1", "s1", "m2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("content IDs = %v; want %v (interleaved)", got, want)
	}
}

func TestRefresherTraktPartialFailurePreservesLastGood(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "trakt", "week")}},
		Snapshots: store,
		Resolver:  fakeResolver{},
		TraktTrending: fakeTrakt{
			byMediaType: map[string][]catalog.TraktCollectionEntry{"movie": {{TMDBID: 1, MediaType: "movie"}}},
			errByType:   map[string]error{"tv": errors.New("trakt shows 500")},
		},
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if _, ok := store.saved["trakt|week"]; ok {
		t.Fatal("SaveSuccess must not run when one Trakt sub-fetch fails (would drop a media type)")
	}
	if store.attempts["trakt|week"].status != "error" {
		t.Fatalf("attempt status = %q; want error", store.attempts["trakt|week"].status)
	}
}

func TestDistinctTrendingCombosCollapsesTrakt(t *testing.T) {
	configs := []json.RawMessage{
		tmdbConfig(t, "trakt", "day"),
		tmdbConfig(t, "trakt", "week"),
		tmdbConfig(t, "tmdb", "day"),
		tmdbConfig(t, "tmdb", "day"),
	}
	got := distinctTrendingCombos(configs)
	if len(got) != 2 {
		t.Fatalf("distinctTrendingCombos len = %d (%+v); want 2", len(got), got)
	}
	seen := map[trendingCombo]bool{}
	for _, c := range got {
		seen[c] = true
	}
	if !seen[trendingCombo{"trakt", "week"}] || !seen[trendingCombo{"tmdb", "day"}] {
		t.Fatalf("combos = %+v; want {trakt week} and {tmdb day}", got)
	}
}

package recommendations

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

const externalTestModel = "ext-test-model"

// fakeExternalTMDB serves details and discover pages from maps and records
// the calls.
type fakeExternalTMDB struct {
	mu       sync.Mutex
	details  map[ExternalTitleKey]*tmdb.MediaDetail
	discover map[string][]int // discover media type → IDs, most-voted first
	calls    map[ExternalTitleKey]int
	pages    int
}

func (f *fakeExternalTMDB) GetMediaDetailInLanguage(_ context.Context, mediaType string, id int, _ string) (*tmdb.MediaDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := ExternalTitleKey{MediaType: mediaType, TMDBID: id}
	f.calls[k]++
	d, ok := f.details[k]
	if !ok {
		return nil, fmt.Errorf("tmdb get %d: %w", id, tmdb.ErrNotFound)
	}
	copied := *d
	return &copied, nil
}

func (f *fakeExternalTMDB) DiscoverPage(_ context.Context, mediaType string, _ tmdb.DiscoverParams, page int) (*tmdb.MediaPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages++
	ids := f.discover[mediaType]
	out := &tmdb.MediaPage{Page: page, TotalPages: max(1, (len(ids)+19)/20)}
	for _, id := range ids[min((page-1)*20, len(ids)):min(page*20, len(ids))] {
		out.Results = append(out.Results, tmdb.MediaResult{ID: id})
	}
	return out, nil
}

func (f *fakeExternalTMDB) detailCalls(keys ...ExternalTitleKey) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range keys {
		n += f.calls[k]
	}
	return n
}

// fakeOwnedLookup owns titles by TMDB key, IMDb ID or TVDB ID.
type fakeOwnedLookup struct {
	tmdb map[ExternalTitleKey]bool
	imdb map[string]bool
	tvdb map[string]bool
}

func (f *fakeOwnedLookup) LookupExternalIDs(_ context.Context, mediaType string, candidates []catalog.ExternalIDLookupCandidate) ([]catalog.ExternalIDMatchRow, error) {
	var rows []catalog.ExternalIDMatchRow
	for _, c := range candidates {
		id, _ := strconv.Atoi(c.TMDBID)
		if f.tmdb[ExternalTitleKey{MediaType: mediaType, TMDBID: id}] || (c.IMDbID != "" && f.imdb[c.IMDbID]) || (c.TVDBID != "" && f.tvdb[c.TVDBID]) {
			rows = append(rows, catalog.ExternalIDMatchRow{QueryTMDBID: c.TMDBID, MediaID: "owned"})
		}
	}
	return rows, nil
}

// externalPoolScenario is two profiles sharing seed A: A's lists name four
// movies, one owned; seed B is unknown to TMDB; series seed C names one
// series; the recent sets hold two movies and a series.
type externalPoolScenario struct {
	t        *testing.T
	db       *pgxpool.Pool
	engine   *Engine
	tmdb     *fakeExternalTMDB
	embedded *int
	owned    *fakeOwnedLookup
	profiles []ExternalProfile
	base     int
	seeds    []ExternalTitleKey
	// pool lists the outside titles the server should keep.
	pool []ExternalTitleKey
}

func newExternalPoolScenario(t *testing.T, base int) *externalPoolScenario {
	t.Helper()
	f := newExternalFixture(t, base)
	pool := f.pool
	snapshotEmbeddingLock(t, pool)
	f.exec(`DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey)
	clearSets := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM external_title_sources WHERE source_key LIKE 'set:%' OR source_key LIKE 'seed:%:' || $1 || '%'`, strconv.Itoa(base/1000))
	}
	clearSets()
	t.Cleanup(clearSets)

	movie := func(n int) ExternalTitleKey { return ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: base + n} }
	series := func(n int) ExternalTitleKey {
		return ExternalTitleKey{MediaType: ExternalMediaSeries, TMDBID: base + n}
	}
	results := func(keys ...ExternalTitleKey) []tmdb.MediaResult {
		out := make([]tmdb.MediaResult, len(keys))
		for i, k := range keys {
			out[i] = tmdb.MediaResult{ID: k.TMDBID}
		}
		return out
	}
	s := &externalPoolScenario{t: t, db: pool, base: base, seeds: []ExternalTitleKey{movie(1), movie(2), series(3)}}
	fake := &fakeExternalTMDB{details: map[ExternalTitleKey]*tmdb.MediaDetail{}, calls: map[ExternalTitleKey]int{}}
	fake.details[movie(1)] = &tmdb.MediaDetail{MediaType: "movie", Title: "A",
		Recommendations: results(movie(10), movie(11), movie(12)), Similar: results(movie(11), movie(13))}
	fake.details[series(3)] = &tmdb.MediaDetail{MediaType: "series", Title: "C", Recommendations: results(series(20))}
	fake.discover = map[string][]int{"movie": {base + 30, base + 31}, "tv": {base + 40}}
	s.pool = []ExternalTitleKey{movie(10), movie(11), movie(13), series(20), movie(30), movie(31), series(40)}
	for _, k := range s.pool {
		fake.details[k] = &tmdb.MediaDetail{MediaType: k.MediaType, Title: "title " + strconv.Itoa(k.TMDBID), Overview: "overview", Genres: []string{"Drama"}, VoteCount: 50}
	}
	s.tmdb = fake

	embedded := 0
	s.embedded = &embedded
	s.owned = &fakeOwnedLookup{tmdb: map[ExternalTitleKey]bool{movie(12): true}, imdb: map[string]bool{}, tvdb: map[string]bool{}}
	s.engine = &Engine{
		repo:          NewRepo(pool),
		embClient:     vectorsOfTexts(4, &embedded),
		cfg:           config.RecommendationsConfig{EmbeddingBaseURL: "http://embed.test", EmbeddingModel: externalTestModel},
		externalTMDB:  fake,
		externalOwned: s.owned,
	}
	seed := func(id string, k ExternalTitleKey, w float64) ExternalSeed {
		return ExternalSeed{ContentID: id, Key: k, Weight: w, OriginalLanguage: "en"}
	}
	p1 := []ExternalSeed{seed("a", movie(1), 3), seed("b", movie(2), 2)}
	p2 := []ExternalSeed{seed("a", movie(1), 1), seed("c", series(3), 1)}
	s.profiles = []ExternalProfile{
		{UserID: 1, ProfileID: "p1", Seeds: p1, SetSources: externalProfileSetSources(p1)},
		{UserID: 2, ProfileID: "p2", Seeds: p2, SetSources: externalProfileSetSources(p2)},
	}
	return s
}

// vectorsOfTexts answers every text with a dims-wide vector and counts the
// texts.
func vectorsOfTexts(dims int, texts *int) quotaTestEmbedder {
	var mu sync.Mutex
	return func(_ context.Context, in []string) ([][]float32, error) {
		mu.Lock()
		*texts += len(in)
		mu.Unlock()
		out := make([][]float32, len(in))
		for i := range in {
			out[i] = testVector(dims)
		}
		return out, nil
	}
}

func (s *externalPoolScenario) build(tmdbRequests, embeds int) ExternalPoolCounts {
	s.t.Helper()
	counts, err := s.engine.buildExternalPool(context.Background(), s.profiles, ExternalPoolOptions{MaxTitles: 5000},
		externalLimits{tmdbRequests: tmdbRequests, embeds: embeds})
	if err != nil {
		s.t.Fatalf("build: %v (counts %+v)", err, counts)
	}
	return counts
}

func (s *externalPoolScenario) lock() {
	s.t.Helper()
	if err := s.engine.repo.SetEmbeddingLock(context.Background(), EmbeddingLock{BaseURL: "http://embed.test", Model: externalTestModel, SourceDimensions: 4, StorageDimensions: CanonicalEmbeddingDimensions}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *externalPoolScenario) vectors() int {
	s.t.Helper()
	texts, err := s.engine.repo.ExternalEmbeddingTexts(context.Background(), s.pool, externalTestModel)
	if err != nil {
		s.t.Fatal(err)
	}
	return len(texts)
}

// A run fetches seed lists and sets once until they are due again, leaves out
// owned titles, stores no vector before library vectors lock the model and
// never writes the lock, and re-embeds a refetched title only when its text
// changed.
func TestBuildExternalPoolDB(t *testing.T) {
	s := newExternalPoolScenario(t, 920_000_000)
	ctx := context.Background()

	counts := s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if counts.VectorsSkipped != errNoExternalLock.Error() || counts.DetailsFetched != 0 || *s.embedded != 0 {
		t.Fatalf("without a lock: %+v, %d texts embedded", counts, *s.embedded)
	}
	if lock, err := s.engine.repo.GetEmbeddingLock(ctx); err != nil || lock != nil {
		t.Fatalf("outside run wrote the lock %+v (%v)", lock, err)
	}
	if counts.Seeds != 3 || counts.SourcesFetched != 5 || s.tmdb.detailCalls(s.seeds...) != 3 || s.tmdb.pages != 2 {
		t.Fatalf("first run: %+v, seed calls %d, pages %d", counts, s.tmdb.detailCalls(s.seeds...), s.tmdb.pages)
	}
	if counts.PoolTitles != len(s.pool) {
		t.Fatalf("pool %d titles, want %d (the owned one left out)", counts.PoolTitles, len(s.pool))
	}

	s.lock()
	counts = s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if s.tmdb.detailCalls(s.seeds...) != 3 || s.tmdb.pages != 2 {
		t.Fatalf("fresh lists or sets fetched again: seed calls %d, pages %d", s.tmdb.detailCalls(s.seeds...), s.tmdb.pages)
	}
	if counts.DetailsFetched != len(s.pool) || counts.Embeddings.Embedded != len(s.pool) || s.vectors() != len(s.pool) {
		t.Fatalf("second run: %+v, %d vectors", counts, s.vectors())
	}
	owned := ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: s.base + 12}
	if stored, err := s.engine.repo.externalTitlesByKeys(ctx, []ExternalTitleKey{owned}); err != nil || len(stored) != 0 {
		t.Fatalf("owned title stored: %v (%v)", stored, err)
	}

	// Nothing is due: no TMDB call, nothing embedded.
	before := *s.embedded
	if counts = s.build(externalTMDBRequestBudget, externalEmbedBudget); counts.TMDBRequests != 0 || *s.embedded != before {
		t.Fatalf("idle run: %+v", counts)
	}

	// Old details are refetched; only the title whose text changed is
	// embedded again.
	if _, err := s.db.Exec(ctx, `UPDATE external_titles SET detail_fetched_at = now() - interval '121 days' WHERE tmdb_id BETWEEN $1 AND $1 + 999`, s.base); err != nil {
		t.Fatal(err)
	}
	s.tmdb.details[s.pool[0]].Overview = "a new overview"
	counts = s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if counts.DetailsFetched != len(s.pool) || counts.Embeddings.Embedded != 1 || counts.TextUnchanged != len(s.pool)-1 || *s.embedded != before+1 {
		t.Fatalf("refetch run: %+v, %d texts embedded", counts, *s.embedded-before)
	}
}

// A run that spends its TMDB or embedding budget stops cleanly and says what
// is left; the next runs continue from stored state until the pool is done.
func TestBuildExternalPoolBudgetDB(t *testing.T) {
	s := newExternalPoolScenario(t, 921_000_000)
	s.lock()

	// Two set pages and two of the three seeds.
	counts := s.build(4, externalEmbedBudget)
	if !counts.BudgetExhausted || counts.TMDBRequests != 4 || counts.SourcesLeft != 1 || counts.DetailsFetched != 0 || counts.DetailsLeft == 0 {
		t.Fatalf("TMDB budget run: %+v", counts)
	}

	// The last seed, then two titles' details: the embedding budget.
	counts = s.build(externalTMDBRequestBudget, 2)
	if !counts.BudgetExhausted || counts.SourcesLeft != 0 || counts.Embeddings.Embedded != 2 || counts.DetailsLeft != len(s.pool)-2 {
		t.Fatalf("embedding budget run: %+v", counts)
	}

	counts = s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if counts.BudgetExhausted || counts.DetailsLeft != 0 || s.vectors() != len(s.pool) {
		t.Fatalf("final run: %+v, %d vectors", counts, s.vectors())
	}
	for _, k := range s.seeds {
		if n := s.tmdb.detailCalls(k); n != 1 {
			t.Fatalf("seed %v fetched %d times", k, n)
		}
	}
}

// A pool title TMDB does not find at the detail stage loses the links that
// named it, so later runs do not ask for it again until a source names it
// anew.
func TestBuildExternalPoolForgetsTitlesTMDBLacksDB(t *testing.T) {
	s := newExternalPoolScenario(t, 922_000_000)
	s.lock()
	gone := s.pool[0]
	delete(s.tmdb.details, gone)

	counts := s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if counts.DetailsNotFound != 1 || s.tmdb.detailCalls(gone) != 1 {
		t.Fatalf("first run: %+v, %d calls for the missing title", counts, s.tmdb.detailCalls(gone))
	}
	var links int
	if err := s.db.QueryRow(context.Background(), `SELECT count(*) FROM external_title_links WHERE media_type = $1 AND tmdb_id = $2`, gone.MediaType, gone.TMDBID).Scan(&links); err != nil || links != 0 {
		t.Fatalf("links naming the missing title: %d (%v)", links, err)
	}
	if counts = s.build(externalTMDBRequestBudget, externalEmbedBudget); counts.DetailsNotFound != 0 || s.tmdb.detailCalls(gone) != 1 {
		t.Fatalf("second run asked again: %+v, %d calls", counts, s.tmdb.detailCalls(gone))
	}
}

// A title the library has under its IMDb or TVDB ID is owned too: one the
// detail reveals is neither stored nor fetched again, and a stored one the
// library gains later is deleted by its stored IDs.
func TestBuildExternalPoolDropsTitlesOwnedByOtherIDsDB(t *testing.T) {
	s := newExternalPoolScenario(t, 923_000_000)
	ctx := context.Background()
	s.lock()
	byIMDb, byTVDB := s.pool[0], s.pool[3] // a movie and series 20
	s.tmdb.details[byIMDb].IMDbID = "tt923000010"
	s.tmdb.details[byTVDB].TVDBID = 923
	s.owned.imdb["tt923000010"] = true

	counts := s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if counts.DetailsOwned != 1 || counts.Embeddings.Embedded != len(s.pool)-1 {
		t.Fatalf("first run: %+v", counts)
	}
	stored, err := s.engine.repo.externalTitlesByKeys(ctx, []ExternalTitleKey{byIMDb, byTVDB})
	if err != nil || len(stored) != 1 || stored[byTVDB].TVDBID != 923 {
		t.Fatalf("stored = %+v (%v), want only the series, with its TVDB ID", stored, err)
	}

	s.owned.tvdb["923"] = true
	counts = s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if s.tmdb.detailCalls(byIMDb) != 1 || counts.TitlesDeleted != 1 || counts.PoolTitles != len(s.pool)-2 {
		t.Fatalf("second run: %+v, %d detail calls for the IMDb-owned title", counts, s.tmdb.detailCalls(byIMDb))
	}
	if stored, err := s.engine.repo.externalTitlesByKeys(ctx, []ExternalTitleKey{byTVDB}); err != nil || len(stored) != 0 {
		t.Fatalf("series owned by TVDB ID still stored: %+v (%v)", stored, err)
	}
}

// A title whose text the provider refuses is remembered: later runs neither
// fetch its detail nor try to embed it until its stored detail is due, and a
// refetch with the same text is not sent again.
func TestBuildExternalPoolRemembersRefusedTitlesDB(t *testing.T) {
	s := newExternalPoolScenario(t, 924_000_000)
	ctx := context.Background()
	s.lock()
	s.engine.externalRefusedEmbeds = newRefusedEmbedInputs()
	refused := s.pool[0]
	s.tmdb.details[refused].Overview = "refuse me"
	var mu sync.Mutex
	attempts := 0
	s.engine.embClient = quotaTestEmbedder(func(_ context.Context, in []string) ([][]float32, error) {
		out := make([][]float32, len(in))
		for i, text := range in {
			if strings.Contains(text, "refuse me") {
				mu.Lock()
				attempts++
				mu.Unlock()
				return nil, testRefusal
			}
			out[i] = testVector(4)
		}
		return out, nil
	})
	tries := func() int {
		mu.Lock()
		defer mu.Unlock()
		return attempts
	}

	counts := s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if counts.Embeddings.Skipped != 1 || counts.Embeddings.Embedded != len(s.pool)-1 {
		t.Fatalf("first run: %+v", counts)
	}
	firstTries := tries()

	counts = s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if s.tmdb.detailCalls(refused) != 1 || tries() != firstTries || counts.RefusedSkipped != 1 {
		t.Fatalf("second run: %+v, %d detail calls, %d tries", counts, s.tmdb.detailCalls(refused), tries()-firstTries)
	}

	// A due detail is fetched again; the same text is not sent again.
	if _, err := s.db.Exec(ctx, `UPDATE external_titles SET detail_fetched_at = now() - interval '121 days' WHERE media_type = $1 AND tmdb_id = $2`, refused.MediaType, refused.TMDBID); err != nil {
		t.Fatal(err)
	}
	counts = s.build(externalTMDBRequestBudget, externalEmbedBudget)
	if s.tmdb.detailCalls(refused) != 2 || tries() != firstTries || counts.RefusedSkipped != 1 {
		t.Fatalf("refetch run: %+v, %d detail calls, %d tries", counts, s.tmdb.detailCalls(refused), tries()-firstTries)
	}
}

package recommendations

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// externalRowsScenario is one profile with two seed movies, each in its own
// library: seed 1's recommendations name movies 10-21, seed 2's similar list
// names movies 10, 11 and 30. Every named title is stored with a vector; the
// nearer to the profile's taste the higher its number.
type externalRowsScenario struct {
	t                *testing.T
	pool             *pgxpool.Pool
	f                *externalFixture
	engine           *Engine
	userID           int
	profileID        string
	seed1, seed2     string
	library1         int
	titles           []ExternalTitleKey
	lockVersion      string
	externalSwitchOn bool
}

func newExternalRowsScenario(t *testing.T, base int) *externalRowsScenario {
	t.Helper()
	f := newExternalFixture(t, base)
	pool := f.pool
	ctx := context.Background()
	snapshotEmbeddingLock(t, pool)
	clearShared := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM external_title_sources WHERE source_key LIKE 'set:%' OR source_key IN ($1, $2)`,
			"seed:movie:"+strconv.Itoa(base+1), "seed:movie:"+strconv.Itoa(base+2))
	}
	clearShared()
	t.Cleanup(clearShared)

	prefix := "ext-rows-" + strconv.Itoa(base) + "-"
	cleanupRecoMediaItems(t, pool, prefix)
	s := &externalRowsScenario{t: t, pool: pool, f: f, externalSwitchOn: true}
	s.userID, s.profileID = newTasteTestAccount(t, pool, prefix)
	s.library1 = newTestLibrary(t, pool, prefix+"one")
	library2 := newTestLibrary(t, pool, prefix+"two")
	s.seed1 = seedTitle(t, pool, prefix+"seed1", "movie", s.library1, "tmdb_id = '"+strconv.Itoa(base+1)+"'")
	s.seed2 = seedTitle(t, pool, prefix+"seed2", "movie", library2, "tmdb_id = '"+strconv.Itoa(base+2)+"'")
	for _, id := range []string{s.seed1, s.seed2} {
		f.exec(`INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES ($1, $2, $3, 10)`, s.userID, s.profileID, id)
		f.exec(`INSERT INTO user_favorites(user_id, profile_id, media_item_id) VALUES ($1, $2, $3)`, s.userID, s.profileID, id)
	}

	var recs []ExternalLink
	for n := 10; n <= 21; n++ {
		recs = append(recs, ExternalLink{List: ExternalListRecommendations, Rank: n - 9, Title: f.movie(n)})
		s.titles = append(s.titles, f.movie(n))
	}
	s.titles = append(s.titles, f.movie(30))
	repo := f.repo
	for _, src := range []ExternalSourceResult{
		{SourceKey: "seed:movie:" + strconv.Itoa(base+1), Status: ExternalSourceOK, Links: recs},
		{SourceKey: "seed:movie:" + strconv.Itoa(base+2), Status: ExternalSourceOK, Links: []ExternalLink{
			{List: ExternalListSimilar, Rank: 1, Title: f.movie(10)},
			{List: ExternalListSimilar, Rank: 2, Title: f.movie(11)},
			{List: ExternalListSimilar, Rank: 3, Title: f.movie(30)},
		}},
	} {
		if err := repo.RecordExternalSource(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	f.storeTitles(s.titles...)
	s.storeVectors(s.titles...)

	if err := repo.SetEmbeddingLock(ctx, EmbeddingLock{BaseURL: "http://embed.test", Model: externalTestModel, SourceDimensions: 4, StorageDimensions: CanonicalEmbeddingDimensions}); err != nil {
		t.Fatal(err)
	}
	version, lock, err := repo.externalRowLock(ctx)
	if err != nil || lock == nil {
		t.Fatal(lock, err)
	}
	s.lockVersion = version
	if err := repo.UpsertTasteProfile(ctx, s.userID, s.profileID, axisVector(0, nil), map[string]int{}, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.engine = NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, pgstore.NewPostgresProvider(pool),
		config.RecommendationsConfig{Enabled: true, ExternalEnabled: true, EmbeddingBaseURL: "http://embed.test", EmbeddingModel: externalTestModel})
	// A rebuild must use stored data only: any TMDB or embedding call fails
	// the test.
	s.engine.embClient = quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
		t.Error("rebuild called the embedding provider")
		return nil, context.Canceled
	})
	s.engine.WithExternalTMDB(failingExternalTMDB{t})
	return s
}

// storeVectors stores a vector for each key, nearer the taste vector (axis 0)
// the higher the title's number.
func (s *externalRowsScenario) storeVectors(keys ...ExternalTitleKey) {
	s.t.Helper()
	embs := make([]ExternalEmbedding, len(keys))
	for i, k := range keys {
		angle := float64(100-(k.TMDBID-s.f.base)) / 100 // title 30: 0.7 rad, title 10: 0.9 rad
		embs[i] = ExternalEmbedding{Key: k, Model: externalTestModel, CanonicalText: "t", Vector: []float32{float32(math.Cos(angle)), float32(math.Sin(angle)), 0, 0}}
	}
	if err := s.f.repo.UpsertExternalEmbeddings(context.Background(), embs); err != nil {
		s.t.Fatal(err)
	}
}

func (s *externalRowsScenario) row() *ExternalRow {
	s.t.Helper()
	row, err := s.f.repo.externalRowFor(context.Background(), s.userID, s.profileID, ExternalMediaMovie)
	if err != nil {
		s.t.Fatal(err)
	}
	return row
}

type failingExternalTMDB struct{ t *testing.T }

func (f failingExternalTMDB) GetMediaDetailInLanguage(context.Context, string, int, string) (*tmdb.MediaDetail, error) {
	f.t.Error("called TMDB for a detail")
	return nil, context.Canceled
}

func (f failingExternalTMDB) DiscoverPage(context.Context, string, tmdb.DiscoverParams, int) (*tmdb.MediaPage, error) {
	f.t.Error("called TMDB discover")
	return nil, context.Canceled
}

// A single-profile rebuild ranks the stored pool from stored vectors alone,
// names each title's seeds best rank first, and deletes a row whose
// candidates fall below the minimum.
func TestRebuildExternalRowsDB(t *testing.T) {
	s := newExternalRowsScenario(t, 930_000_000)
	ctx := context.Background()

	built, err := s.engine.rebuildExternalRows(ctx, nil, s.userID, s.profileID, nil)
	if err != nil || built.Written != 1 {
		t.Fatalf("rebuild = %+v, %v; want the movie row written", built, err)
	}
	row := s.row()
	if row == nil || len(row.Items) != len(s.titles) || row.EmbeddingModel != externalTestModel {
		t.Fatalf("row %+v, want %d items under %s", row, len(s.titles), externalTestModel)
	}
	if row.Items[0].TMDBID != s.f.base+30 {
		t.Fatalf("highest-scoring title should lead MMR output: got %+v", row.Items)
	}
	for _, it := range row.Items {
		if math.IsNaN(it.Score) || math.IsInf(it.Score, 0) {
			t.Fatalf("non-finite score in row: %+v", it)
		}
	}
	reasons := map[int][]string{}
	for _, it := range row.Items {
		reasons[it.TMDBID-s.f.base] = it.Reasons
	}
	if !slices.Equal(reasons[10], []string{s.seed1, s.seed2}) || !slices.Equal(reasons[30], []string{s.seed2}) || !slices.Equal(reasons[15], []string{s.seed1}) {
		t.Fatalf("reasons %v", reasons)
	}

	// Below the minimum the row goes.
	if _, err := s.pool.Exec(ctx, `DELETE FROM external_title_embeddings WHERE tmdb_id BETWEEN $1 + 12 AND $1 + 30`, s.f.base); err != nil {
		t.Fatal(err)
	}
	if built, err = s.engine.rebuildExternalRows(ctx, nil, s.userID, s.profileID, nil); err != nil || built.Written != 0 || built.Deleted != 1 || s.row() != nil {
		t.Fatalf("rebuild with 2 candidates = %+v, %v, row %+v; want it deleted", built, err, s.row())
	}
}

type loseExternalProfileLock struct {
	pgClusterLock
	profileKey int64
}

func (l loseExternalProfileLock) TryAlso(ctx context.Context, key int64) (func(), bool, error) {
	release, acquired, err := l.pgClusterLock.TryAlso(ctx, key)
	if err == nil && acquired && key == l.profileKey {
		err = l.lock.Conn().Conn().Close(ctx)
	}
	return release, acquired, err
}

func TestExternalRowsStopWhenProfileLockSessionClosesDB(t *testing.T) {
	s := newExternalRowsScenario(t, 937_000_000)
	ctx := t.Context()
	worker := newJobTestWorker(pgJobLocker{pool: s.pool}, nil)
	worker.engine = s.engine
	held, acquired, err := worker.locker.TryLock(ctx, jobLockKeys[JobExternal])
	if err != nil || !acquired {
		t.Fatalf("claim outside-title job: acquired=%t, err=%v", acquired, err)
	}
	defer held.Release()
	lost := loseExternalProfileLock{held.(pgClusterLock), profileRefreshLock(s.userID, s.profileID)}

	result, err := worker.rebuildAllExternalRows(ctx, lost)
	if s.row() != nil || !errors.Is(err, errJobLockLost) {
		t.Fatalf("lost session: rebuilt %+v, err=%v, has row=%t", result, err, s.row() != nil)
	}
}

func TestExternalJobQueuesStaleProfileAfterRebuildDB(t *testing.T) {
	s := newExternalRowsScenario(t, 939_000_000)
	ctx := t.Context()
	worker := newJobTestWorker(pgJobLocker{pool: s.pool}, nil)
	worker.engine = s.engine
	worker.profileRefreshCh = make(chan profileRefreshRequest, 1)
	worker.profileRefreshPending = map[string]struct{}{}
	if err := s.f.repo.MarkProfileStale(ctx, s.userID, s.profileID); err != nil {
		t.Fatal(err)
	}
	if result, err := worker.rebuildAllExternalRows(ctx, nil); err != nil || result.RowsBuilt != 1 {
		t.Fatalf("rebuild = %+v, err=%v", result, err)
	}
	select {
	case refresh := <-worker.profileRefreshCh:
		if refresh.userID != s.userID || refresh.profileID != s.profileID {
			t.Fatalf("queued refresh = %+v", refresh)
		}
	default:
		t.Fatal("stale profile was left for the next sweep")
	}
}

type unavailableExternalUserStore struct {
	userstore.UserStoreProvider
	userID int
	err    error
}

func (p unavailableExternalUserStore) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	if userID == p.userID {
		return nil, p.err
	}
	return p.UserStoreProvider.ForUser(ctx, userID)
}

func TestExternalPoolContinuesPastUnreadableProfileDB(t *testing.T) {
	s := newExternalRowsScenario(t, 940_000_000)
	ctx := t.Context()
	userID, profileID := newTasteTestAccount(t, s.pool, "ext-pool-unreadable")
	if err := s.f.repo.UpsertTasteProfile(ctx, userID, profileID, axisVector(0, nil), nil, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("one account store failed to open")
	s.engine.storeProvider = unavailableExternalUserStore{s.engine.storeProvider, userID, readErr}
	newTitle := s.f.movie(40)
	tmdb := &fakeExternalTMDB{calls: map[ExternalTitleKey]int{}, discover: map[string][]int{ExternalMediaMovie: {newTitle.TMDBID}}}
	s.engine.WithExternalTMDB(tmdb)

	counts, err := s.engine.BuildExternalPool(ctx, ExternalPoolOptions{MaxTitles: len(s.titles)})
	if !errors.Is(err, readErr) || counts.Profiles != 1 || tmdb.pages == 0 {
		t.Fatalf("partial run = %+v, err=%v, discover calls=%d", counts, err, tmdb.pages)
	}
	if stored, err := s.f.repo.externalTitlesByKeys(ctx, s.titles); err != nil || len(stored) != len(s.titles) {
		t.Fatalf("partial run removed stored titles: found %d of %d, err=%v", len(stored), len(s.titles), err)
	}
	if calls := tmdb.detailCalls(newTitle); calls != 0 {
		t.Fatalf("new title should not replace a stored one while a profile is unreadable, got %d detail calls", calls)
	}
}

// A row is not written once the embedding lock it was built under is gone
// or changed, including when an embeddings reset deletes the lock while the
// write waits; a profile deleted meanwhile keeps no row.
func TestExternalRowRefusalsDB(t *testing.T) {
	s := newExternalRowsScenario(t, 931_000_000)
	ctx := context.Background()
	repo := s.f.repo
	row := ExternalRow{UserID: s.userID, ProfileID: s.profileID, MediaType: ExternalMediaMovie,
		Items: []ExternalRowItem{{TMDBID: s.titles[0].TMDBID}}, EmbeddingModel: externalTestModel, BuiltAt: time.Now()}

	if written, err := repo.writeExternalRowUnderLock(ctx, row, s.lockVersion+" "); err != nil || written || s.row() != nil {
		t.Fatalf("write under a changed lock = %v, %v", written, err)
	}

	// The reset deletes the lock and has not committed: the write waits for
	// it, then finds no lock.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey); err != nil {
		t.Fatal(err)
	}
	type result struct {
		written bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		written, err := repo.writeExternalRowUnderLock(ctx, row, s.lockVersion)
		done <- result{written, err}
	}()
	waitForLockWait(t, s.pool, "INSERT INTO external_recommendation_rows")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.err != nil || res.written || s.row() != nil {
		t.Fatalf("write racing a reset = %+v, row %+v", res, s.row())
	}

	// A deleted profile: a row written after the deletion's purge is
	// removed by the rebuild's own check.
	if err := repo.SetEmbeddingLock(ctx, EmbeddingLock{BaseURL: "http://embed.test", Model: externalTestModel, SourceDimensions: 4, StorageDimensions: CanonicalEmbeddingDimensions}); err != nil {
		t.Fatal(err)
	}
	// The same model's lock written again after the reset is another lock:
	// a row ranked before the reset is refused.
	if written, err := repo.writeExternalRowUnderLock(ctx, row, s.lockVersion); err != nil || written || s.row() != nil {
		t.Fatalf("write under a recreated lock = %v, %v", written, err)
	}
	raw, lock, err := repo.externalRowLock(ctx)
	if err != nil || lock == nil {
		t.Fatal(lock, err)
	}
	if written, err := repo.writeExternalRowUnderLock(ctx, row, raw); err != nil || !written {
		t.Fatalf("write under the current lock = %v, %v", written, err)
	}
	s.f.exec(`DELETE FROM user_profiles WHERE id = $1`, s.profileID)
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = s.engine
	if _, err := w.rebuildProfileExternalRows(ctx, nil, s.userID, s.profileID, nil); err != nil || s.row() != nil {
		t.Fatalf("rebuild of a deleted profile = %v, row %+v", err, s.row())
	}
}

// waitForLockWait waits until a session running a query that contains text
// waits on a lock.
func waitForLockWait(t *testing.T, pool *pgxpool.Pool, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			               WHERE wait_event_type = 'Lock' AND position($1 IN query) > 0)`, text).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("no session waited on the lock")
		case <-tick.C:
		}
	}
}

// The embeddings reset deletes outside vectors and rows and keeps the
// outside titles.
func TestResetEmbeddingsClearsOutsideVectorsAndRowsDB(t *testing.T) {
	s := newExternalRowsScenario(t, 932_000_000)
	ctx := context.Background()
	if _, err := s.engine.rebuildExternalRows(ctx, nil, s.userID, s.profileID, nil); err != nil || s.row() == nil {
		t.Fatalf("rebuild: %v", err)
	}
	res, err := s.f.repo.ResetEmbeddings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalEmbeddings < int64(len(s.titles)) || res.ExternalRows < 1 {
		t.Fatalf("reset %+v", res)
	}
	texts, err := s.f.repo.ExternalEmbeddingTexts(ctx, s.titles, externalTestModel)
	if err != nil || len(texts) != 0 || s.row() != nil {
		t.Fatalf("after the reset: %d vectors (%v), row %+v", len(texts), err, s.row())
	}
	stored, err := s.f.repo.externalTitlesByKeys(ctx, s.titles)
	if err != nil || len(stored) != len(s.titles) {
		t.Fatalf("titles after the reset: %d (%v)", len(stored), err)
	}
}

// With the feature off the job applies retention and nothing else: no TMDB
// call, no embedding, no rows.
func TestExternalJobWithFeatureOffOnlyPrunesDB(t *testing.T) {
	s := newExternalRowsScenario(t, 933_000_000)
	ctx := context.Background()
	s.f.exec(`UPDATE external_titles SET detail_fetched_at = now() - interval '200 days' WHERE media_type = 'movie' AND tmdb_id = $1`, s.titles[0].TMDBID)
	cfg := s.engine.cfg
	cfg.ExternalEnabled = false
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = s.engine
	w.WithLiveConfig(func() config.RecommendationsConfig { return cfg })

	res, err := w.doExternal(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Enabled || res.PrunedTitles < 1 || res.TMDBRequests != 0 || res.RowProfiles != 0 || s.row() != nil {
		t.Fatalf("job with the feature off: %+v, row %+v", res, s.row())
	}
	if stored, err := s.f.repo.externalTitlesByKeys(ctx, s.titles[:1]); err != nil || len(stored) != 0 {
		t.Fatalf("expired title kept: %v (%v)", stored, err)
	}
}

// A server started with recommendations off has no worker; its retention
// loop still deletes an expired title.
func TestRunExternalRetentionPrunesWithoutAWorkerDB(t *testing.T) {
	s := newExternalRowsScenario(t, 936_000_000)
	s.f.exec(`UPDATE external_titles SET detail_fetched_at = now() - interval '200 days' WHERE media_type = 'movie' AND tmdb_id = $1`, s.titles[0].TMDBID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunExternalRetention(ctx, s.pool)
	}()
	eventually(t, "the expired title to be pruned", func() bool {
		stored, err := s.f.repo.externalTitlesByKeys(context.Background(), s.titles[:1])
		return err == nil && len(stored) == 0
	})
	cancel()
	<-done
}

// Reads serve the stored row with card data only while the feature is on,
// and drop a reason the viewer cannot see while keeping its title.
func TestExternalRecommendationsReadDB(t *testing.T) {
	s := newExternalRowsScenario(t, 934_000_000)
	ctx := context.Background()
	if _, err := s.engine.rebuildExternalRows(ctx, nil, s.userID, s.profileID, nil); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(s.f.repo, nil, nil, pgstore.NewPostgresProvider(s.pool)).
		WithExternalRecommendations(func() bool { return s.externalSwitchOn })
	read := func(r *Reader, filter catalog.AccessFilter) []ExternalRecommendation {
		t.Helper()
		got, err := r.ExternalRecommendations(ctx, s.userID, s.profileID, ExternalMediaMovie, filter)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	all := read(reader, catalog.AccessFilter{})
	if len(all) != len(s.titles) || all[0].Title == "" || all[0].Key.MediaType != ExternalMediaMovie || all[0].VoteCount == 0 ||
		all[0].IMDbID == "" || all[0].TVDBID == 0 {
		t.Fatalf("read %d titles, first %+v", len(all), all[0])
	}
	byID := map[int]ExternalRecommendation{}
	for _, r := range all {
		byID[r.Key.TMDBID-s.f.base] = r
	}
	if got := byID[10].Reasons; len(got) != 2 || got[0] != (ExternalReason{ContentID: s.seed1, Title: s.seed1}) {
		t.Fatalf("reasons of title 10: %+v", got)
	}

	// Seed 2 sits in a library the viewer cannot see.
	limited := read(reader, catalog.AccessFilter{UserID: s.userID, ProfileID: s.profileID, AllowedLibraryIDs: []int{s.library1}})
	if len(limited) != len(s.titles) {
		t.Fatalf("a hidden reason removed titles: %d of %d", len(limited), len(s.titles))
	}
	for _, r := range limited {
		for _, reason := range r.Reasons {
			if reason.ContentID == s.seed2 {
				t.Fatalf("title %d names the hidden seed", r.Key.TMDBID)
			}
		}
		if r.Key.TMDBID == s.f.base+30 && len(r.Reasons) != 0 {
			t.Fatalf("title 30 reasons %+v, want none", r.Reasons)
		}
	}

	s.externalSwitchOn = false
	if got := read(reader, catalog.AccessFilter{}); got != nil || reader.ExternalRecommendationsEnabled() {
		t.Fatalf("feature off served %d titles", len(got))
	}
	s.externalSwitchOn = true
	if got := read(reader.WithPersonalRows(false), catalog.AccessFilter{}); got != nil {
		t.Fatalf("recommendations disabled served %d titles", len(got))
	}
}

type waitingDiscoverClient struct {
	failingExternalTMDB
	started chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (c *waitingDiscoverClient) DiscoverPage(ctx context.Context, _ string, _ tmdb.DiscoverParams, _ int) (*tmdb.MediaPage, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.resume:
		return &tmdb.MediaPage{}, nil
	}
}

func TestExternalJobStopsDuringFetchDB(t *testing.T) {
	for i, reason := range []string{"switch off", "session lost"} {
		t.Run(reason, func(t *testing.T) {
			s := newExternalRowsScenario(t, 938_000_000+i*100)
			w := newJobTestWorker(&fakeLocker{}, nil)
			w.engine = s.engine
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			held, acquired, err := (pgJobLocker{s.pool}).TryLock(ctx, externalJobLock)
			if err != nil || !acquired {
				t.Fatalf("acquire job lock: %v, %v", acquired, err)
			}
			pid := held.(pgClusterLock).lock.Conn().Conn().PgConn().PID()
			client := &waitingDiscoverClient{failingExternalTMDB: failingExternalTMDB{t}, started: make(chan struct{}), resume: make(chan struct{})}
			w.engine.WithExternalTMDB(client)
			done := make(chan error, 1)
			go func() {
				_, err := w.doExternal(ctx, held)
				held.Release()
				done <- err
			}()
			select {
			case <-client.started:
			case err := <-done:
				t.Fatalf("job stopped before TMDB: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var want error
			if reason == "switch off" {
				w.StopExternalGeneration()
			} else {
				s.terminateDuringSourceWrite(ctx, pid, client.resume)
				want = errJobLockLost
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("job ended with %v, want %v", err, want)
			}
			var recorded int
			if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM external_title_sources WHERE source_key LIKE 'set:%'").Scan(&recorded); err != nil || recorded != 0 {
				t.Fatalf("recorded %d interrupted sources: %v", recorded, err)
			}
		})
	}
}

// Block the write after the last probe, then drop its job session. A write
// on another connection would commit when the table lock is released.
func (s *externalRowsScenario) terminateDuringSourceWrite(ctx context.Context, pid uint32, resume chan struct{}) {
	s.t.Helper()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "LOCK external_title_sources IN SHARE MODE"); err != nil {
		s.t.Fatal(err)
	}
	close(resume)
	waitForLockWait(s.t, s.pool, "INSERT INTO external_title_sources")
	s.f.exec("SELECT pg_terminate_backend($1, 5000)", pid)
}

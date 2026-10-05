package recommendations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	taskrepository "github.com/Silo-Server/silo-server/internal/taskmanager/repository"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Two workers on separate pools stand in for two API servers sharing one
// database: while one runs a job the other skips it, and only finished runs
// are recorded in task_executions.
func TestRecommendationJobRunsOnOneServerPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	otherPool, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(otherPool.Close)

	var lastID int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM task_executions`).Scan(&lastID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_executions WHERE id > $1 AND task_key LIKE 'recommendations.%'`, lastID)
	})

	serverA := newJobTestWorker(pgJobLocker{pool: pool}, taskrepository.NewPgExecutionRepository(pool))
	serverB := newJobTestWorker(pgJobLocker{pool: otherPool}, taskrepository.NewPgExecutionRepository(otherPool))

	started := make(chan struct{})
	finish := make(chan struct{})
	doneA := make(chan error, 1)
	go func() {
		doneA <- serverA.runJob(job{name: JobCowatch, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
			close(started)
			<-finish
			return countsResult{Done: 1}, nil
		}}, false)
	}()
	select {
	case <-started:
	case err := <-doneA:
		t.Fatalf("server A did not start the job: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server A did not start the job")
	}

	ranB := false
	err = serverB.runJob(job{name: JobCowatch, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		ranB = true
		return countsResult{}, nil
	}}, false)
	if !errors.Is(err, ErrJobRunningElsewhere) || ranB {
		t.Fatalf("server B while A runs: err=%v ran=%v; want ErrJobRunningElsewhere", err, ranB)
	}
	// Other jobs are not blocked by this one.
	if err := serverB.runJob(job{name: JobTasteProfiles, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		return countsResult{}, nil
	}}, false); err != nil {
		t.Fatalf("a different job on server B: %v", err)
	}

	close(finish)
	if err := <-doneA; err != nil {
		t.Fatalf("server A run: %v", err)
	}
	// A released the lock when its run returned, so B can take the job now.
	if err := serverB.runJob(job{name: JobCowatch, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		ranB = true
		return countsResult{Done: 4, Failed: 2}, errors.New("co-watch upsert failed")
	}}, false); err != nil || !ranB {
		t.Fatalf("server B after A finished: err=%v ran=%v", err, ranB)
	}

	rows, err := pool.Query(ctx, `
		SELECT status, COALESCE(error_message, ''), result_data::text
		FROM task_executions
		WHERE id > $1 AND task_key = $2
		ORDER BY id`, lastID, cowatchTaskKey)
	if err != nil {
		t.Fatal(err)
	}
	type recorded struct{ status, errMsg, data string }
	var runs []recorded
	for rows.Next() {
		var r recorded
		if err := rows.Scan(&r.status, &r.errMsg, &r.data); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []recorded{
		{runStatusCompleted, "", `{"done": 1, "failed": 0}`},
		{runStatusFailed, "co-watch upsert failed", `{"done": 4, "failed": 2}`},
	}
	if len(runs) != len(want) {
		t.Fatalf("recorded co-watch runs = %+v, want %+v (the skipped run must not be recorded)", runs, want)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Fatalf("run %d = %+v, want %+v", i, runs[i], want[i])
		}
	}

	last, err := serverA.LastRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run := last[JobCowatch]; run.Status != runStatusFailed || run.ErrorMessage != "co-watch upsert failed" {
		t.Fatalf("last co-watch run = %+v", run)
	}
	if run := last[JobTasteProfiles]; run.Status != runStatusCompleted {
		t.Fatalf("last taste profile run = %+v", run)
	}
}

func TestListCacheRefreshCandidatesOldestFirstPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"cache-order-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	var dims int
	if err := pool.QueryRow(ctx, `
		SELECT atttypmod FROM pg_attribute
		WHERE attrelid = 'public.user_taste_profiles'::regclass AND attname = 'embedding'`).Scan(&dims); err != nil {
		t.Fatalf("read embedding dimensions: %v", err)
	}

	repo := NewRepo(pool)
	for _, profile := range []string{"recent", "never", "old"} {
		if err := repo.UpsertTasteProfile(ctx, userID, profile, make([]float32, dims), map[string]int{}, "", time.Now()); err != nil {
			t.Fatalf("seed taste profile %s: %v", profile, err)
		}
	}
	items := []ScoredItem{{MediaItemID: "item", Score: 1}}
	expiry := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	for _, profile := range []string{"recent", "old"} {
		if err := repo.UpsertRecommendationCache(ctx, userID, profile, RecTypeForYouMain, "", items, expiry.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	// A profile with only other rows has no main row and still goes first.
	if err := repo.UpsertRecommendationCache(ctx, userID, "never", RecTypeForYouClusterPrefix+"0", "", items, expiry.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE recommendation_cache
		SET    created_at = NOW() - CASE profile_id WHEN 'old' THEN interval '30 hours' ELSE interval '1 hour' END
		WHERE  user_id = $1 AND rec_type = $2`, userID, RecTypeForYouMain); err != nil {
		t.Fatal(err)
	}

	candidates, err := repo.ListCacheRefreshCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, c := range candidates {
		if c.UserID == userID {
			mine = append(mine, c.ProfileID)
		}
	}
	if strings.Join(mine, ",") != "never,old,recent" {
		t.Fatalf("candidates = %v, want never, old, recent", mine)
	}

	// A global row with a unique type makes the existence check true.
	recType := "test_global_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM recommendation_cache WHERE user_id IS NULL AND rec_type = $1`, recType)
	})
	if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, recType, "", items, expiry.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if cached, err := repo.HasGlobalRecommendationCache(ctx); err != nil || !cached {
		t.Fatalf("global cache check = %v, %v; want true", cached, err)
	}
}

func TestExtendGlobalRecommendationCachePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"cache-extend-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	base := "test_extend_" + uuid.NewString() + "_"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM recommendation_cache WHERE starts_with(rec_type, $1)`, base)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	now := time.Now().UTC().Truncate(time.Second)
	soon, next, beyond, gone := now.Add(time.Hour), now.Add(cacheTTL), now.Add(cacheTTL+time.Hour), now.Add(-time.Hour)
	items := []ScoredItem{{MediaItemID: "item", Score: 1}}
	seed := func(userID int, recType string, expires time.Time) {
		t.Helper()
		if err := repo.UpsertRecommendationCache(ctx, userID, GlobalCacheProfileID, recType, "", items, expires.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	seed(GlobalCacheUserID, base+"one", soon)
	seed(GlobalCacheUserID, base+"one_more", soon)
	seed(GlobalCacheUserID, base+"p_a", soon)
	seed(GlobalCacheUserID, base+"p_b", beyond)
	seed(GlobalCacheUserID, base+"p_gone", gone)
	// An account row is not global, even under the same profile ID and type.
	seed(userID, base+"p_a", soon)

	// expiry reads a row's expiry; a nil owner is the global row.
	expiry := func(owner *int, recType string) time.Time {
		t.Helper()
		var at time.Time
		if err := pool.QueryRow(ctx, `
			SELECT expires_at FROM recommendation_cache
			WHERE  user_id IS NOT DISTINCT FROM $1::int AND profile_id = $2
			  AND  rec_type = $3 AND source_item_id = ''`,
			owner, GlobalCacheProfileID, recType).Scan(&at); err != nil {
			t.Fatalf("read %s expiry: %v", recType, err)
		}
		return at
	}

	n, err := repo.ExtendGlobalRecommendationCache(ctx, base+"one", false, next.Format(time.RFC3339))
	if err != nil || n != 1 {
		t.Fatalf("exact extend = %d, %v; want 1 row", n, err)
	}
	if got := expiry(nil, base+"one"); !got.Equal(next) {
		t.Fatalf("extended row expires %v, want %v", got, next)
	}
	if got := expiry(nil, base+"one_more"); !got.Equal(soon) {
		t.Fatalf("exact extend reached a longer type: %v", got)
	}

	n, err = repo.ExtendGlobalRecommendationCache(ctx, base+"p_", true, next.Format(time.RFC3339))
	if err != nil || n != 2 {
		t.Fatalf("prefix extend = %d, %v; want the 2 unexpired global rows", n, err)
	}
	for recType, want := range map[string]time.Time{
		base + "p_a":    next,
		base + "p_b":    beyond, // never shortened
		base + "p_gone": gone,   // an expired row is not brought back
	} {
		if got := expiry(nil, recType); !got.Equal(want) {
			t.Fatalf("%s expires %v, want %v", recType, got, want)
		}
	}
	if got := expiry(&userID, base+"p_a"); !got.Equal(soon) {
		t.Fatalf("account row extended to %v", got)
	}
}

// On a user store outside Postgres, the nightly taste job finds profiles
// whose only signals live in the store, and skips the store's profiles with
// none. On the Postgres store its subject query alone decides.
func TestTasteProfileSubjectsReadTheSQLiteStorePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"taste-subjects-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	provider := userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()}))
	t.Cleanup(func() { _ = provider.Close() })
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"with-signals", "empty"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatalf("create profile %s: %v", id, err)
		}
	}
	if err := store.AddFavorite(ctx, "with-signals", "movie-only-in-sqlite"); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(pool, nil, nil, nil, provider, config.RecommendationsConfig{})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	subjectsOf := func() []string {
		t.Helper()
		subjects, failed, err := w.tasteProfileSubjects(ctx)
		if err != nil || failed != 0 {
			t.Fatalf("list subjects: failed=%d err=%v", failed, err)
		}
		var profiles []string
		for _, s := range subjects {
			if s.UserID == userID {
				profiles = append(profiles, s.ProfileID)
			}
		}
		return profiles
	}

	if got := subjectsOf(); len(got) != 0 {
		t.Fatalf("subjects on the Postgres store = %v, want none: the SQLite rows are invisible to the query", got)
	}
	engine.WithUserStoreOutsidePostgres(true)
	if got := subjectsOf(); len(got) != 1 || got[0] != "with-signals" {
		t.Fatalf("subjects on the SQLite store = %v, want [with-signals]", got)
	}
}

// On the Postgres store the nightly taste job skips the rows of a profile its
// account no longer lists, as the purge migration does, and keeps an account
// that lists no profile at all. A store outside Postgres cannot tell, so it
// keeps them.
func TestTasteProfileSubjectsSkipDeletedProfilesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	userID, live := newTasteTestAccount(t, pool, "taste-subjects-deleted-")
	var unlisted int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"taste-subjects-unlisted-"+uuid.NewString()).Scan(&unlisted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, unlisted) })

	repo := NewRepo(pool)
	vec := axisVector(1800, nil)
	for _, s := range []StaleProfile{{UserID: userID, ProfileID: live}, {UserID: userID, ProfileID: "deleted"}, {UserID: unlisted, ProfileID: "unlisted"}} {
		if err := repo.UpsertTasteProfile(ctx, s.UserID, s.ProfileID, vec, map[string]int{}, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewEngine(pool, nil, nil, nil, pgstore.NewPostgresProvider(pool), config.RecommendationsConfig{})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	subjectsOf := func() []string {
		t.Helper()
		subjects, _, err := w.tasteProfileSubjects(ctx)
		if err != nil {
			t.Fatalf("list subjects: %v", err)
		}
		var profiles []string
		for _, s := range subjects {
			if s.UserID == userID || s.UserID == unlisted {
				profiles = append(profiles, s.ProfileID)
			}
		}
		slices.Sort(profiles)
		return profiles
	}

	want := []string{live, "unlisted"}
	slices.Sort(want)
	if got := subjectsOf(); !slices.Equal(got, want) {
		t.Fatalf("subjects on the Postgres store = %v, want %v", got, want)
	}
	engine.WithUserStoreOutsidePostgres(true)
	if got := subjectsOf(); !slices.Contains(got, "deleted") {
		t.Fatalf("subjects on a store outside Postgres = %v, want the unlisted profile kept", got)
	}
}

// The profile's cached rows leave out what it watched and what it favorited,
// including taste-seed picks and a favorited episode's series, while its
// watchlist titles stay recommendable. This covers the main and cluster rows,
// Because You Watched and Similar Users.
func TestCacheUserRowsLeaveOutWatchedAndFavoritedTitlesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tcache-exclude-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, profile := newTasteTestAccount(t, pool, prefix)
	// Similar Users needs three peer accounts, and two of them behind a title.
	type account struct {
		userID  int
		profile string
	}
	var peers []account
	for i := range minSimilarUsersPeerAccounts {
		peerID, peer := newTasteTestAccount(t, pool, fmt.Sprintf("%speer-%d-", prefix, i))
		peers = append(peers, account{peerID, peer})
	}
	repo := NewRepo(pool)

	const axis = 2100
	seedPick, watched, watchlist := prefix+"seed-pick", prefix+"watched", prefix+"watchlist"
	plain := []string{prefix + "plain-1", prefix + "plain-2", prefix + "plain-3"}
	series, episode := prefix+"series", prefix+"episode"
	for i, id := range append([]string{seedPick, watched, watchlist, series}, plain...) {
		mediaType := "movie"
		if id == series {
			mediaType = "series"
		}
		seedRecoMediaItem(t, pool, id, mediaType, "matched")
		if err := repo.UpsertEmbedding(ctx, id, axisVector(axis, map[int]float32{axis + 1 + i: 0.05}), "test-model", id); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO episodes(content_id, series_id, season_number, episode_number) VALUES($1, $2, 1, 1)`, []any{episode, series}},
		{`INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, updated_at) VALUES($1, $2, $3, true, NOW())`, []any{userID, profile, watched}},
		{`INSERT INTO user_favorites(user_id, profile_id, media_item_id) VALUES($1, $2, $3), ($1, $2, $4)`, []any{userID, profile, seedPick, episode}},
		{`INSERT INTO user_watchlist(user_id, profile_id, media_item_id) VALUES($1, $2, $3)`, []any{userID, profile, watchlist}},
		{`INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 5), ($1, $2, $4, 5)`, []any{peers[0].userID, peers[0].profile, seedPick, plain[0]}},
		{`INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 5), ($1, $2, $4, 5)`, []any{peers[1].userID, peers[1].profile, seedPick, plain[0]}},
	} {
		if _, err := pool.Exec(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed %q: %v", stmt.query, err)
		}
	}
	taste := axisVector(axis, nil)
	now := time.Now()
	for _, p := range append([]account{{userID, profile}}, peers...) {
		if err := repo.UpsertTasteProfile(ctx, p.userID, p.profile, taste, map[string]int{"favorited": 2, "watch_high": 1}, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UpsertTasteClusters(ctx, userID, profile, []TasteCluster{{ClusterIdx: 0, Embedding: taste, Label: "Test", MemberCount: 2, TotalWeight: 1.6}}); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, pgstore.NewPostgresProvider(pool), config.RecommendationsConfig{DiversityLambda: 0.7})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	if built := w.cacheUserRows(ctx, engine.repo, userID, profile, cacheExpiry(now)); built.failed != 0 {
		t.Fatalf("cache build = %+v, want no failures", built)
	}

	excluded := []string{seedPick, watched, series}
	for _, row := range []struct {
		recType, source string
		mustHave        string
	}{
		{RecTypeForYouMain, "", watchlist},
		{RecTypeForYouClusterPrefix + "0", "", watchlist},
		{RecTypeSimilarUsersLiked, "", plain[0]},
		{RecTypeBecauseWatched, watched, plain[1]},
	} {
		items, err := repo.GetRecommendationCache(ctx, userID, profile, row.recType, row.source)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.MediaItemID
		}
		for _, id := range excluded {
			if slices.Contains(ids, id) {
				t.Fatalf("%s row %v holds excluded title %s", row.recType, ids, id)
			}
		}
		if !slices.Contains(ids, row.mustHave) {
			t.Fatalf("%s row %v is missing %s", row.recType, ids, row.mustHave)
		}
	}
}

// A main, cluster or Because You Watched row whose rebuild finds nothing, here
// after the profile's access narrowed to no library, is deleted instead of
// serving its old picks until it expires. A row whose build fails keeps its
// cached version, and Similar Users is still cached empty.
func TestCacheUserRowsDropRowsThatRebuildEmptyPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tcache-empty-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, profile := newTasteTestAccount(t, pool, prefix)
	repo := NewRepo(pool)

	anchor := prefix + "anchor"
	taste := axisVector(2200, nil)
	seedRecoMediaItem(t, pool, anchor, "movie", "matched")
	if err := repo.UpsertEmbedding(ctx, anchor, taste, "test-model", anchor); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.UpsertTasteProfile(ctx, userID, profile, taste, map[string]int{"watch_high": 1}, "", now); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, updated_at) VALUES($1, $2, '` + anchor + `', true, NOW())`,
		// Restrictions with no library allowed admit no title.
		`UPDATE user_profiles SET library_restrictions_enabled = true WHERE user_id = $1 AND id = $2`,
		// A cluster without an embedding cannot be read, so the cluster build fails.
		`INSERT INTO user_taste_clusters(user_id, profile_id, cluster_idx, embedding, dominant_genres, label, member_count, total_weight) VALUES($1, $2, 0, NULL, '[]', 'Broken', 1, 1)`,
	} {
		if _, err := pool.Exec(ctx, stmt, userID, profile); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	type cacheKey struct{ profile, recType, source string }
	mainKey := cacheKey{profile, RecTypeForYouMain, ""}
	clusterKey := cacheKey{profile, RecTypeForYouClusterPrefix + "0", ""}
	becauseKey := cacheKey{profile, RecTypeBecauseWatched, anchor}
	otherKey := cacheKey{"other", RecTypeForYouMain, ""}
	oldPicks := []ScoredItem{{MediaItemID: prefix + "old-pick", Score: 1}}
	for _, k := range []cacheKey{mainKey, clusterKey, becauseKey, otherKey} {
		if err := repo.UpsertRecommendationCache(ctx, userID, k.profile, k.recType, k.source, oldPicks, cacheExpiry(now)); err != nil {
			t.Fatal(err)
		}
	}
	cached := func(k cacheKey) bool {
		t.Helper()
		return countRows(t, pool, `
			SELECT COUNT(*) FROM recommendation_cache
			WHERE  user_id = $1 AND profile_id = $2 AND rec_type = $3 AND source_item_id = $4`,
			userID, k.profile, k.recType, k.source) == 1
	}

	engine := NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, pgstore.NewPostgresProvider(pool), config.RecommendationsConfig{})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	build := func(wantFailed int) {
		t.Helper()
		if built := w.cacheUserRows(ctx, repo, userID, profile, cacheExpiry(now)); built.failed != wantFailed {
			t.Fatalf("cache build = %+v, want %d failed", built, wantFailed)
		}
	}

	build(1)
	if cached(mainKey) || cached(becauseKey) {
		t.Fatal("a main or Because You Watched row survived an empty rebuild")
	}
	if !cached(clusterKey) {
		t.Fatal("the cluster row was dropped although its build failed")
	}
	if items, err := repo.GetRecommendationCache(ctx, userID, profile, RecTypeSimilarUsersLiked, ""); err != nil || items == nil || len(items) != 0 {
		t.Fatalf("similar users row = %v, %v; want it cached empty", items, err)
	}
	if !cached(otherKey) {
		t.Fatal("another profile's row was dropped")
	}

	// Once the cluster can be read and comes out empty, its row goes too.
	if err := repo.UpsertTasteClusters(ctx, userID, profile, []TasteCluster{{ClusterIdx: 0, Embedding: taste, Label: "Test", MemberCount: 1, TotalWeight: 1}}); err != nil {
		t.Fatal(err)
	}
	build(0)
	if cached(clusterKey) {
		t.Fatal("the cluster row survived an empty rebuild")
	}
}

func TestDeleteGlobalRecommendationCachePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"cache-delete-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	base := "test_delete_" + uuid.NewString() + "_"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM recommendation_cache WHERE starts_with(rec_type, $1)`, base)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	expires := time.Now().Add(cacheTTL).Format(time.RFC3339)
	items := []ScoredItem{{MediaItemID: "item", Score: 1}}
	for _, row := range []struct {
		owner   int
		recType string
	}{
		{GlobalCacheUserID, base + "one"},
		{GlobalCacheUserID, base + "one_more"},
		{GlobalCacheUserID, base + "g_kept"},
		{GlobalCacheUserID, base + "g_dropped"},
		// An account row is not global, even under the same profile ID and type.
		{userID, base + "g_dropped"},
	} {
		if err := repo.UpsertRecommendationCache(ctx, row.owner, GlobalCacheProfileID, row.recType, "", items, expires); err != nil {
			t.Fatal(err)
		}
	}
	remaining := func() []string {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT COALESCE(user_id::text, 'global') || ':' || rec_type FROM recommendation_cache
			WHERE  starts_with(rec_type, $1) ORDER BY 1`, base)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if n, err := repo.DeleteGlobalRecommendationCache(ctx, base+"one", false, nil); err != nil || n != 1 {
		t.Fatalf("exact delete = %d, %v; want 1 row", n, err)
	}
	if n, err := repo.DeleteGlobalRecommendationCache(ctx, base+"g_", true, []string{base + "g_kept"}); err != nil || n != 1 {
		t.Fatalf("prefix delete = %d, %v; want the 1 row off the kept list", n, err)
	}
	want := []string{
		fmt.Sprintf("%d:%sg_dropped", userID, base),
		"global:" + base + "g_kept",
		"global:" + base + "one_more",
	}
	if got := remaining(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("remaining rows = %v, want %v", got, want)
	}
}

package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// externalFixture names a test's sources and titles so they cannot collide
// with another test's, and removes them afterwards.
type externalFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	repo   *Repo
	prefix string
	base   int
}

func newExternalFixture(t *testing.T, base int) *externalFixture {
	t.Helper()
	pool := newEngineTestPool(t)
	f := &externalFixture{t: t, pool: pool, repo: NewRepo(pool), prefix: "test-ext:" + t.Name() + ":", base: base}
	cleanup := func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM external_title_sources WHERE source_key LIKE $1 || '%'`, f.prefix)
		_, _ = pool.Exec(ctx, `DELETE FROM external_titles WHERE tmdb_id BETWEEN $1 AND $1 + 999`, base)
	}
	cleanup()
	t.Cleanup(cleanup)
	return f
}

func (f *externalFixture) source(name string) string { return f.prefix + name }

func (f *externalFixture) movie(n int) ExternalTitleKey {
	return ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: f.base + n}
}

func (f *externalFixture) storeTitles(keys ...ExternalTitleKey) {
	f.t.Helper()
	titles := make([]ExternalTitle, len(keys))
	for i, k := range keys {
		n := k.TMDBID - f.base
		titles[i] = ExternalTitle{Key: k, Title: strconv.Itoa(k.TMDBID), VoteCount: n, IMDbID: "tt" + strconv.Itoa(n), TVDBID: n, DetailFetchedAt: time.Now()}
	}
	if err := f.repo.UpsertExternalTitles(context.Background(), titles); err != nil {
		f.t.Fatal(err)
	}
}

// externalTitlesByKeys returns the stored outside titles among keys. A key
// without a stored title is absent from the map.
func (r *Repo) externalTitlesByKeys(ctx context.Context, keys []ExternalTitleKey) (map[ExternalTitleKey]ExternalTitle, error) {
	out := make(map[ExternalTitleKey]ExternalTitle, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	types, ids := externalKeyArrays(keys)
	rows, err := r.pool.Query(ctx, `
		SELECT t.media_type, t.tmdb_id, t.title, t.overview, t.poster_path, t.backdrop_path,
		       t.release_date, t.original_language, t.popularity, t.vote_average, t.vote_count,
		       t.us_certification, t.detail_fetched_at, t.imdb_id, t.tvdb_id
		FROM   external_titles t
		JOIN   unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
		       ON t.media_type = k.media_type AND t.tmdb_id = k.tmdb_id`, types, ids)
	if err != nil {
		return nil, fmt.Errorf("read outside titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t ExternalTitle
		var fetched *time.Time
		if err := rows.Scan(&t.Key.MediaType, &t.Key.TMDBID, &t.Title, &t.Overview, &t.PosterPath, &t.BackdropPath,
			&t.ReleaseDate, &t.OriginalLanguage, &t.Popularity, &t.VoteAverage, &t.VoteCount,
			&t.USCertification, &fetched, &t.IMDbID, &t.TVDBID); err != nil {
			return nil, fmt.Errorf("scan outside title: %w", err)
		}
		if fetched != nil {
			t.DetailFetchedAt = *fetched
		}
		out[t.Key] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outside titles: %w", err)
	}
	return out, nil
}

// externalRowFor reads a profile's row for one media type, or nil when it
// has none.
func (r *Repo) externalRowFor(ctx context.Context, userID int, profileID, mediaType string) (*ExternalRow, error) {
	row := ExternalRow{UserID: userID, ProfileID: profileID, MediaType: mediaType}
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT items, embedding_model, built_at
		FROM   external_recommendation_rows
		WHERE  user_id = $1 AND profile_id = $2 AND media_type = $3`,
		userID, profileID, mediaType).Scan(&raw, &row.EmbeddingModel, &row.BuiltAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read outside row for user %d profile %s %s: %w", userID, profileID, mediaType, err)
	}
	if err := json.Unmarshal(raw, &row.Items); err != nil {
		return nil, fmt.Errorf("decode outside row for user %d profile %s %s: %w", userID, profileID, mediaType, err)
	}
	return &row, nil
}

func (f *externalFixture) record(name, status string, links ...ExternalLink) {
	f.t.Helper()
	if err := f.repo.RecordExternalSource(context.Background(), ExternalSourceResult{SourceKey: f.source(name), Status: status, Error: "boom", Links: links}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *externalFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *externalFixture) links(name string) []ExternalLink {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT list, rank, media_type, tmdb_id FROM external_title_links WHERE source_key = $1 ORDER BY list, rank`, f.source(name))
	if err != nil {
		f.t.Fatal(err)
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ExternalLink, error) {
		var l ExternalLink
		err := row.Scan(&l.List, &l.Rank, &l.Title.MediaType, &l.Title.TMDBID)
		return l, err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return links
}

// A source is fetched when it never was, when its last fetch failed, or when
// its last result (links or none) is older than the cutoff. An empty result
// inside the cutoff is not fetched again.
func TestExternalSourcesToFetchDB(t *testing.T) {
	f := newExternalFixture(t, 910_000_000)
	ctx := context.Background()
	link := ExternalLink{List: ExternalListRecommendations, Rank: 1, Title: f.movie(1)}
	f.record("ok-fresh", ExternalSourceOK, link)
	f.record("ok-stale", ExternalSourceOK, link)
	f.record("empty-fresh", ExternalSourceEmpty)
	f.record("empty-stale", ExternalSourceEmpty)
	f.record("failed", ExternalSourceOK, link)
	f.record("failed", ExternalSourceFailed)
	f.exec(`UPDATE external_title_sources SET fetched_at = now() - interval '40 days' WHERE source_key = ANY($1)`,
		[]string{f.source("ok-stale"), f.source("empty-stale")})

	names := []string{"never", "ok-fresh", "ok-stale", "empty-fresh", "empty-stale", "failed"}
	keys := make([]string, len(names))
	for i, n := range names {
		keys[i] = f.source(n)
	}
	got, err := f.repo.ExternalSourcesToFetch(ctx, keys, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{f.source("never"), f.source("ok-stale"), f.source("empty-stale"), f.source("failed")}
	if !slices.Equal(got, want) {
		t.Fatalf("to fetch = %v, want %v", got, want)
	}
}

// Recording a source replaces its links whole, even when nodes record the
// same source at once; a failed fetch keeps the links it had.
func TestExternalSourceLinksReplacedDB(t *testing.T) {
	f := newExternalFixture(t, 911_000_000)
	ctx := context.Background()
	setOf := func(first, n int) []ExternalLink {
		out := make([]ExternalLink, n)
		for i := range out {
			out[i] = ExternalLink{List: ExternalListSimilar, Rank: i + 1, Title: f.movie(first + i)}
		}
		return out
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := range writers {
		wg.Go(func() {
			errs <- f.repo.RecordExternalSource(ctx, ExternalSourceResult{
				SourceKey: f.source("seed"), Status: ExternalSourceOK, Links: setOf(w*100, 20+w),
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent record: %v", err)
		}
	}
	got := f.links("seed")
	w := (len(got) - 20)
	if w < 0 || w >= writers || !slices.Equal(got, setOf(w*100, 20+w)) {
		t.Fatalf("links after concurrent records are not one writer's set: %v", got)
	}

	f.record("seed", ExternalSourceFailed)
	if kept := f.links("seed"); !slices.Equal(kept, got) {
		t.Fatalf("failed fetch changed links: %v", kept)
	}
	var status, msg string
	if err := f.pool.QueryRow(ctx, `SELECT status, error FROM external_title_sources WHERE source_key = $1`, f.source("seed")).Scan(&status, &msg); err != nil {
		t.Fatal(err)
	}
	if status != ExternalSourceFailed || msg != "boom" {
		t.Fatalf("status = %q %q", status, msg)
	}

	f.record("seed", ExternalSourceEmpty)
	if left := f.links("seed"); len(left) != 0 {
		t.Fatalf("empty fetch left links: %v", left)
	}
}

// The pool keeps the titles named by the most distinct seeds up to the cap,
// adds every set title, leaves out titles without a stored row, and gives
// each title its seeds by best rank.
func TestExternalPoolDB(t *testing.T) {
	f := newExternalFixture(t, 912_000_000)
	ctx := context.Background()
	rec := func(rank, n int) ExternalLink {
		return ExternalLink{List: ExternalListRecommendations, Rank: rank, Title: f.movie(n)}
	}
	sim := func(rank, n int) ExternalLink {
		return ExternalLink{List: ExternalListSimilar, Rank: rank, Title: f.movie(n)}
	}
	set := func(rank, n int) ExternalLink {
		return ExternalLink{List: ExternalListSet, Rank: rank, Title: f.movie(n)}
	}
	// 1: two seeds. 2: both of s1's lists, so one seed. 3: one seed at rank 1.
	// 4: three seeds but not stored. 7: one seed at rank 9, cut by the cap.
	// 2 and 5 are in the set; 6 is in the set but not stored.
	f.record("s1", ExternalSourceOK, rec(1, 1), rec(2, 2), sim(2, 2), sim(3, 1), rec(4, 4))
	f.record("s2", ExternalSourceOK, rec(5, 1), rec(6, 4), rec(9, 7))
	f.record("s3", ExternalSourceOK, rec(1, 3), rec(7, 4))
	f.record("set", ExternalSourceOK, set(1, 5), set(2, 2), set(3, 6))
	f.storeTitles(f.movie(1), f.movie(2), f.movie(3), f.movie(5), f.movie(7))

	seeds := []string{f.source("s1"), f.source("s2"), f.source("s3")}
	got, err := f.repo.ExternalPool(ctx, ExternalPoolQuery{SeedSources: seeds, SetSources: []string{f.source("set")}, Cap: 2})
	if err != nil {
		t.Fatal(err)
	}
	seed := func(name string, rank int) ExternalPoolSeed {
		return ExternalPoolSeed{SourceKey: f.source(name), BestRank: rank}
	}
	want := []ExternalPoolTitle{
		{Key: f.movie(1), Seeds: []ExternalPoolSeed{seed("s1", 1), seed("s2", 5)}},
		{Key: f.movie(3), Seeds: []ExternalPoolSeed{seed("s3", 1)}},
		{Key: f.movie(2), Seeds: []ExternalPoolSeed{seed("s1", 2)}, InSet: true},
		{Key: f.movie(5), Seeds: []ExternalPoolSeed{}, InSet: true},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pool =\n%v\nwant\n%v", got, want)
	}

	all, err := f.repo.ExternalPool(ctx, ExternalPoolQuery{SeedSources: seeds, SetSources: []string{f.source("set")}, IncludeUnstored: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, p := range all {
		ids = append(ids, p.Key.TMDBID-f.base)
	}
	if !slices.Equal(ids, []int{4, 1, 3, 2, 7, 5, 6}) {
		t.Fatalf("uncapped pool with unstored titles = %v", ids)
	}
}

// Similarity is the cosine to the taste vector over the titles with a
// vector for the model, most similar first.
func TestExternalSimilaritiesDB(t *testing.T) {
	f := newExternalFixture(t, 913_000_000)
	ctx := context.Background()
	series := ExternalTitleKey{MediaType: ExternalMediaSeries, TMDBID: f.base + 5}
	f.storeTitles(f.movie(1), f.movie(2), f.movie(3), f.movie(4), series)
	const model = "test-model"
	if err := f.repo.UpsertExternalEmbeddings(ctx, []ExternalEmbedding{
		{Key: f.movie(1), Vector: []float32{0, 0, 1}, Model: model},
		{Key: f.movie(2), Vector: []float32{1, 0, 0}, Model: model},
		{Key: f.movie(3), Vector: []float32{0.6, 0.8, 0}, Model: model},
		{Key: f.movie(4), Vector: []float32{1, 0, 0}, Model: "other-model"},
		{Key: series, Vector: []float32{1, 0, 0}, Model: model},
		// Never stored as a title: skipped, not an error.
		{Key: f.movie(9), Vector: []float32{1, 0, 0}, Model: model},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := f.repo.ExternalSimilarities(ctx, []float32{1, 0, 0}, model, ExternalMediaMovie,
		[]int{f.base + 1, f.base + 2, f.base + 3, f.base + 4, f.base + 5, f.base + 9})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		n   int
		sim float64
	}{{2, 1}, {3, 0.6}, {1, 0}}
	if len(got) != len(want) {
		t.Fatalf("similarities = %v", got)
	}
	for i, w := range want {
		if got[i].Key != f.movie(w.n) || math.Abs(got[i].Similarity-w.sim) > 0.01 || got[i].VoteCount != w.n {
			t.Fatalf("similarity %d = %+v, want movie %d at %v", i, got[i], w.n, w.sim)
		}
	}
}

// Retention deletes titles whose detail is too old or that nothing has
// referenced, and unreferenced sources with their links; the rest stays.
func TestExternalRetentionDB(t *testing.T) {
	f := newExternalFixture(t, 914_000_000)
	ctx := context.Background()
	f.storeTitles(f.movie(1), f.movie(2), f.movie(3))
	f.exec(`UPDATE external_titles SET detail_fetched_at = now() - interval '200 days' WHERE tmdb_id = $1`, f.base+1)
	f.exec(`UPDATE external_titles SET last_referenced_at = now() - interval '40 days' WHERE tmdb_id = $1`, f.base+2)
	link := func(n int) ExternalLink { return ExternalLink{List: ExternalListSet, Rank: n, Title: f.movie(n)} }
	f.record("old", ExternalSourceOK, link(1), link(2))
	f.record("kept", ExternalSourceOK, link(3))
	f.exec(`UPDATE external_title_sources SET last_referenced_at = now() - interval '40 days' WHERE source_key = $1`, f.source("old"))

	// Touching a referenced title keeps it even though its row is old.
	f.exec(`UPDATE external_titles SET last_referenced_at = now() - interval '40 days' WHERE tmdb_id = $1`, f.base+3)
	if err := f.repo.TouchExternalReferences(ctx, nil, []ExternalTitleKey{f.movie(3)}); err != nil {
		t.Fatal(err)
	}

	res, err := f.repo.PruneExternalTitles(ctx, time.Now().Add(-150*24*time.Hour), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Titles < 2 || res.Sources < 1 || res.Links < 2 {
		t.Fatalf("pruned %+v, want at least 2 titles, 1 source, 2 links", res)
	}
	stored, err := f.repo.externalTitlesByKeys(ctx, []ExternalTitleKey{f.movie(1), f.movie(2), f.movie(3)})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[f.movie(3)].Title == "" || stored[f.movie(3)].IMDbID != "tt3" || stored[f.movie(3)].TVDBID != 3 {
		t.Fatalf("titles left = %v, want only movie 3", stored)
	}
	if len(f.links("old")) != 0 || len(f.links("kept")) != 1 {
		t.Fatalf("links left: old %v kept %v", f.links("old"), f.links("kept"))
	}
}

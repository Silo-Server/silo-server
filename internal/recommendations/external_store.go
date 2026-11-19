package recommendations

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// Outside titles are movies and series the library does not have, keyed by
// TMDB ID. This file is their Postgres store: the sources a pool is built
// from and the titles those name (external_title_sources/_links), the titles'
// card fields and vectors (external_titles/_embeddings), and each profile's
// ranked rows (external_recommendation_rows).

// Outside title media types: Silo's names, not TMDB's ("tv").
const (
	ExternalMediaMovie  = "movie"
	ExternalMediaSeries = "series"
)

// Lists a source's links belong to. A seed source has recommendations and
// similar; a set source has set.
const (
	ExternalListRecommendations = "recommendations"
	ExternalListSimilar         = "similar"
	ExternalListSet             = "set"
)

// Results of a source fetch.
const (
	ExternalSourceOK     = "ok"
	ExternalSourceEmpty  = "empty"
	ExternalSourceFailed = "failed"
)

// ExternalTitleKey identifies an outside title. MediaType is
// ExternalMediaMovie or ExternalMediaSeries.
type ExternalTitleKey struct {
	MediaType string
	TMDBID    int
}

// ExternalLink is one title a source names, at a 1-based rank in one list.
type ExternalLink struct {
	List  string
	Rank  int
	Title ExternalTitleKey
}

// ExternalSourceResult is the outcome of fetching one source. Links are read
// only for ExternalSourceOK; an OK result without links is stored as empty.
type ExternalSourceResult struct {
	SourceKey string
	Status    string
	Error     string
	Links     []ExternalLink
}

// ExternalTitle is an outside title's stored card data.
type ExternalTitle struct {
	Key ExternalTitleKey
	// IMDbID and TVDBID are the title's other IDs from its TMDB detail, ""
	// and 0 when TMDB has none. A library item matched by one of them is
	// the same title.
	IMDbID           string
	TVDBID           int
	Title            string
	Overview         string
	PosterPath       string
	BackdropPath     string
	ReleaseDate      string
	OriginalLanguage string
	Popularity       float64
	VoteAverage      float64
	VoteCount        int
	USCertification  string
	DetailFetchedAt  time.Time
}

// ExternalEmbedding is a vector to store for an outside title. Vector is the
// model's output; it is zero-padded to the canonical width on write.
type ExternalEmbedding struct {
	Key           ExternalTitleKey
	Vector        []float32
	Model         string
	CanonicalText string
}

// ExternalPoolQuery selects a profile's candidate pool.
type ExternalPoolQuery struct {
	// SeedSources are the profile's seed source keys; their recommendations
	// and similar links name candidates.
	SeedSources []string
	// SetSources are the set source keys the profile uses; every title they
	// name is a candidate.
	SetSources []string
	// Cap keeps the Cap seed-list titles named by the most distinct seeds.
	// Cap <= 0 keeps them all.
	Cap int
	// IncludeUnstored also returns titles that have no external_titles row
	// (no detail yet, or owned by the library), so a pool builder can decide
	// what to fetch. Ranking leaves it false.
	IncludeUnstored bool
}

// ExternalPoolSeed is a seed that named a candidate, at its best rank across
// the seed's lists.
type ExternalPoolSeed struct {
	SourceKey string
	BestRank  int
}

// ExternalPoolTitle is a candidate in a profile's pool.
type ExternalPoolTitle struct {
	Key ExternalTitleKey
	// Seeds named the title, best rank first.
	Seeds []ExternalPoolSeed
	// InSet reports that one of the query's set sources names the title.
	InSet bool
}

// ExternalSimilarity is a candidate's cosine similarity to a taste vector.
type ExternalSimilarity struct {
	Key        ExternalTitleKey
	Similarity float64
	VoteCount  int
}

// ExternalRowItem is one ranked title in a profile's row. Reasons are the
// library content IDs of the seeds that named it.
type ExternalRowItem struct {
	TMDBID  int      `json:"tmdb_id"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
}

// ExternalRow is a profile's ranked outside titles of one media type, built
// under EmbeddingModel.
type ExternalRow struct {
	UserID         int
	ProfileID      string
	MediaType      string
	Items          []ExternalRowItem
	EmbeddingModel string
	BuiltAt        time.Time
}

// ExternalRetentionResult counts what PruneExternalTitles deleted.
type ExternalRetentionResult struct {
	Titles  int64
	Sources int64
	Links   int64
}

// ExternalReset counts what resetExternalEmbeddings deleted.
type ExternalReset struct {
	Embeddings int64
	Rows       int64
}

func externalKeyArrays(keys []ExternalTitleKey) ([]string, []int) {
	types := make([]string, len(keys))
	ids := make([]int, len(keys))
	for i, k := range keys {
		types[i], ids[i] = k.MediaType, k.TMDBID
	}
	return types, ids
}

func compareExternalKeys(a, b ExternalTitleKey) int {
	return cmp.Or(cmp.Compare(a.MediaType, b.MediaType), cmp.Compare(a.TMDBID, b.TMDBID))
}

// ExternalSourcesToFetch returns the source keys that need a fetch: never
// fetched, last fetch failed, or last fetched (with links or empty) before
// staleBefore. They come in input order, those whose last fetch failed last,
// so sources that keep failing cannot use up a run's requests ahead of ones
// that have not been tried.
func (r *Repo) ExternalSourcesToFetch(ctx context.Context, sourceKeys []string, staleBefore time.Time) ([]string, error) {
	if len(sourceKeys) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT k.source_key
		FROM   unnest($1::text[]) WITH ORDINALITY AS k(source_key, ord)
		LEFT   JOIN external_title_sources s ON s.source_key = k.source_key
		WHERE  s.source_key IS NULL
		   OR  s.status = 'failed'
		   OR  s.fetched_at IS NULL
		   OR  s.fetched_at < $2
		ORDER  BY COALESCE(s.status = 'failed', false), k.ord`, sourceKeys, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("list outside sources to fetch: %w", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list outside sources to fetch: %w", err)
	}
	return keys, nil
}

// RecordExternalSource stores a source fetch. An OK or empty result replaces
// the source's links in one transaction and marks it fetched now. A failed
// result records the error and keeps the source's previous links and fetch
// time, so the pool keeps using them until a fetch succeeds.
func (r *Repo) RecordExternalSource(ctx context.Context, res ExternalSourceResult) error {
	status := res.Status
	if status == ExternalSourceOK && len(res.Links) == 0 {
		status = ExternalSourceEmpty
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin outside source %s: %w", res.SourceKey, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	switch status {
	case ExternalSourceFailed:
		if _, err := tx.Exec(ctx, `
			INSERT INTO external_title_sources (source_key, status, error)
			VALUES ($1, 'failed', $2)
			ON CONFLICT (source_key) DO UPDATE
				SET status = 'failed', error = EXCLUDED.error, last_referenced_at = now()`,
			res.SourceKey, res.Error); err != nil {
			return fmt.Errorf("record failed outside source %s: %w", res.SourceKey, err)
		}
	case ExternalSourceOK, ExternalSourceEmpty:
		// The upsert locks the source row, so a concurrent record of the
		// same source waits here and its delete then sees these links.
		if _, err := tx.Exec(ctx, `
			INSERT INTO external_title_sources (source_key, status, fetched_at)
			VALUES ($1, $2, now())
			ON CONFLICT (source_key) DO UPDATE
				SET status = EXCLUDED.status, error = '', fetched_at = now(), last_referenced_at = now()`,
			res.SourceKey, status); err != nil {
			return fmt.Errorf("record outside source %s: %w", res.SourceKey, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM external_title_links WHERE source_key = $1`, res.SourceKey); err != nil {
			return fmt.Errorf("replace outside source %s links: %w", res.SourceKey, err)
		}
		if status == ExternalSourceOK {
			lists := make([]string, len(res.Links))
			ranks := make([]int, len(res.Links))
			titles := make([]ExternalTitleKey, len(res.Links))
			for i, l := range res.Links {
				lists[i], ranks[i], titles[i] = l.List, l.Rank, l.Title
			}
			types, ids := externalKeyArrays(titles)
			if _, err := tx.Exec(ctx, `
				INSERT INTO external_title_links (source_key, list, rank, media_type, tmdb_id)
				SELECT $1, l.list, l.rank, l.media_type, l.tmdb_id
				FROM   unnest($2::text[], $3::int[], $4::text[], $5::int[]) AS l(list, rank, media_type, tmdb_id)`,
				res.SourceKey, lists, ranks, types, ids); err != nil {
				return fmt.Errorf("insert outside source %s links: %w", res.SourceKey, err)
			}
		}
	default:
		return fmt.Errorf("record outside source %s: unknown status %q", res.SourceKey, res.Status)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit outside source %s: %w", res.SourceKey, err)
	}
	return nil
}

// TouchExternalReferences marks the sources and titles a run still uses as
// referenced now, so retention keeps them.
func (r *Repo) TouchExternalReferences(ctx context.Context, sourceKeys []string, titles []ExternalTitleKey) error {
	if len(sourceKeys) > 0 {
		sorted := slices.Sorted(slices.Values(sourceKeys))
		if _, err := r.pool.Exec(ctx, `
			UPDATE external_title_sources SET last_referenced_at = now()
			WHERE  source_key IN (SELECT unnest($1::text[]))`, sorted); err != nil {
			return fmt.Errorf("touch outside sources: %w", err)
		}
	}
	if len(titles) > 0 {
		types, ids := externalKeyArrays(slices.SortedFunc(slices.Values(titles), compareExternalKeys))
		if _, err := r.pool.Exec(ctx, `
			UPDATE external_titles t SET last_referenced_at = now()
			FROM   unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
			WHERE  t.media_type = k.media_type AND t.tmdb_id = k.tmdb_id`, types, ids); err != nil {
			return fmt.Errorf("touch outside titles: %w", err)
		}
	}
	return nil
}

// UpsertExternalTitles stores outside titles from their detail fetch and
// marks them referenced now. Titles are written in key order, so concurrent
// writers on other nodes lock rows in the same order.
func (r *Repo) UpsertExternalTitles(ctx context.Context, titles []ExternalTitle) error {
	if len(titles) == 0 {
		return nil
	}
	sorted := slices.SortedFunc(slices.Values(titles), func(a, b ExternalTitle) int {
		return compareExternalKeys(a.Key, b.Key)
	})
	batch := &pgx.Batch{}
	for _, t := range sorted {
		batch.Queue(`
			INSERT INTO external_titles (media_type, tmdb_id, title, overview, poster_path, backdrop_path,
			                             release_date, original_language, popularity, vote_average, vote_count,
			                             us_certification, detail_fetched_at, imdb_id, tvdb_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (media_type, tmdb_id) DO UPDATE
				SET title = EXCLUDED.title, overview = EXCLUDED.overview,
				    imdb_id = EXCLUDED.imdb_id, tvdb_id = EXCLUDED.tvdb_id,
				    poster_path = EXCLUDED.poster_path, backdrop_path = EXCLUDED.backdrop_path,
				    release_date = EXCLUDED.release_date, original_language = EXCLUDED.original_language,
				    popularity = EXCLUDED.popularity, vote_average = EXCLUDED.vote_average,
				    vote_count = EXCLUDED.vote_count, us_certification = EXCLUDED.us_certification,
				    detail_fetched_at = EXCLUDED.detail_fetched_at,
				    last_referenced_at = now(), updated_at = now()`,
			t.Key.MediaType, t.Key.TMDBID, t.Title, t.Overview, t.PosterPath, t.BackdropPath,
			t.ReleaseDate, t.OriginalLanguage, t.Popularity, t.VoteAverage, t.VoteCount,
			t.USCertification, t.DetailFetchedAt, t.IMDbID, max(t.TVDBID, 0))
	}
	if err := r.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("upsert outside titles: %w", err)
	}
	return nil
}

// ExternalTitlesNeedingDetail returns, in input order, the keys that have no
// stored title or whose detail was fetched before staleBefore.
func (r *Repo) ExternalTitlesNeedingDetail(ctx context.Context, keys []ExternalTitleKey, staleBefore time.Time) ([]ExternalTitleKey, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	types, ids := externalKeyArrays(keys)
	rows, err := r.pool.Query(ctx, `
		SELECT k.media_type, k.tmdb_id
		FROM   unnest($1::text[], $2::int[]) WITH ORDINALITY AS k(media_type, tmdb_id, ord)
		LEFT   JOIN external_titles t ON t.media_type = k.media_type AND t.tmdb_id = k.tmdb_id
		WHERE  t.tmdb_id IS NULL OR t.detail_fetched_at IS NULL OR t.detail_fetched_at < $3
		ORDER  BY k.ord`, types, ids, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("list outside titles needing detail: %w", err)
	}
	keys, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ExternalTitleKey, error) {
		var k ExternalTitleKey
		err := row.Scan(&k.MediaType, &k.TMDBID)
		return k, err
	})
	if err != nil {
		return nil, fmt.Errorf("list outside titles needing detail: %w", err)
	}
	return keys, nil
}

// ExternalEmbeddingTexts returns the canonical text stored with each key's
// vector for model. A key with no vector for model is absent, so a caller
// embeds a title whose text is missing or differs from the one it builds.
func (r *Repo) ExternalEmbeddingTexts(ctx context.Context, keys []ExternalTitleKey, model string) (map[ExternalTitleKey]string, error) {
	out := make(map[ExternalTitleKey]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	types, ids := externalKeyArrays(keys)
	rows, err := r.pool.Query(ctx, `
		SELECT e.media_type, e.tmdb_id, e.canonical_text
		FROM   external_title_embeddings e
		JOIN   unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
		       ON e.media_type = k.media_type AND e.tmdb_id = k.tmdb_id
		WHERE  e.model = $3`, types, ids, model)
	if err != nil {
		return nil, fmt.Errorf("read outside embedding texts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k ExternalTitleKey
		var text string
		if err := rows.Scan(&k.MediaType, &k.TMDBID, &text); err != nil {
			return nil, fmt.Errorf("scan outside embedding text: %w", err)
		}
		out[k] = text
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outside embedding texts: %w", err)
	}
	return out, nil
}

// UpsertExternalEmbeddings stores vectors for outside titles. A title deleted
// since it was read (retention, the server cap) is skipped rather than failing
// the batch.
func (r *Repo) UpsertExternalEmbeddings(ctx context.Context, embeddings []ExternalEmbedding) error {
	if len(embeddings) == 0 {
		return nil
	}
	sorted := slices.SortedFunc(slices.Values(embeddings), func(a, b ExternalEmbedding) int {
		return compareExternalKeys(a.Key, b.Key)
	})
	batch := &pgx.Batch{}
	for _, e := range sorted {
		padded, err := ensureCanonicalDimensions(e.Vector)
		if err != nil {
			return fmt.Errorf("upsert outside embedding %s %d: %w", e.Key.MediaType, e.Key.TMDBID, err)
		}
		batch.Queue(`
			INSERT INTO external_title_embeddings (media_type, tmdb_id, embedding, model, canonical_text)
			SELECT t.media_type, t.tmdb_id, $3, $4, $5
			FROM   external_titles t
			WHERE  t.media_type = $1 AND t.tmdb_id = $2
			ON CONFLICT (media_type, tmdb_id) DO UPDATE
				SET embedding = EXCLUDED.embedding, model = EXCLUDED.model,
				    canonical_text = EXCLUDED.canonical_text, updated_at = now()`,
			e.Key.MediaType, e.Key.TMDBID, pgvector.NewVector(padded), e.Model, e.CanonicalText)
	}
	if err := r.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("upsert outside embeddings: %w", err)
	}
	return nil
}

// ExternalPool returns a profile's candidate titles: the q.Cap seed-list
// titles named by the most distinct seeds (a title in both of one seed's
// lists counts once; ties go to the better best rank), plus every title the
// set sources name. Each carries the seeds that named it, best rank first.
// Titles come most-named first.
func (r *Repo) ExternalPool(ctx context.Context, q ExternalPoolQuery) ([]ExternalPoolTitle, error) {
	if len(q.SeedSources) == 0 && len(q.SetSources) == 0 {
		return nil, nil
	}
	var limit *int
	if q.Cap > 0 {
		limit = &q.Cap
	}
	seedSources, setSources := q.SeedSources, q.SetSources
	if seedSources == nil {
		seedSources = []string{}
	}
	if setSources == nil {
		setSources = []string{}
	}
	rows, err := r.pool.Query(ctx, `
		WITH links AS (
			SELECT l.source_key, l.list, l.rank, l.media_type, l.tmdb_id
			FROM   external_title_links l
			WHERE  (l.source_key = ANY($1) OR l.source_key = ANY($2))
			  AND  ($4 OR EXISTS (SELECT 1 FROM external_titles t
			                      WHERE t.media_type = l.media_type AND t.tmdb_id = l.tmdb_id))
		), seed_links AS (
			SELECT source_key, media_type, tmdb_id, MIN(rank) AS best_rank
			FROM   links
			WHERE  source_key = ANY($1) AND list <> 'set'
			GROUP  BY source_key, media_type, tmdb_id
		), seed_titles AS (
			SELECT media_type, tmdb_id
			FROM   seed_links
			GROUP  BY media_type, tmdb_id
			ORDER  BY COUNT(*) DESC, MIN(best_rank), media_type, tmdb_id
			LIMIT  $3
		), set_titles AS (
			SELECT DISTINCT media_type, tmdb_id
			FROM   links
			WHERE  source_key = ANY($2)
		), pool AS (
			SELECT media_type, tmdb_id FROM seed_titles
			UNION
			SELECT media_type, tmdb_id FROM set_titles
		)
		SELECT p.media_type, p.tmdb_id,
		       EXISTS (SELECT 1 FROM set_titles s WHERE s.media_type = p.media_type AND s.tmdb_id = p.tmdb_id),
		       COALESCE(array_agg(sl.source_key ORDER BY sl.best_rank, sl.source_key)
		                FILTER (WHERE sl.source_key IS NOT NULL), '{}'),
		       COALESCE(array_agg(sl.best_rank ORDER BY sl.best_rank, sl.source_key)
		                FILTER (WHERE sl.source_key IS NOT NULL), '{}')
		FROM   pool p
		LEFT   JOIN seed_links sl ON sl.media_type = p.media_type AND sl.tmdb_id = p.tmdb_id
		GROUP  BY p.media_type, p.tmdb_id
		ORDER  BY COUNT(sl.source_key) DESC, MIN(sl.best_rank) NULLS LAST, p.media_type, p.tmdb_id`,
		seedSources, setSources, limit, q.IncludeUnstored)
	if err != nil {
		return nil, fmt.Errorf("read outside pool: %w", err)
	}
	defer rows.Close()
	var out []ExternalPoolTitle
	for rows.Next() {
		var t ExternalPoolTitle
		var sources []string
		var ranks []int
		if err := rows.Scan(&t.Key.MediaType, &t.Key.TMDBID, &t.InSet, &sources, &ranks); err != nil {
			return nil, fmt.Errorf("scan outside pool title: %w", err)
		}
		t.Seeds = make([]ExternalPoolSeed, len(sources))
		for i := range sources {
			t.Seeds[i] = ExternalPoolSeed{SourceKey: sources[i], BestRank: ranks[i]}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outside pool: %w", err)
	}
	return out, nil
}

// ExternalSimilarities returns, most similar first, the cosine similarity of
// taste to each of tmdbIDs (all of mediaType) that has a vector for model,
// with its vote count. It is an exact scan over the given titles.
func (r *Repo) ExternalSimilarities(ctx context.Context, taste []float32, model, mediaType string, tmdbIDs []int) ([]ExternalSimilarity, error) {
	if len(taste) == 0 || len(tmdbIDs) == 0 {
		return nil, nil
	}
	padded, err := ensureCanonicalDimensions(taste)
	if err != nil {
		return nil, fmt.Errorf("outside similarities: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT e.tmdb_id,
		       1 - (e.embedding::halfvec(3072) <=> $1::halfvec(3072)) AS similarity,
		       t.vote_count
		FROM   external_title_embeddings e
		JOIN   external_titles t ON t.media_type = e.media_type AND t.tmdb_id = e.tmdb_id
		WHERE  e.media_type = $2 AND e.tmdb_id = ANY($3::int[]) AND e.model = $4
		ORDER  BY similarity DESC, e.tmdb_id`,
		pgvector.NewHalfVector(padded), mediaType, tmdbIDs, model)
	if err != nil {
		return nil, fmt.Errorf("outside similarities: %w", err)
	}
	defer rows.Close()
	var out []ExternalSimilarity
	for rows.Next() {
		s := ExternalSimilarity{Key: ExternalTitleKey{MediaType: mediaType}}
		if err := rows.Scan(&s.Key.TMDBID, &s.Similarity, &s.VoteCount); err != nil {
			return nil, fmt.Errorf("scan outside similarity: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outside similarities: %w", err)
	}
	return out, nil
}

// DeleteExternalRows deletes a profile's rows of every media type.
func (r *Repo) DeleteExternalRows(ctx context.Context, userID int, profileID string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM external_recommendation_rows WHERE user_id = $1 AND profile_id = $2`, userID, profileID)
	if err != nil {
		return 0, fmt.Errorf("delete outside rows for user %d profile %s: %w", userID, profileID, err)
	}
	return tag.RowsAffected(), nil
}

// CountExternalTitles counts the stored outside titles.
func (r *Repo) CountExternalTitles(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM external_titles`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count outside titles: %w", err)
	}
	return n, nil
}

// DeleteExternalTitles deletes the given outside titles and their vectors.
// Links naming them stay: a link may name a title without a stored row.
func (r *Repo) DeleteExternalTitles(ctx context.Context, keys []ExternalTitleKey) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	types, ids := externalKeyArrays(slices.SortedFunc(slices.Values(keys), compareExternalKeys))
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM external_titles t
		USING  unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
		WHERE  t.media_type = k.media_type AND t.tmdb_id = k.tmdb_id`, types, ids)
	if err != nil {
		return 0, fmt.Errorf("delete outside titles: %w", err)
	}
	return tag.RowsAffected(), nil
}

// deleteExternalEmbeddings deletes the vectors of the given titles and keeps
// the titles.
func (r *Repo) deleteExternalEmbeddings(ctx context.Context, keys []ExternalTitleKey) error {
	if len(keys) == 0 {
		return nil
	}
	types, ids := externalKeyArrays(slices.SortedFunc(slices.Values(keys), compareExternalKeys))
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM external_title_embeddings e
		USING  unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
		WHERE  e.media_type = k.media_type AND e.tmdb_id = k.tmdb_id`, types, ids); err != nil {
		return fmt.Errorf("delete outside embeddings: %w", err)
	}
	return nil
}

// PruneExternalTitles applies retention: it deletes titles whose detail was
// fetched before detailBefore, and titles and sources nothing has referenced
// since unreferencedBefore, with the deleted sources' links. Each delete is
// its own statement; a title or source a run touches concurrently is either
// kept or fetched again later.
func (r *Repo) PruneExternalTitles(ctx context.Context, detailBefore, unreferencedBefore time.Time) (ExternalRetentionResult, error) {
	var res ExternalRetentionResult
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM external_titles
		WHERE  detail_fetched_at IS NULL
		   OR  detail_fetched_at < $1
		   OR  last_referenced_at < $2`, detailBefore, unreferencedBefore)
	if err != nil {
		return res, fmt.Errorf("prune outside titles: %w", err)
	}
	res.Titles = tag.RowsAffected()

	// The links go with their source by the foreign key's cascade; the
	// count reads them from the statement's snapshot, before the delete.
	var sources, links int64
	if err := r.pool.QueryRow(ctx, `
		WITH gone AS (
			DELETE FROM external_title_sources WHERE last_referenced_at < $1 RETURNING source_key
		)
		SELECT (SELECT count(*) FROM gone),
		       (SELECT count(*) FROM external_title_links l WHERE l.source_key IN (SELECT source_key FROM gone))`,
		unreferencedBefore).Scan(&sources, &links); err != nil {
		return res, fmt.Errorf("prune outside sources: %w", err)
	}
	res.Sources, res.Links = sources, links
	return res, nil
}

// resetExternalEmbeddings deletes every outside vector and every profile's
// outside rows inside the embeddings reset's transaction. Titles, sources
// and links do not depend on the model and stay.
func resetExternalEmbeddings(ctx context.Context, tx pgx.Tx) (ExternalReset, error) {
	var res ExternalReset
	tag, err := tx.Exec(ctx, `DELETE FROM external_title_embeddings`)
	if err != nil {
		return res, fmt.Errorf("reset outside embeddings: %w", err)
	}
	res.Embeddings = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM external_recommendation_rows`)
	if err != nil {
		return res, fmt.Errorf("reset outside rows: %w", err)
	}
	res.Rows = tag.RowsAffected()
	return res, nil
}

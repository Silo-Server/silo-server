package recommendations

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

func ensureCanonicalDimensions(vec []float32) ([]float32, error) {
	return embeddingvectors.EnsureCanonicalDimensions(vec)
}

const embeddingLockSettingKey = "recommendations.embedding_lock"
const minHNSWEfSearch = 200

// watchedActivityCTE unifies video watch progress and ebook reader progress
// into one activity stream. Episodes roll up to their parent series via
// COALESCE(e.series_id, ...). The ebook completed flag is derived from the
// shared finished-progress threshold.
var watchedActivityCTE = fmt.Sprintf(`
watched_activity AS (
	SELECT COALESCE(e.series_id, wp.media_item_id) AS item_id,
	       wp.media_item_id AS leaf_item_id,
	       wp.user_id,
	       wp.profile_id,
	       wp.user_id::text || ':' || COALESCE(wp.profile_id, '') AS watcher_id,
	       wp.updated_at,
	       wp.completed AS completed
	FROM   user_watch_progress wp
	LEFT JOIN episodes e ON e.content_id = wp.media_item_id
	WHERE  (wp.completed = true OR (wp.duration_seconds > 0 AND wp.position_seconds / wp.duration_seconds >= 0.5))
	  AND  NOT EXISTS (
		SELECT 1
		FROM   user_history_hidden_items hhi
		WHERE  hhi.user_id = wp.user_id
		  AND  hhi.profile_id = wp.profile_id
		  AND  hhi.media_item_id = wp.media_item_id
		  AND  wp.updated_at <= hhi.hidden_before
	  )
	UNION ALL
	SELECT erp.content_id AS item_id,
	       erp.content_id AS leaf_item_id,
	       erp.user_id,
	       erp.profile_id,
	       erp.user_id::text || ':' || COALESCE(erp.profile_id, '') AS watcher_id,
	       erp.updated_at,
	       erp.progress >= %s AS completed
	FROM   ebook_reader_progress erp
	WHERE  erp.progress >= 0.5
	  AND  NOT EXISTS (
		SELECT 1
		FROM   user_history_hidden_items hhi
		WHERE  hhi.user_id = erp.user_id
		  AND  hhi.profile_id = erp.profile_id
		  AND  hhi.media_item_id = erp.content_id
		  AND  erp.updated_at <= hhi.hidden_before
	  )
)`, catalog.EbookFinishedProgressThresholdSQL)

var tasteSeedCandidateQuery = fmt.Sprintf(`
			WITH %s,
			watched_counts AS (
				SELECT item_id, COUNT(DISTINCT watcher_id) AS watch_count
				FROM   watched_activity
				WHERE  updated_at > NOW() - INTERVAL '180 days'
				GROUP  BY item_id
			)
			SELECT mi.content_id
			FROM   media_items mi
			LEFT JOIN watched_counts wc ON wc.item_id = mi.content_id
			WHERE  %s
			  AND  mi.type IN ('movie', 'series', 'audiobook', 'ebook')
			  AND  mi.poster_path IS NOT NULL
			  AND  mi.poster_path <> ''
			ORDER  BY COALESCE(wc.watch_count, 0) DESC,
			          CASE
			            WHEN mi.rating_imdb IS NOT NULL THEN 2
			            WHEN mi.rating_tmdb IS NOT NULL AND mi.rating_tmdb < 9.5 THEN 1
			            ELSE 0
			          END DESC,
			          mi.rating_imdb DESC NULLS LAST,
			          CASE WHEN mi.rating_tmdb < 9.5 THEN mi.rating_tmdb END DESC NULLS LAST,
			          mi.year DESC NULLS LAST,
			          mi.content_id ASC
			LIMIT  $1 OFFSET $2`, watchedActivityCTE, recommendationItemEligibilityWhereClause("mi"))

// Repo provides database operations for the recommendation system.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new Repo with the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func hnswEfSearch(candidateLimit int) int {
	return max(candidateLimit, minHNSWEfSearch)
}

func (r *Repo) withHNSWCandidateScan(ctx context.Context, candidateLimit int, fn func(pgx.Tx) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin hnsw candidate scan tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		SELECT set_config('hnsw.iterative_scan', 'relaxed_order', true),
		       set_config('hnsw.ef_search', $1, true)
	`, fmt.Sprintf("%d", hnswEfSearch(candidateLimit)))
	if err != nil {
		return fmt.Errorf("configure hnsw candidate scan: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit hnsw candidate scan tx: %w", err)
	}
	return nil
}

// UserSimilarity represents a user with a similarity score relative to the
// requesting user's taste profile.
type UserSimilarity struct {
	UserID    int
	ProfileID string
	Score     float64
}

type EmbeddingTextCandidate struct {
	MediaItemID   string
	Model         string
	CanonicalText string
}

func embeddingEligibilityWhereClause() string {
	return recommendationItemEligibilityWhereClause("mi")
}

func recommendationItemEligibilityWhereClause(alias string) string {
	// Delegate to the shared embed-eligibility predicate so the recommendations
	// population stays identical to the catalog coverage denominator.
	return embeddingvectors.ItemEligibilityWhereClause(alias)
}

// UpsertEmbedding stores or updates an embedding for a media item.
func (r *Repo) UpsertEmbedding(ctx context.Context, itemID string, embedding []float32, model, canonicalText string) error {
	padded, err := ensureCanonicalDimensions(embedding)
	if err != nil {
		return fmt.Errorf("upsert embedding for item %s: %w", itemID, err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin upsert embedding for item %s: %w", itemID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (media_item_id) DO UPDATE
			SET embedding      = EXCLUDED.embedding,
			    model          = EXCLUDED.model,
			    canonical_text = EXCLUDED.canonical_text,
			    updated_at     = NOW()
	`, itemID, pgvector.NewVector(padded), model, canonicalText)
	if err != nil {
		return fmt.Errorf("upsert embedding for item %s: %w", itemID, err)
	}
	if err := catalog.EnqueueSearchIndexUpsert(ctx, tx, itemID); err != nil {
		return fmt.Errorf("enqueue catalog search embedding update for item %s: %w", itemID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit upsert embedding for item %s: %w", itemID, err)
	}
	return nil
}

// GetEmbeddingLock retrieves the embedding lock metadata from server_settings.
func (r *Repo) GetEmbeddingLock(ctx context.Context) (*EmbeddingLock, error) {
	var raw string
	err := r.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, embeddingLockSettingKey).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get embedding lock: %w", err)
	}
	lock, err := ParseEmbeddingLock(raw)
	if err != nil {
		return nil, err
	}
	return lock, nil
}

// SetEmbeddingLock stores the embedding lock metadata in server_settings.
func (r *Repo) SetEmbeddingLock(ctx context.Context, lock EmbeddingLock) error {
	if lock.StorageDimensions == 0 {
		lock.StorageDimensions = CanonicalEmbeddingDimensions
	}
	raw, err := lock.Marshal()
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
	`, embeddingLockSettingKey, raw)
	if err != nil {
		return fmt.Errorf("set embedding lock: %w", err)
	}
	return nil
}

// GetEmbedding retrieves the embedding vector for a media item.
// Returns nil, nil when no embedding exists.
func (r *Repo) GetEmbedding(ctx context.Context, itemID string) ([]float32, error) {
	var v pgvector.Vector
	err := r.pool.QueryRow(ctx,
		`SELECT embedding FROM media_item_embeddings WHERE media_item_id = $1`,
		itemID,
	).Scan(&v)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get embedding for item %s: %w", itemID, err)
	}
	return v.Slice(), nil
}

// FindSimilar returns items ordered by cosine similarity to the given embedding,
// excluding the specified item IDs. When mediaType is non-empty, results are
// restricted to that media_items.type so cross-media-type results never appear
// (e.g. an audiobook in a "Similar to this movie" rail) once audiobook
// embeddings exist alongside movie/series ones.
func (r *Repo) FindSimilar(ctx context.Context, embedding []float32, excludeIDs []string, mediaType string, limit int) ([]ScoredItem, error) {
	var items []ScoredItem
	err := r.withHNSWCandidateScan(ctx, limit, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.media_item_id,
			       1 - (e.embedding::halfvec(3072) <=> $1::halfvec(3072)) AS similarity
			FROM   media_item_embeddings e
			JOIN   media_items mi ON mi.content_id = e.media_item_id
			WHERE  e.media_item_id != ALL($2)
			  AND  ($4 = '' OR mi.type = $4)
			ORDER  BY e.embedding::halfvec(3072) <=> $1::halfvec(3072)
			LIMIT  $3
		`, pgvector.NewVector(embedding), excludeIDs, limit, mediaType)
		if err != nil {
			return err
		}
		defer rows.Close()

		items = make([]ScoredItem, 0, limit)
		for rows.Next() {
			var item ScoredItem
			if err := rows.Scan(&item.MediaItemID, &item.Score); err != nil {
				return fmt.Errorf("scanning similar item: %w", err)
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterating similar items: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find similar items: %w", err)
	}
	return items, nil
}

// FindTasteProfileCandidates returns full-library discover candidates for a
// user's taste-profile embedding, optionally constrained to items sharing at
// least one selected genre and to the caller's access scope.
func (r *Repo) FindTasteProfileCandidates(
	ctx context.Context,
	embedding []float32,
	excludeIDs []string,
	genres []string,
	limit int,
	filter catalog.AccessFilter,
) ([]ScoredItem, map[string][]string, error) {
	return r.findTasteProfileCandidates(ctx, embedding, excludeIDs, genres, limit, filter, "")
}

func (r *Repo) FindTasteProfileCandidatesByMediaType(
	ctx context.Context,
	embedding []float32,
	excludeIDs []string,
	genres []string,
	limit int,
	filter catalog.AccessFilter,
	mediaType string,
) ([]ScoredItem, map[string][]string, error) {
	return r.findTasteProfileCandidates(ctx, embedding, excludeIDs, genres, limit, filter, mediaType)
}

func (r *Repo) findTasteProfileCandidates(
	ctx context.Context,
	embedding []float32,
	excludeIDs []string,
	genres []string,
	limit int,
	filter catalog.AccessFilter,
	mediaType string,
) ([]ScoredItem, map[string][]string, error) {
	if embedding == nil || limit <= 0 {
		return []ScoredItem{}, map[string][]string{}, nil
	}
	if excludeIDs == nil {
		excludeIDs = []string{}
	}

	conditions := []string{
		"(mi.status = 'matched' OR mi.type = 'audiobook')",
		"e.media_item_id != ALL($2)",
	}
	args := []any{pgvector.NewVector(embedding), excludeIDs}
	argIdx := 3
	genreMatchCountSQL := "0"
	annLimit := limit

	if mediaType != "" {
		conditions = append(conditions, fmt.Sprintf("mi.type = $%d", argIdx))
		args = append(args, mediaType)
		argIdx++
	}

	if len(genres) > 0 {
		annLimit = limit * 5
		if annLimit < limit {
			annLimit = limit
		}
		if annLimit > 2000 {
			annLimit = 2000
		}
		genreMatchCountSQL = fmt.Sprintf(`(
				SELECT COUNT(DISTINCT matched_genre)
				FROM unnest(COALESCE(mi.genres, '{}'::text[])) AS matched_genre
				WHERE matched_genre = ANY($%d)
			)`, argIdx)
		conditions = append(conditions, fmt.Sprintf("mi.genres && $%d", argIdx))
		args = append(args, genres)
		argIdx++
	}

	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			return []ScoredItem{}, map[string][]string{}, nil
		}
		conditions = append(conditions, fmt.Sprintf(`
			EXISTS (
				SELECT 1
				FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id = ANY($%d)
			)`, argIdx))
		args = append(args, filter.AllowedLibraryIDs)
		argIdx++
	}

	if len(filter.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf(`
			EXISTS (
				SELECT 1
				FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id != ALL($%d)
			)`, argIdx))
		args = append(args, filter.DisabledLibraryIDs)
		argIdx++
	}

	if filter.MaxContentRating != "" {
		allowedRatings := access.AllowedRatingsUpTo(filter.MaxContentRating)
		if len(allowedRatings) == 0 {
			return []ScoredItem{}, map[string][]string{}, nil
		}

		placeholders := make([]string, len(allowedRatings))
		for i, rating := range allowedRatings {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, rating)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf(
			"mi.content_rating IN (%s)",
			strings.Join(placeholders, ", "),
		))
	}

	query := fmt.Sprintf(`
			WITH ann_candidates AS (
				SELECT mi.content_id,
				       e.embedding::halfvec(3072) <=> $1::halfvec(3072) AS distance,
				       COALESCE(mi.genres, '{}'::text[]) AS genres
				FROM   media_item_embeddings e
				JOIN   media_items mi ON mi.content_id = e.media_item_id
				WHERE  %s
				ORDER  BY e.embedding::halfvec(3072) <=> $1::halfvec(3072)
				LIMIT  $%d
			)
			SELECT content_id,
			       1 - distance AS similarity,
			       genres,
			       %s AS genre_match_count
			FROM   ann_candidates mi
			ORDER  BY genre_match_count DESC, distance ASC, content_id
			LIMIT  $%d`,
		strings.Join(conditions, " AND "),
		argIdx,
		genreMatchCountSQL,
		argIdx+1,
	)
	args = append(args, annLimit, limit)

	var items []ScoredItem
	genreMap := make(map[string][]string, limit)
	err := r.withHNSWCandidateScan(ctx, annLimit, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		items = make([]ScoredItem, 0, limit)
		for rows.Next() {
			var item ScoredItem
			var itemGenres []string
			var genreMatchCount int
			if err := rows.Scan(&item.MediaItemID, &item.Score, &itemGenres, &genreMatchCount); err != nil {
				return fmt.Errorf("scan taste profile candidate: %w", err)
			}
			item.Reason = "taste_profile_match"
			items = append(items, item)
			genreMap[item.MediaItemID] = itemGenres
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate taste profile candidates: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("find taste profile candidates: %w", err)
	}

	return items, genreMap, nil
}

// buildItemsNeedingEmbeddingSQL returns the cheap missing/model-stale candidate
// query. It deliberately avoids the per-row item_people LATERAL joins that the
// text-staleness CTE (ListEmbeddingTextCandidates) needs, so the active-backfill
// pass stays cheap: a single LEFT JOIN on the embeddings table is enough to find
// rows that have no embedding or were embedded under a different model. Ordering
// by content_id makes the $2 cursor stable across calls.
// Args: $1 = current model, $2 = afterID cursor (empty for the first page), $3 = limit.
func buildItemsNeedingEmbeddingSQL() string {
	return fmt.Sprintf(`
		SELECT mi.content_id
		FROM   media_items mi
		LEFT JOIN media_item_embeddings e ON e.media_item_id = mi.content_id
		WHERE  %s
		  AND  (e.media_item_id IS NULL OR e.model != $1)
		  AND  ($2 = '' OR mi.content_id > $2)
		ORDER  BY mi.content_id
		LIMIT  $3
	`, embeddingEligibilityWhereClause())
}

// ItemsNeedingEmbedding returns content IDs of media items that either have no
// embedding or whose embedding was generated with a different model, ordered by
// content_id and paged via afterID (pass "" for the first page). This is the
// "cheap" pass of the embedding backfill: it intentionally does NOT detect
// text-staleness (which would require the expensive item_people LATERAL joins);
// re-embedding text-changed items is handled separately by
// ListEmbeddingTextCandidates.
//
// Books bypass the status='matched' gate because their scanner/plugin-derived
// metadata is authoritative as soon as the scan/enrichment completes.
func (r *Repo) ItemsNeedingEmbedding(ctx context.Context, currentModel, afterID string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, buildItemsNeedingEmbeddingSQL(), currentModel, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("items needing embedding: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning content_id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating items needing embedding: %w", err)
	}
	return ids, nil
}

func (r *Repo) ListEmbeddingTextCandidates(ctx context.Context, afterID, currentModel string, limit int) ([]EmbeddingTextCandidate, error) {
	query := fmt.Sprintf(`
		-- Keep current_text in sync with embeddings.BuildEmbeddingText. This lets
		-- the embedding job page over only missing, model-stale, or text-stale rows.
		-- Book lines map to embeddings.mediaTypeLabel + the author/narrator
		-- branch in BuildEmbeddingText; any divergence here forces book items
		-- to re-embed every job run.
		WITH text_candidates AS (
			SELECT mi.content_id,
			       COALESCE(e.model, '') AS model,
			       COALESCE(e.canonical_text, '') AS canonical_text,
			       array_to_string(array_remove(ARRAY[
			           CASE
			               WHEN cardinality(COALESCE(mi.genres, ARRAY[]::text[])) > 0 AND COALESCE(mi.overview, '') <> ''
			                   THEN array_to_string(mi.genres, ', ') || ' ' || CASE WHEN mi.type = 'series' THEN 'TV series' WHEN mi.type = 'audiobook' THEN 'audiobook' WHEN mi.type = 'ebook' THEN 'ebook' ELSE 'movie' END || ' about ' || substr(mi.overview, 1, 1000)
			               WHEN cardinality(COALESCE(mi.genres, ARRAY[]::text[])) > 0
			                   THEN array_to_string(mi.genres, ', ') || ' ' || CASE WHEN mi.type = 'series' THEN 'TV series' WHEN mi.type = 'audiobook' THEN 'audiobook' WHEN mi.type = 'ebook' THEN 'ebook' ELSE 'movie' END
			               WHEN COALESCE(mi.overview, '') <> ''
			                   THEN CASE WHEN mi.type = 'series' THEN 'TV series' WHEN mi.type = 'audiobook' THEN 'audiobook' WHEN mi.type = 'ebook' THEN 'ebook' ELSE 'movie' END || '. ' || substr(mi.overview, 1, 1000)
			               ELSE NULL
			           END,
			           CASE WHEN COALESCE(mi.year, 0) > 0
			                THEN COALESCE(mi.title, '') || ' (' || mi.year::text || ')'
			                ELSE COALESCE(mi.title, '')
			           END,
			           CASE WHEN COALESCE(mi.content_rating, '') <> '' THEN 'Rated ' || mi.content_rating ELSE NULL END,
			           CASE WHEN COALESCE(mi.tagline, '') <> '' THEN '"' || mi.tagline || '"' ELSE NULL END,
			           CASE WHEN mi.type NOT IN ('audiobook', 'ebook') AND actors.names <> '' THEN 'Cast: ' || actors.names ELSE NULL END,
			           CASE WHEN mi.type NOT IN ('audiobook', 'ebook') AND directors.names <> '' THEN 'Directed by ' || directors.names ELSE NULL END,
			           CASE
			               WHEN mi.type IN ('audiobook', 'ebook') AND authors.names <> '' THEN 'Written by ' || authors.names
			               WHEN mi.type NOT IN ('audiobook', 'ebook') AND writers.names <> '' THEN 'Written by ' || writers.names
			               ELSE NULL
			           END,
			           CASE WHEN mi.type = 'audiobook' AND narrators.names <> '' THEN 'Narrated by ' || narrators.names ELSE NULL END,
			           CASE WHEN cardinality(COALESCE(mi.keywords, ARRAY[]::text[])) > 0 THEN 'Keywords: ' || array_to_string((mi.keywords)[1:5], ', ') ELSE NULL END,
			           CASE WHEN COALESCE(mi.original_language, '') <> '' THEN 'Original language: ' || mi.original_language ELSE NULL END,
			           CASE WHEN cardinality(COALESCE(mi.studios, ARRAY[]::text[])) > 0 THEN 'Studios: ' || array_to_string(mi.studios, ', ') ELSE NULL END,
			           CASE WHEN cardinality(COALESCE(mi.networks, ARRAY[]::text[])) > 0 THEN 'Network: ' || array_to_string(mi.networks, ', ') ELSE NULL END,
			           CASE WHEN cardinality(COALESCE(mi.countries, ARRAY[]::text[])) > 0 THEN 'Country: ' || array_to_string((mi.countries)[1:2], ', ') ELSE NULL END
			       ]::text[], NULL), '. ') AS current_text
			FROM   media_items mi
			LEFT JOIN media_item_embeddings e ON e.media_item_id = mi.content_id
			LEFT JOIN LATERAL (
				SELECT COALESCE(string_agg(
					CASE WHEN ranked.character <> '' THEN ranked.name || ' as ' || ranked.character ELSE ranked.name END,
					', ' ORDER BY ranked.sort_order
				), '') AS names
				FROM (
					SELECT COALESCE(p.name, '') AS name,
					       COALESCE(ip.character, '') AS character,
					       ip.sort_order,
					       ip.person_id
					FROM item_people ip
					JOIN people p ON p.id = ip.person_id
					WHERE ip.content_id = mi.content_id
					  AND ip.kind = 1
					ORDER BY ip.sort_order, p.name, COALESCE(ip.character, ''), ip.person_id
					LIMIT 5
				) ranked
			) actors ON TRUE
			LEFT JOIN LATERAL (
				SELECT COALESCE(string_agg(COALESCE(p.name, ''), ', ' ORDER BY ip.sort_order, p.name, ip.person_id), '') AS names
				FROM item_people ip
				JOIN people p ON p.id = ip.person_id
				WHERE ip.content_id = mi.content_id
				  AND ip.kind = 2
			) directors ON TRUE
			LEFT JOIN LATERAL (
				SELECT COALESCE(string_agg(COALESCE(p.name, ''), ', ' ORDER BY ip.sort_order, p.name, ip.person_id), '') AS names
				FROM item_people ip
				JOIN people p ON p.id = ip.person_id
				WHERE ip.content_id = mi.content_id
				  AND ip.kind = 3
			) writers ON TRUE
			LEFT JOIN LATERAL (
				SELECT COALESCE(string_agg(COALESCE(p.name, ''), ', ' ORDER BY ip.sort_order, p.name, ip.person_id), '') AS names
				FROM item_people ip
				JOIN people p ON p.id = ip.person_id
				WHERE ip.content_id = mi.content_id
				  AND ip.kind = 7
			) authors ON TRUE
			LEFT JOIN LATERAL (
				SELECT COALESCE(string_agg(COALESCE(p.name, ''), ', ' ORDER BY ip.sort_order, p.name, ip.person_id), '') AS names
				FROM item_people ip
				JOIN people p ON p.id = ip.person_id
				WHERE ip.content_id = mi.content_id
				  AND ip.kind = 8
			) narrators ON TRUE
			WHERE  %s
			  AND  ($1 = '' OR mi.content_id > $1)
		)
		SELECT mi.content_id,
		       mi.model,
		       mi.canonical_text
		FROM   text_candidates mi
		WHERE  mi.model = ''
		   OR  mi.model != $2
		   OR  mi.canonical_text IS DISTINCT FROM mi.current_text
		ORDER  BY mi.content_id
		LIMIT  $3
	`, embeddingEligibilityWhereClause())
	rows, err := r.pool.Query(ctx, query, afterID, currentModel, limit)
	if err != nil {
		return nil, fmt.Errorf("embedding text candidates: %w", err)
	}
	defer rows.Close()

	var candidates []EmbeddingTextCandidate
	for rows.Next() {
		var candidate EmbeddingTextCandidate
		if err := rows.Scan(&candidate.MediaItemID, &candidate.Model, &candidate.CanonicalText); err != nil {
			return nil, fmt.Errorf("scanning embedding text candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating embedding text candidates: %w", err)
	}
	return candidates, nil
}

// EmbeddingCount returns the total number of stored embeddings.
func (r *Repo) EmbeddingCount(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_item_embeddings`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("embedding count: %w", err)
	}
	return count, nil
}

// TotalMediaItemCount returns the number of media items eligible for embedding.
// Mirrors the status/type gate in ItemsNeedingEmbedding so progress reporting
// (used by the admin worker UI) lines up with what the job actually does.
func (r *Repo) TotalMediaItemCount(ctx context.Context) (int, error) {
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM media_items mi WHERE %s`, embeddingEligibilityWhereClause())
	err := r.pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("total media item count: %w", err)
	}
	return count, nil
}

// TasteProfileCount returns the total number of stored taste profiles.
func (r *Repo) TasteProfileCount(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_taste_profiles`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("taste profile count: %w", err)
	}
	return count, nil
}

// CacheEntryCount returns the total number of non-expired recommendation cache entries.
func (r *Repo) CacheEntryCount(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM recommendation_cache WHERE expires_at > NOW()`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("cache entry count: %w", err)
	}
	return count, nil
}

// UpsertTasteProfile stores or updates a user's precomputed taste profile.
func (r *Repo) UpsertTasteProfile(ctx context.Context, userID int, profileID string, embedding []float32, signalCounts map[string]int, maxContentRating string) error {
	countsJSON, err := json.Marshal(signalCounts)
	if err != nil {
		return fmt.Errorf("marshaling signal counts: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO user_taste_profiles
			(user_id, profile_id, embedding, signal_counts, max_content_rating, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (user_id, profile_id) DO UPDATE
			SET embedding          = EXCLUDED.embedding,
			    signal_counts      = EXCLUDED.signal_counts,
			    max_content_rating = EXCLUDED.max_content_rating,
			    updated_at         = NOW()
	`, userID, profileID, pgvector.NewVector(embedding), countsJSON, maxContentRating)
	if err != nil {
		return fmt.Errorf("upsert taste profile for user %d profile %s: %w", userID, profileID, err)
	}
	return nil
}

// TasteProfileMeta holds the non-vector metadata for a taste profile.
type TasteProfileMeta struct {
	SignalCounts     map[string]int
	MaxContentRating string
	UpdatedAt        string
}

// GetTasteProfileMeta retrieves the signal_counts and updated_at for a user's
// taste profile. Returns nil, nil when no profile exists.
func (r *Repo) GetTasteProfileMeta(ctx context.Context, userID int, profileID string) (*TasteProfileMeta, error) {
	var countsJSON []byte
	var updatedAt time.Time
	var maxContentRating string
	err := r.pool.QueryRow(ctx,
		`SELECT signal_counts, COALESCE(max_content_rating, ''), updated_at FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	).Scan(&countsJSON, &maxContentRating, &updatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get taste profile meta for user %d profile %s: %w", userID, profileID, err)
	}

	var counts map[string]int
	if err := json.Unmarshal(countsJSON, &counts); err != nil {
		return nil, fmt.Errorf("unmarshal signal_counts: %w", err)
	}
	return &TasteProfileMeta{
		SignalCounts:     counts,
		MaxContentRating: maxContentRating,
		UpdatedAt:        updatedAt.Format(time.RFC3339),
	}, nil
}

// GetTasteProfile retrieves the embedding for a user's taste profile.
// Returns nil, nil when no profile exists.
func (r *Repo) GetTasteProfile(ctx context.Context, userID int, profileID string) ([]float32, error) {
	var v pgvector.Vector
	err := r.pool.QueryRow(ctx,
		`SELECT embedding FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	).Scan(&v)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get taste profile for user %d profile %s: %w", userID, profileID, err)
	}
	return v.Slice(), nil
}

// FindSimilarUsers returns users with taste profiles similar to the given user's
// profile, filtered by max_content_rating compatibility.
func (r *Repo) FindSimilarUsers(ctx context.Context, userID int, profileID string, maxContentRating string, limit int) ([]UserSimilarity, error) {
	embedding, err := r.GetTasteProfile(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("getting taste profile for similarity search: %w", err)
	}
	if embedding == nil {
		return nil, nil
	}

	allowedPeerRatings := compatiblePeerContentRatings(maxContentRating)
	var users []UserSimilarity
	err = r.withHNSWCandidateScan(ctx, limit, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.user_id,
			       p.profile_id,
			       1 - (p.embedding::halfvec(3072) <=> $1::halfvec(3072)) AS score
			FROM   user_taste_profiles p
			WHERE  p.user_id     != $2
			  AND  ($3 OR COALESCE(p.max_content_rating, '') = '' OR p.max_content_rating = ANY($4::text[]))
			ORDER  BY p.embedding::halfvec(3072) <=> $1::halfvec(3072)
			LIMIT  $5
		`, pgvector.NewVector(embedding), userID, maxContentRating == "", allowedPeerRatings, limit)
		if err != nil {
			return err
		}
		defer rows.Close()

		users = make([]UserSimilarity, 0, limit)
		for rows.Next() {
			var u UserSimilarity
			if err := rows.Scan(&u.UserID, &u.ProfileID, &u.Score); err != nil {
				return fmt.Errorf("scanning similar user: %w", err)
			}
			users = append(users, u)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterating similar users: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find similar users: %w", err)
	}
	return users, nil
}

func compatiblePeerContentRatings(maxContentRating string) []string {
	allowed := access.AllowedRatingsUpTo(maxContentRating)
	if len(allowed) == 0 {
		return []string{}
	}
	sort.Strings(allowed)
	return allowed
}

// UpsertRecommendationCache stores or refreshes a precomputed recommendation
// list for a user.
func (r *Repo) UpsertRecommendationCache(ctx context.Context, userID int, profileID, recType, sourceItemID string, items []ScoredItem, expiresAt string) error {
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshaling cached items: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO recommendation_cache
			(user_id, profile_id, rec_type, source_item_id, items, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::timestamptz, NOW())
		ON CONFLICT (user_id, profile_id, rec_type, source_item_id) DO UPDATE
			SET items      = EXCLUDED.items,
			    expires_at = EXCLUDED.expires_at,
			    created_at = NOW()
	`, userID, profileID, recType, sourceItemID, itemsJSON, expiresAt)
	if err != nil {
		return fmt.Errorf("upsert recommendation cache: %w", err)
	}
	return nil
}

// GetRecommendationCache retrieves cached recommendation results that have not
// yet expired. Returns nil, nil on cache miss or expiry.
func (r *Repo) GetRecommendationCache(ctx context.Context, userID int, profileID, recType, sourceItemID string) ([]ScoredItem, error) {
	var itemsJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT items
		FROM   recommendation_cache
		WHERE  user_id        = $1
		  AND  profile_id     = $2
		  AND  rec_type       = $3
		  AND  source_item_id = $4
		  AND  expires_at     > NOW()
	`, userID, profileID, recType, sourceItemID).Scan(&itemsJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get recommendation cache: %w", err)
	}

	var items []ScoredItem
	if err := json.Unmarshal(itemsJSON, &items); err != nil {
		return nil, fmt.Errorf("unmarshaling cached items: %w", err)
	}
	return items, nil
}

// ListCachedGenreSamplers returns all non-expired global genre sampler cache entries
// as a map of genre name → scored items.
func (r *Repo) ListCachedGenreSamplers(ctx context.Context) (map[string][]ScoredItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT rec_type, items
		FROM   recommendation_cache
		WHERE  user_id    = $1
		  AND  profile_id = $2
		  AND  rec_type LIKE $3
		  AND  expires_at > NOW()`,
		GlobalCacheUserID, GlobalCacheProfileID, RecTypeGenreSamplerPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("list cached genre samplers: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]ScoredItem)
	for rows.Next() {
		var recType string
		var itemsJSON []byte
		if err := rows.Scan(&recType, &itemsJSON); err != nil {
			return nil, fmt.Errorf("scan genre sampler cache: %w", err)
		}
		genre := strings.TrimPrefix(recType, RecTypeGenreSamplerPrefix)
		var items []ScoredItem
		if err := json.Unmarshal(itemsJSON, &items); err != nil {
			continue
		}
		result[genre] = items
	}
	return result, rows.Err()
}

// CleanExpiredCache removes all expired recommendation cache entries and
// returns the number of rows deleted.
func (r *Repo) CleanExpiredCache(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM recommendation_cache WHERE expires_at < NOW()`)
	if err != nil {
		return 0, fmt.Errorf("clean expired cache: %w", err)
	}
	return tag.RowsAffected(), nil
}

// --- Taste Cluster Operations ---

// UpsertTasteClusters replaces all clusters for a user/profile.
func (r *Repo) UpsertTasteClusters(ctx context.Context, userID int, profileID string, clusters []TasteCluster) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for taste clusters: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`DELETE FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID)
	if err != nil {
		return fmt.Errorf("delete old clusters: %w", err)
	}

	for _, c := range clusters {
		genresJSON, err := json.Marshal(c.DominantGenres)
		if err != nil {
			return fmt.Errorf("marshal genres: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO user_taste_clusters
				(user_id, profile_id, cluster_idx, embedding, dominant_genres, label, member_count, total_weight, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())`,
			userID, profileID, c.ClusterIdx,
			pgvector.NewVector(c.Embedding), genresJSON, c.Label,
			c.MemberCount, c.TotalWeight)
		if err != nil {
			return fmt.Errorf("insert cluster %d: %w", c.ClusterIdx, err)
		}
	}

	return tx.Commit(ctx)
}

// GetTasteClusters retrieves all clusters for a user/profile.
func (r *Repo) GetTasteClusters(ctx context.Context, userID int, profileID string) ([]TasteCluster, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cluster_idx, embedding, dominant_genres, label, member_count, total_weight, updated_at
		FROM   user_taste_clusters
		WHERE  user_id = $1 AND profile_id = $2
		ORDER  BY cluster_idx`,
		userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste clusters: %w", err)
	}
	defer rows.Close()

	var clusters []TasteCluster
	for rows.Next() {
		var c TasteCluster
		var v pgvector.Vector
		var genresJSON []byte
		if err := rows.Scan(&c.ClusterIdx, &v, &genresJSON, &c.Label, &c.MemberCount, &c.TotalWeight, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan cluster: %w", err)
		}
		c.UserID = userID
		c.ProfileID = profileID
		c.Embedding = v.Slice()
		if err := json.Unmarshal(genresJSON, &c.DominantGenres); err != nil {
			return nil, fmt.Errorf("unmarshal cluster genres: %w", err)
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// --- Co-Watch Operations ---

// UpsertCowatchPairs bulk-upserts co-watch pairs. Operates in a single transaction.
func (r *Repo) UpsertCowatchPairs(ctx context.Context, pairs []CowatchPair) error {
	if len(pairs) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for cowatch: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, p := range pairs {
		_, err := tx.Exec(ctx, `
			INSERT INTO item_cowatch (item_id, similar_item_id, jaccard_score, cowatch_count, updated_at)
			VALUES ($1, $2, $3, $4, NOW())
			ON CONFLICT (item_id, similar_item_id) DO UPDATE
				SET jaccard_score = EXCLUDED.jaccard_score,
				    cowatch_count = EXCLUDED.cowatch_count,
				    updated_at    = NOW()`,
			p.ItemID, p.SimilarItemID, p.JaccardScore, p.CowatchCount)
		if err != nil {
			return fmt.Errorf("upsert cowatch pair: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// GetCowatchNeighbors returns the top co-watch neighbors for an item.
func (r *Repo) GetCowatchNeighbors(ctx context.Context, itemID string, limit int) ([]CowatchPair, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT item_id, similar_item_id, jaccard_score, cowatch_count
		FROM   item_cowatch
		WHERE  item_id = $1
		ORDER  BY jaccard_score DESC
		LIMIT  $2`,
		itemID, limit)
	if err != nil {
		return nil, fmt.Errorf("get cowatch neighbors: %w", err)
	}
	defer rows.Close()

	var pairs []CowatchPair
	for rows.Next() {
		var p CowatchPair
		if err := rows.Scan(&p.ItemID, &p.SimilarItemID, &p.JaccardScore, &p.CowatchCount); err != nil {
			return nil, fmt.Errorf("scan cowatch pair: %w", err)
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

// CowatchPairCount returns the total number of co-watch pairs stored.
func (r *Repo) CowatchPairCount(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM item_cowatch`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("cowatch pair count: %w", err)
	}
	return count, nil
}

// --- Staleness Operations ---

// MarkProfileStale sets stale_at = NOW() on a user's taste profile.
func (r *Repo) MarkProfileStale(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE user_taste_profiles SET stale_at = NOW() WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID)
	if err != nil {
		return fmt.Errorf("mark profile stale: %w", err)
	}
	return nil
}

// StaleProfile represents a taste profile that needs refreshing.
type StaleProfile struct {
	UserID    int
	ProfileID string
}

// GetStaleProfiles returns profiles where stale_at > updated_at.
func (r *Repo) GetStaleProfiles(ctx context.Context, limit int) ([]StaleProfile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, profile_id
		FROM   user_taste_profiles
		WHERE  stale_at IS NOT NULL AND stale_at > updated_at
		LIMIT  $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("get stale profiles: %w", err)
	}
	defer rows.Close()

	var profiles []StaleProfile
	for rows.Next() {
		var p StaleProfile
		if err := rows.Scan(&p.UserID, &p.ProfileID); err != nil {
			return nil, fmt.Errorf("scan stale profile: %w", err)
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// ClearStaleAt resets stale_at to NULL after refreshing a profile.
func (r *Repo) ClearStaleAt(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE user_taste_profiles SET stale_at = NULL WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID)
	if err != nil {
		return fmt.Errorf("clear stale_at: %w", err)
	}
	return nil
}

// --- Watch Signal Queries (cross-user aggregation) ---

// WatchProgressRow holds raw watch progress data for signal computation.
type WatchProgressRow struct {
	MediaItemID     string
	PositionSeconds float64
	DurationSeconds float64
	Completed       bool
	UpdatedAt       time.Time
}

// ResolveCanonicalContentRefs maps mixed movie/series/season/episode IDs to
// the canonical movie/series entity used for taste-profile learning.
func (r *Repo) ResolveCanonicalContentRefs(ctx context.Context, contentIDs []string) (map[string]canonicalContentRef, error) {
	result := make(map[string]canonicalContentRef, len(contentIDs))
	if len(contentIDs) == 0 {
		return result, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT content_id, kind, canonical_id, series_id, season_number
		FROM (
			SELECT mi.content_id,
			       mi.type AS kind,
			       mi.content_id AS canonical_id,
			       NULL::text AS series_id,
			       NULL::int AS season_number
			FROM   media_items mi
			WHERE  mi.content_id = ANY($1)

			UNION ALL

			SELECT s.content_id,
			       'season' AS kind,
			       s.series_id AS canonical_id,
			       s.series_id AS series_id,
			       s.season_number
			FROM   seasons s
			WHERE  s.content_id = ANY($1)

			UNION ALL

			SELECT e.content_id,
			       'episode' AS kind,
			       e.series_id AS canonical_id,
			       e.series_id AS series_id,
			       e.season_number
			FROM   episodes e
			WHERE  e.content_id = ANY($1)
		) refs
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical content refs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			contentID    string
			kind         string
			canonicalID  string
			seriesID     *string
			seasonNumber *int
		)
		if err := rows.Scan(&contentID, &kind, &canonicalID, &seriesID, &seasonNumber); err != nil {
			return nil, fmt.Errorf("scan canonical content ref: %w", err)
		}

		ref := canonicalContentRef{
			Kind:        canonicalContentKind(kind),
			CanonicalID: canonicalID,
		}
		if seriesID != nil {
			ref.SeriesID = *seriesID
		}
		if seasonNumber != nil {
			ref.SeasonNumber = *seasonNumber
			ref.HasSeason = true
		}
		result[contentID] = ref
	}

	return result, rows.Err()
}

// GetWatchProgressForUser returns all watch progress entries for a user/profile.
func (r *Repo) GetWatchProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_item_id, position_seconds, duration_seconds, completed, updated_at
		FROM   user_watch_progress
		WHERE  user_id = $1 AND profile_id = $2
		  AND  NOT EXISTS (
			SELECT 1
			FROM   user_history_hidden_items hhi
			WHERE  hhi.user_id = user_watch_progress.user_id
			  AND  hhi.profile_id = user_watch_progress.profile_id
			  AND  hhi.media_item_id = user_watch_progress.media_item_id
			  AND  user_watch_progress.updated_at <= hhi.hidden_before
		  )`,
		userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get watch progress: %w", err)
	}
	defer rows.Close()

	var result []WatchProgressRow
	for rows.Next() {
		var wp WatchProgressRow
		if err := rows.Scan(&wp.MediaItemID, &wp.PositionSeconds, &wp.DurationSeconds, &wp.Completed, &wp.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan watch progress: %w", err)
		}
		result = append(result, wp)
	}
	return result, rows.Err()
}

// ebookReaderProgressForUserQuery maps ebook reader progress onto the
// WatchProgressRow shape (progress as position with duration 1). Rows hidden
// via user_history_hidden_items are excluded with the same semantics as
// GetWatchProgressForUser: hidden when updated_at <= hidden_before, visible
// again once new reading activity lands after hidden_before.
var ebookReaderProgressForUserQuery = fmt.Sprintf(`
	SELECT content_id,
	       progress::double precision AS position_seconds,
	       1::double precision AS duration_seconds,
	       progress >= %s AS completed,
	       updated_at
	FROM   ebook_reader_progress
	WHERE  user_id = $1 AND profile_id = $2
	  AND  NOT EXISTS (
		SELECT 1
		FROM   user_history_hidden_items hhi
		WHERE  hhi.user_id = ebook_reader_progress.user_id
		  AND  hhi.profile_id = ebook_reader_progress.profile_id
		  AND  hhi.media_item_id = ebook_reader_progress.content_id
		  AND  ebook_reader_progress.updated_at <= hhi.hidden_before
	  )`, catalog.EbookFinishedProgressThresholdSQL)

func (r *Repo) GetEbookReaderProgressForUser(ctx context.Context, userID int, profileID string) ([]WatchProgressRow, error) {
	rows, err := r.pool.Query(ctx, ebookReaderProgressForUserQuery, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get ebook reader progress: %w", err)
	}
	defer rows.Close()

	var result []WatchProgressRow
	for rows.Next() {
		var wp WatchProgressRow
		if err := rows.Scan(&wp.MediaItemID, &wp.PositionSeconds, &wp.DurationSeconds, &wp.Completed, &wp.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan ebook reader progress: %w", err)
		}
		result = append(result, wp)
	}
	return result, rows.Err()
}

// RewatchCount holds the number of completed watches for an item.
type RewatchCount struct {
	MediaItemID   string
	Count         int
	LastWatchedAt time.Time
}

// GetRewatchCounts returns items watched to completion 2+ times by a user/profile.
func (r *Repo) GetRewatchCounts(ctx context.Context, userID int, profileID string) ([]RewatchCount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_item_id, COUNT(*) AS cnt, MAX(watched_at) AS last_watched_at
		FROM   user_watch_history
		WHERE  user_id = $1 AND profile_id = $2 AND completed = true
		  AND  NOT EXISTS (
			SELECT 1
			FROM   user_history_hidden_items hhi
			WHERE  hhi.user_id = user_watch_history.user_id
			  AND  hhi.profile_id = user_watch_history.profile_id
			  AND  hhi.media_item_id = user_watch_history.media_item_id
			  AND  user_watch_history.watched_at <= hhi.hidden_before
		  )
		GROUP  BY media_item_id
		HAVING COUNT(*) >= 2`,
		userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get rewatch counts: %w", err)
	}
	defer rows.Close()

	var result []RewatchCount
	for rows.Next() {
		var rc RewatchCount
		if err := rows.Scan(&rc.MediaItemID, &rc.Count, &rc.LastWatchedAt); err != nil {
			return nil, fmt.Errorf("scan rewatch count: %w", err)
		}
		result = append(result, rc)
	}
	return result, rows.Err()
}

// itemWatchersQuery feeds co-watch matrix computation. watched_activity rolls
// episodes up to their parent series, so a single watcher can produce many
// rows for the same item; the GROUP BY collapses them to one row per
// (watcher, item) before ranking and aggregation so a lone binge-watcher
// cannot satisfy the minimum-watchers threshold ($2) by itself and the
// per-user recency cap ($1) counts distinct items rather than raw rows.
//
// Watcher identity is (user_id, profile_id) for the Jaccard math — profiles of
// one account legitimately have distinct tastes — but the minimum-watchers
// floor ($2) is a sparsity/privacy threshold and must count distinct login
// ACCOUNTS: one household account with N profiles must not satisfy it alone,
// so the HAVING clause counts DISTINCT user_id rather than watcher rows.
var itemWatchersQuery = fmt.Sprintf(`
	WITH %s,
	user_watches AS (
		SELECT user_id,
		       watcher_id,
		       item_id AS media_item_id,
		       ROW_NUMBER() OVER (PARTITION BY user_id, profile_id ORDER BY MAX(updated_at) DESC) AS rn
		FROM   watched_activity
		GROUP  BY user_id, profile_id, watcher_id, item_id
	)
	SELECT media_item_id, ARRAY_AGG(watcher_id) AS watchers
	FROM   user_watches
	WHERE  rn <= $1
	GROUP  BY media_item_id
	HAVING COUNT(DISTINCT user_id) >= $2`, watchedActivityCTE)

// ItemWatchers maps item_id to profile identities that watched >= 50% progress.
// Used for co-watch matrix computation.
func (r *Repo) GetItemWatchers(ctx context.Context, minWatchers int, maxPerUser int) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx, itemWatchersQuery, maxPerUser, minWatchers)
	if err != nil {
		return nil, fmt.Errorf("get item watchers: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var itemID string
		var watchers []string
		if err := rows.Scan(&itemID, &watchers); err != nil {
			return nil, fmt.Errorf("scan item watchers: %w", err)
		}
		result[itemID] = watchers
	}
	return result, rows.Err()
}

// --- Cold Start Queries ---

// GetPopularItems returns the most-played media items over the given number of days.
// Episodes are resolved to their parent series.
func (r *Repo) GetPopularItems(ctx context.Context, days, limit int) ([]ScoredItem, error) {
	query := fmt.Sprintf(`
		WITH %s,
		watched_items AS (
			SELECT item_id, watcher_id
			FROM   watched_activity
			WHERE  updated_at > NOW() - ($1 || ' days')::interval
		)
		SELECT wi.item_id, COUNT(DISTINCT wi.watcher_id) AS watch_count
		FROM   watched_items wi
		JOIN   media_items mi ON mi.content_id = wi.item_id
		GROUP  BY wi.item_id
		ORDER  BY watch_count DESC
		LIMIT  $2`, watchedActivityCTE)
	rows, err := r.pool.Query(ctx, query, fmt.Sprintf("%d", days), limit)
	if err != nil {
		return nil, fmt.Errorf("get popular items: %w", err)
	}
	defer rows.Close()

	var items []ScoredItem
	for rows.Next() {
		var item ScoredItem
		var watchCount int
		if err := rows.Scan(&item.MediaItemID, &watchCount); err != nil {
			return nil, fmt.Errorf("scan popular item: %w", err)
		}
		item.Score = float64(watchCount)
		item.Reason = "popular"
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetRecentlyAddedItems returns items added within the given number of days.
// Audiobooks and ebooks bypass the matched-status gate because their
// scan-derived metadata is authoritative before any external-provider match
// exists.
func (r *Repo) GetRecentlyAddedItems(ctx context.Context, days, limit int) ([]ScoredItem, error) {
	query := fmt.Sprintf(`
		SELECT mi.content_id, mi.created_at
		FROM   media_items mi
		WHERE  %s
		  AND  mi.created_at > NOW() - ($1 || ' days')::interval
		ORDER  BY mi.created_at DESC
		LIMIT  $2`, recommendationItemEligibilityWhereClause("mi"))
	rows, err := r.pool.Query(ctx, query, fmt.Sprintf("%d", days), limit)
	if err != nil {
		return nil, fmt.Errorf("get recently added: %w", err)
	}
	defer rows.Close()

	var items []ScoredItem
	for rows.Next() {
		var item ScoredItem
		var createdAt time.Time
		if err := rows.Scan(&item.MediaItemID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan recently added: %w", err)
		}
		item.Score = float64(createdAt.Unix())
		item.Reason = "recently_added"
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetTopRatedItems returns items with highest average rating (min ratingCount ratings).
func (r *Repo) GetTopRatedItems(ctx context.Context, minRatings, limit int) ([]ScoredItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_item_id, AVG(rating) AS avg_rating
		FROM   user_ratings
		GROUP  BY media_item_id
		HAVING COUNT(*) >= $1
		ORDER  BY avg_rating DESC
		LIMIT  $2`,
		minRatings, limit)
	if err != nil {
		return nil, fmt.Errorf("get top rated: %w", err)
	}
	defer rows.Close()

	var items []ScoredItem
	for rows.Next() {
		var item ScoredItem
		if err := rows.Scan(&item.MediaItemID, &item.Score); err != nil {
			return nil, fmt.Errorf("scan top rated: %w", err)
		}
		item.Reason = "top_rated"
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetTasteSeedCandidates returns movie/series/audiobook content IDs ordered for the
// taste-seeding picker: server engagement first (most-watched in the last
// 180 days), then rating reliability and rating score, then recency. This keeps
// fresh servers from front-loading single-vote TMDB 10.0 obscurities while
// established servers prioritize what users actually watch. Episodes are resolved
// to their parent series. Items without a poster are excluded.
func (r *Repo) GetTasteSeedCandidates(ctx context.Context, limit, offset int) ([]string, error) {
	rows, err := r.pool.Query(ctx, tasteSeedCandidateQuery, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("get taste seed candidates: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan taste seed candidate: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetTopGenres returns the most popular genres by watch count.
// Episodes are resolved to their parent series for genre lookup.
func (r *Repo) GetTopGenres(ctx context.Context, limit int) ([]string, error) {
	query := fmt.Sprintf(`
		WITH %s,
		watched_items AS (
			SELECT item_id, watcher_id
			FROM   watched_activity
		)
		SELECT g.genre, COUNT(DISTINCT wi.watcher_id) AS watchers
		FROM   watched_items wi
		JOIN   media_items mi ON mi.content_id = wi.item_id
		CROSS JOIN LATERAL UNNEST(mi.genres) AS g(genre)
		GROUP  BY g.genre
		ORDER  BY watchers DESC
		LIMIT  $1`, watchedActivityCTE)
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("get top genres: %w", err)
	}
	defer rows.Close()

	var genres []string
	for rows.Next() {
		var genre string
		var count int
		if err := rows.Scan(&genre, &count); err != nil {
			return nil, fmt.Errorf("scan top genre: %w", err)
		}
		genres = append(genres, genre)
	}
	return genres, rows.Err()
}

// GetGenreSamplerItems returns the most-played media items in a specific genre.
func (r *Repo) GetGenreSamplerItems(ctx context.Context, genre string, limit int) ([]ScoredItem, error) {
	query := fmt.Sprintf(`
		WITH %s,
		watched_items AS (
			SELECT item_id, watcher_id
			FROM   watched_activity
		)
		SELECT wi.item_id, COUNT(DISTINCT wi.watcher_id) AS watch_count
		FROM   watched_items wi
		JOIN   media_items mi ON mi.content_id = wi.item_id
		WHERE  $1 = ANY(mi.genres)
		GROUP  BY wi.item_id
		ORDER  BY watch_count DESC
		LIMIT  $2`, watchedActivityCTE)
	rows, err := r.pool.Query(ctx, query, genre, limit)
	if err != nil {
		return nil, fmt.Errorf("get genre sampler items: %w", err)
	}
	defer rows.Close()

	var items []ScoredItem
	for rows.Next() {
		var item ScoredItem
		var watchCount int
		if err := rows.Scan(&item.MediaItemID, &watchCount); err != nil {
			return nil, fmt.Errorf("scan genre sampler item: %w", err)
		}
		item.Score = float64(watchCount)
		item.Reason = "genre_sampler"
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetBatchEmbeddings retrieves embeddings for multiple item IDs.
func (r *Repo) GetBatchEmbeddings(ctx context.Context, itemIDs []string) (map[string][]float32, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT media_item_id, embedding
		FROM   media_item_embeddings
		WHERE  media_item_id = ANY($1)`,
		itemIDs)
	if err != nil {
		return nil, fmt.Errorf("get batch embeddings: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]float32)
	for rows.Next() {
		var id string
		var v pgvector.Vector
		if err := rows.Scan(&id, &v); err != nil {
			return nil, fmt.Errorf("scan batch embedding: %w", err)
		}
		result[id] = v.Slice()
	}
	return result, rows.Err()
}

// GetItemGenres returns the full genre array for each item ID.
func (r *Repo) GetItemGenres(ctx context.Context, itemIDs []string) (map[string][]string, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, genres
		FROM   media_items
		WHERE  content_id = ANY($1)
		  AND  array_length(genres, 1) > 0`,
		itemIDs)
	if err != nil {
		return nil, fmt.Errorf("get item genres: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var id string
		var genres []string
		if err := rows.Scan(&id, &genres); err != nil {
			return nil, fmt.Errorf("scan item genre: %w", err)
		}
		result[id] = genres
	}
	return result, rows.Err()
}

// FilterAccessibleItemIDs returns the subset of item IDs allowed by the given
// access filter. The returned map is keyed by media_items.content_id.
func (r *Repo) FilterAccessibleItemIDs(ctx context.Context, itemIDs []string, filter catalog.AccessFilter) (map[string]struct{}, error) {
	if len(itemIDs) == 0 {
		return map[string]struct{}{}, nil
	}

	conditions := []string{"mi.content_id = ANY($1)"}
	args := []any{itemIDs}
	argIdx := 2

	if filter.AllowedContentIDs != nil {
		if len(filter.AllowedContentIDs) == 0 {
			return map[string]struct{}{}, nil
		}
		conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY($%d)", argIdx))
		args = append(args, filter.AllowedContentIDs)
		argIdx++
	}

	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			return map[string]struct{}{}, nil
		}
		conditions = append(conditions, fmt.Sprintf(`
			EXISTS (
				SELECT 1
				FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id = ANY($%d)
			)`, argIdx))
		args = append(args, filter.AllowedLibraryIDs)
		argIdx++
	} else if len(filter.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf(`
			EXISTS (
				SELECT 1
				FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id != ALL($%d)
			)`, argIdx))
		args = append(args, filter.DisabledLibraryIDs)
		argIdx++
	}

	if filter.MaxContentRating != "" {
		allowedRatings := access.AllowedRatingsUpTo(filter.MaxContentRating)
		if len(allowedRatings) == 0 {
			return map[string]struct{}{}, nil
		}
		conditions = append(conditions, fmt.Sprintf("mi.content_rating = ANY($%d)", argIdx))
		args = append(args, allowedRatings)
		argIdx++
	}

	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT mi.content_id
		FROM   media_items mi
		WHERE  %s`, strings.Join(conditions, " AND ")), args...)
	if err != nil {
		return nil, fmt.Errorf("filter accessible item IDs: %w", err)
	}
	defer rows.Close()

	result := make(map[string]struct{}, len(itemIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan accessible item ID: %w", err)
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

// GetItemAddedDates returns the created_at dates for a set of items.
func (r *Repo) GetItemAddedDates(ctx context.Context, itemIDs []string) (map[string]time.Time, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, created_at
		FROM   media_items
		WHERE  content_id = ANY($1)`,
		itemIDs)
	if err != nil {
		return nil, fmt.Errorf("get item added dates: %w", err)
	}
	defer rows.Close()

	result := make(map[string]time.Time)
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, fmt.Errorf("scan item added date: %w", err)
		}
		result[id] = t
	}
	return result, rows.Err()
}

// GetAllUsersWithTasteProfiles returns all user/profile pairs that have taste profiles.
func (r *Repo) GetAllUsersWithTasteProfiles(ctx context.Context) ([]StaleProfile, error) {
	rows, err := r.pool.Query(ctx, `SELECT user_id, profile_id FROM user_taste_profiles`)
	if err != nil {
		return nil, fmt.Errorf("get all users with taste profiles: %w", err)
	}
	defer rows.Close()

	var profiles []StaleProfile
	for rows.Next() {
		var p StaleProfile
		if err := rows.Scan(&p.UserID, &p.ProfileID); err != nil {
			return nil, fmt.Errorf("scan user with taste profile: %w", err)
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// GetWatchedItemIDs returns content IDs of media items the user has watched
// (>= 50% progress or completed). Episodes are resolved to their parent series.
func (r *Repo) GetWatchedItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	query := fmt.Sprintf(`
		WITH %s
		SELECT DISTINCT item_id
		FROM   watched_activity
		WHERE  user_id = $1 AND profile_id = $2`, watchedActivityCTE)
	rows, err := r.pool.Query(ctx, query, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get watched item IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan watched item ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetWatchedItemIDSet returns the watched item IDs as a set keyed by content ID.
func (r *Repo) GetWatchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	ids, err := r.GetWatchedItemIDs(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	return scoredItemIDSet(ids), nil
}

// GetWatchedItemIDSetFromStore derives watched item IDs from a user store,
// then canonicalizes episode progress rows to their parent series IDs.
func (r *Repo) GetWatchedItemIDSetFromStore(ctx context.Context, store userstore.UserStore, profileID string) (map[string]struct{}, error) {
	if store == nil {
		return map[string]struct{}{}, nil
	}

	const pageSize = 1000
	rawIDs := make([]string, 0, pageSize)
	offset := 0

	for {
		progress, err := store.ListProgress(ctx, profileID, "all", pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("list progress from store: %w", err)
		}

		for _, wp := range progress {
			if wp.Completed || (wp.DurationSeconds > 0 && wp.PositionSeconds/wp.DurationSeconds >= 0.5) {
				rawIDs = append(rawIDs, wp.MediaItemID)
			}
		}

		if len(progress) < pageSize {
			break
		}
		offset += len(progress)
	}

	return r.ResolveCanonicalItemIDSet(ctx, rawIDs)
}

// GetRecentCompletedItemIDs returns the most recently completed leaf item IDs for a profile.
func (r *Repo) GetRecentCompletedItemIDs(ctx context.Context, userID int, profileID string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}

	query := fmt.Sprintf(`
		WITH %s
		SELECT leaf_item_id
		FROM   watched_activity
		WHERE  user_id = $1 AND profile_id = $2 AND completed = true
		ORDER  BY updated_at DESC, leaf_item_id ASC
		LIMIT  $3
	`, watchedActivityCTE)
	rows, err := r.pool.Query(ctx, query, userID, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("get recent completed item IDs: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan recent completed item ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent completed item IDs: %w", err)
	}
	return ids, nil
}

// ExcludeWatchedItems removes watched items from a scored recommendation list.
func (r *Repo) ExcludeWatchedItems(ctx context.Context, userID int, profileID string, items []ScoredItem) ([]ScoredItem, error) {
	if len(items) == 0 {
		return items, nil
	}

	watchedSet, err := r.GetWatchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get watched item IDs: %w", err)
	}
	return excludeScoredItems(items, watchedSet), nil
}

func scoredItemIDSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func excludeScoredItems(items []ScoredItem, excluded map[string]struct{}) []ScoredItem {
	if len(items) == 0 || len(excluded) == 0 {
		return items
	}

	filtered := make([]ScoredItem, 0, len(items))
	for _, item := range items {
		if _, ok := excluded[item.MediaItemID]; ok {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// ResolveCanonicalItemIDSet maps episode IDs to their parent series IDs and
// leaves movie/series IDs unchanged.
func (r *Repo) ResolveCanonicalItemIDSet(ctx context.Context, itemIDs []string) (map[string]struct{}, error) {
	if len(itemIDs) == 0 {
		return map[string]struct{}{}, nil
	}

	set := scoredItemIDSet(itemIDs)
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, series_id
		FROM episodes
		WHERE content_id = ANY($1)
	`, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical item IDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID string
		var seriesID string
		if err := rows.Scan(&contentID, &seriesID); err != nil {
			return nil, fmt.Errorf("scan canonical item ID: %w", err)
		}
		delete(set, contentID)
		set[seriesID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate canonical item IDs: %w", err)
	}

	return set, nil
}

// CleanOldCacheTypes removes V1 cache entries that are no longer used.
func (r *Repo) CleanOldCacheTypes(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM recommendation_cache WHERE user_id = $1 AND profile_id = $2 AND rec_type IN ('for_you', 'taste_match')`,
		userID, profileID)
	if err != nil {
		return fmt.Errorf("clean old cache types: %w", err)
	}
	return nil
}

// GetItemAllGenres returns the full genre array for each item ID.
func (r *Repo) GetItemAllGenres(ctx context.Context, itemIDs []string) (map[string][]string, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, genres
		FROM   media_items
		WHERE  content_id = ANY($1)
		  AND  genres IS NOT NULL
		  AND  array_length(genres, 1) > 0`,
		itemIDs)
	if err != nil {
		return nil, fmt.Errorf("get item genres: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var id string
		var genres []string
		if err := rows.Scan(&id, &genres); err != nil {
			return nil, fmt.Errorf("scan item genres: %w", err)
		}
		result[id] = genres
	}
	return result, rows.Err()
}

func (r *Repo) GetItemMediaTypes(ctx context.Context, itemIDs []string) (map[string]string, error) {
	if len(itemIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, type
		FROM   media_items
		WHERE  content_id = ANY($1)`,
		itemIDs)
	if err != nil {
		return nil, fmt.Errorf("get item media types: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string, len(itemIDs))
	for rows.Next() {
		var id, mediaType string
		if err := rows.Scan(&id, &mediaType); err != nil {
			return nil, fmt.Errorf("scan item media type: %w", err)
		}
		result[id] = mediaType
	}
	return result, rows.Err()
}

// ItemMetadata holds lightweight metadata used by the validation pipeline.
type ItemMetadata struct {
	Title   string
	Type    string
	Genres  []string
	Year    int
	Studios []string
}

// GetItemMetadata returns lightweight metadata for a single item.
func (r *Repo) GetItemMetadata(ctx context.Context, itemID string) (*ItemMetadata, error) {
	var m ItemMetadata
	err := r.pool.QueryRow(ctx, `
		SELECT title, COALESCE(type, ''), genres, COALESCE(year, 0), studios
		FROM media_items WHERE content_id = $1`, itemID,
	).Scan(&m.Title, &m.Type, &m.Genres, &m.Year, &m.Studios)
	if err != nil {
		return nil, fmt.Errorf("get item metadata %s: %w", itemID, err)
	}
	return &m, nil
}

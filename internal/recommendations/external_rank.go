package recommendations

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const (
	// ExternalRowSize is how many outside titles a profile's row keeps.
	ExternalRowSize = 60
	// externalMinCandidates is the fewest candidates of a media type a
	// profile needs for a row. Standardizing fewer gives scores that mean
	// little, and a shorter row is not worth a place on a page.
	externalMinCandidates = 10
	// externalMaxReasons is how many seeds an item names as its reasons.
	externalMaxReasons = 2
)

// externalMediaTypes are the media types a profile gets an outside row of.
var externalMediaTypes = []string{ExternalMediaMovie, ExternalMediaSeries}

// rankExternalCandidates scores a profile's candidates of one media type and
// returns its row: score = z(similarity) + z(log(1 + vote count)), each
// standardized over these candidates, best first (ties by TMDB ID), at most
// ExternalRowSize. A measure with no spread adds 0 to every score. Each item
// names up to externalMaxReasons of the seeds that named it, best rank
// first, as library content IDs from seedContentIDs; seeds missing from it
// are skipped. A candidate whose similarity is not finite is left out. Fewer
// than externalMinCandidates candidates give no row.
func rankExternalCandidates(sims []ExternalSimilarity, pool map[ExternalTitleKey]ExternalPoolTitle, seedContentIDs map[string]string) []ExternalRowItem {
	// A zero vector has no cosine: its similarity is NaN, which would turn
	// every standardized score NaN and make the row unstorable.
	sims = slices.DeleteFunc(slices.Clone(sims), func(s ExternalSimilarity) bool {
		return math.IsNaN(s.Similarity) || math.IsInf(s.Similarity, 0)
	})
	if len(sims) < externalMinCandidates {
		return nil
	}
	similarity := make([]float64, len(sims))
	popularity := make([]float64, len(sims))
	for i, s := range sims {
		similarity[i] = s.Similarity
		popularity[i] = math.Log1p(float64(max(s.VoteCount, 0)))
	}
	zSim, zPop := standardize(similarity), standardize(popularity)
	items := make([]ExternalRowItem, len(sims))
	for i, s := range sims {
		items[i] = ExternalRowItem{
			TMDBID:  s.Key.TMDBID,
			Score:   zSim[i] + zPop[i],
			Reasons: externalReasons(pool[s.Key].Seeds, seedContentIDs),
		}
	}
	slices.SortFunc(items, func(a, b ExternalRowItem) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.TMDBID, b.TMDBID))
	})
	return items[:min(len(items), ExternalRowSize)]
}

// standardize returns each value's z-score over values (population standard
// deviation), or all zeros when they have no spread.
func standardize(values []float64) []float64 {
	out := make([]float64, len(values))
	if len(values) == 0 {
		return out
	}
	var mean float64
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	var variance float64
	for _, v := range values {
		variance += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(variance / float64(len(values)))
	if sd < 1e-12 {
		return out
	}
	for i, v := range values {
		out[i] = (v - mean) / sd
	}
	return out
}

// externalReasons maps a candidate's seeds, best rank first, to up to
// externalMaxReasons distinct library content IDs.
func externalReasons(seeds []ExternalPoolSeed, seedContentIDs map[string]string) []string {
	reasons := []string{}
	for _, s := range seeds {
		id, ok := seedContentIDs[s.SourceKey]
		if !ok || id == "" || slices.Contains(reasons, id) {
			continue
		}
		reasons = append(reasons, id)
		if len(reasons) == externalMaxReasons {
			break
		}
	}
	return reasons
}

// ExternalRowsBuilt counts one profile's outside row rebuild.
type ExternalRowsBuilt struct {
	// Written counts the rows stored; Deleted the media types left without
	// a row (too few candidates, no taste vector, no embedding lock).
	Written int
	Deleted int
	// Refused counts rows not written because the embedding lock changed
	// while they were built.
	Refused int
}

// rebuildExternalRows rebuilds one profile's outside rows from stored data:
// its pool, the outside vectors and its taste vector. It calls neither TMDB
// nor the embedding provider. A profile with no taste vector, no stored
// embedding lock, or fewer than externalMinCandidates candidates of a media
// type loses that row. A row is written only while the embedding lock it was
// built under is still stored unchanged, so a row built across an embeddings
// reset is refused. The caller holds the profile's refresh lock and, after
// this returns, checks that the profile still exists (purgeIfProfileDeleted).
func (e *Engine) rebuildExternalRows(ctx context.Context, held clusterLock, userID int, profileID string, signals *tasteSignals) (ExternalRowsBuilt, error) {
	var res ExternalRowsBuilt
	writer := e.repo.externalPoolWriter
	if held != nil {
		writer = held.poolWriter(writer)
	}
	deleteAll := func() (ExternalRowsBuilt, error) {
		n, err := writer.deleteExternalRows(ctx, userID, profileID)
		res.Deleted += int(n)
		return res, err
	}
	lockVersion, lock, err := e.repo.externalRowLock(ctx)
	if err != nil {
		return res, err
	}
	if lock == nil {
		return deleteAll()
	}
	taste, err := e.repo.GetTasteProfile(ctx, userID, profileID)
	if err != nil {
		return res, err
	}
	if len(taste) == 0 {
		return deleteAll()
	}
	profile, exists, err := e.externalProfile(ctx, userID, profileID, signals)
	if err != nil {
		return res, err
	}
	if !exists {
		return deleteAll()
	}
	poolTitles, err := e.repo.ExternalPool(ctx, profile.PoolQuery())
	if err != nil {
		return res, err
	}
	pool := make(map[ExternalTitleKey]ExternalPoolTitle, len(poolTitles))
	ids := map[string][]int{}
	for _, t := range poolTitles {
		pool[t.Key] = t
		ids[t.Key.MediaType] = append(ids[t.Key.MediaType], t.Key.TMDBID)
	}
	seedContentIDs := profile.SeedContentIDs()
	builtAt, err := e.repo.Now(ctx)
	if err != nil {
		return res, err
	}
	for _, mediaType := range externalMediaTypes {
		sims, err := e.repo.ExternalSimilarities(ctx, taste, lock.Model, mediaType, ids[mediaType])
		if err != nil {
			return res, err
		}
		items := rankExternalCandidates(sims, pool, seedContentIDs)
		if len(items) == 0 {
			n, err := writer.deleteExternalRow(ctx, userID, profileID, mediaType)
			if err != nil {
				return res, err
			}
			res.Deleted += int(n)
			continue
		}
		written, err := writer.writeExternalRowUnderLock(ctx, ExternalRow{
			UserID: userID, ProfileID: profileID, MediaType: mediaType,
			Items: items, EmbeddingModel: lock.Model, BuiltAt: builtAt,
		}, lockVersion)
		if err != nil {
			return res, err
		}
		if written {
			res.Written++
		} else {
			res.Refused++
		}
	}
	return res, nil
}

// externalRowLock returns the stored embedding lock and the version of its
// row, or nil when none is stored. The version, not the lock's JSON, names
// the lock a row was ranked under: a reset followed by an embedding run with
// the same model writes byte-identical JSON in a new row.
func (r *Repo) externalRowLock(ctx context.Context) (string, *EmbeddingLock, error) {
	lock, err := ReadEmbeddingLock(ctx, r.pool)
	if err != nil || lock == nil {
		return "", nil, err
	}
	return lock.rowVersion, lock, nil
}

// writeExternalRowUnderLock writes row in one statement when the stored
// embedding lock's row is still lockVersion (see externalRowLock), and
// reports whether it did. The statement share-locks the lock's row: an
// embeddings reset that deleted it first makes the write wait for the reset
// and then find no lock, and a write that locked it first commits before the
// reset deletes the lock, so the reset's later delete of every outside row
// removes this one too.
func (w externalPoolWriter) writeExternalRowUnderLock(ctx context.Context, row ExternalRow, lockVersion string) (bool, error) {
	items := row.Items
	if items == nil {
		items = []ExternalRowItem{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return false, fmt.Errorf("marshal outside row: %w", err)
	}
	tag, err := w.db.Exec(ctx, `
		INSERT INTO external_recommendation_rows (user_id, profile_id, media_type, items, embedding_model, built_at)
		SELECT $1, $2, $3, $4, $5, $6
		FROM   server_settings s
		WHERE  s.key = $7 AND s.xmin::text || ':' || s.ctid::text = $8
		FOR SHARE OF s
		ON CONFLICT (user_id, profile_id, media_type) DO UPDATE
			SET items = EXCLUDED.items, embedding_model = EXCLUDED.embedding_model, built_at = EXCLUDED.built_at`,
		row.UserID, row.ProfileID, row.MediaType, raw, row.EmbeddingModel, row.BuiltAt, embeddingLockSettingKey, lockVersion)
	if err != nil {
		return false, fmt.Errorf("write outside row for user %d profile %s %s: %w", row.UserID, row.ProfileID, row.MediaType, err)
	}
	return tag.RowsAffected() > 0, nil
}

// deleteExternalRow deletes a profile's row of one media type.
func (w externalPoolWriter) deleteExternalRow(ctx context.Context, userID int, profileID, mediaType string) (int64, error) {
	tag, err := w.db.Exec(ctx, `
		DELETE FROM external_recommendation_rows
		WHERE  user_id = $1 AND profile_id = $2 AND media_type = $3`, userID, profileID, mediaType)
	if err != nil {
		return 0, fmt.Errorf("delete outside row for user %d profile %s %s: %w", userID, profileID, mediaType, err)
	}
	return tag.RowsAffected(), nil
}

// externalRowSubjects lists the profiles the outside rows are rebuilt for:
// those with a taste vector, and those holding rows a rebuild may delete.
func (r *Repo) externalRowSubjects(ctx context.Context) ([]StaleProfile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, profile_id FROM user_taste_profiles WHERE embedding IS NOT NULL
		UNION
		SELECT user_id, profile_id FROM external_recommendation_rows
		ORDER  BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list outside row subjects: %w", err)
	}
	profiles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StaleProfile, error) {
		var p StaleProfile
		err := row.Scan(&p.UserID, &p.ProfileID)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("list outside row subjects: %w", err)
	}
	return profiles, nil
}

// forgetExternalTitles deletes the links that name keys, and their stored
// titles, after TMDB answered that it does not know them. A source names
// them again only when it is refetched and TMDB still lists them, so a
// missing title is not asked for on every run.
func (w externalPoolWriter) forgetExternalTitles(ctx context.Context, keys []ExternalTitleKey) error {
	if len(keys) == 0 {
		return nil
	}
	types, ids := externalKeyArrays(slices.SortedFunc(slices.Values(keys), compareExternalKeys))
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin forgetting outside titles: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`DELETE FROM external_title_links l
		 USING  unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
		 WHERE  l.media_type = k.media_type AND l.tmdb_id = k.tmdb_id`,
		`DELETE FROM external_titles t
		 USING  unnest($1::text[], $2::int[]) AS k(media_type, tmdb_id)
		 WHERE  t.media_type = k.media_type AND t.tmdb_id = k.tmdb_id`,
	} {
		if _, err := tx.Exec(ctx, stmt, types, ids); err != nil {
			return fmt.Errorf("forget outside titles: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit forgetting outside titles: %w", err)
	}
	return nil
}

// externalRowTitle is one item of a stored row with its title's card data.
type externalRowTitle struct {
	item  ExternalRowItem
	title ExternalTitle
}

// externalRowTitles reads a profile's row of one media type with each item's
// stored title, in row order, in one query, and returns the distinct reason
// content IDs. Items whose title retention deleted are left out.
func (r *Repo) externalRowTitles(ctx context.Context, userID int, profileID, mediaType string) ([]externalRowTitle, []string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT i.item, t.media_type, t.tmdb_id, t.title, t.overview, t.poster_path, t.backdrop_path,
		       t.release_date, t.original_language, t.popularity, t.vote_average, t.vote_count,
		       t.us_certification, t.detail_fetched_at, t.imdb_id, t.tvdb_id
		FROM   external_recommendation_rows r
		CROSS  JOIN LATERAL jsonb_array_elements(r.items) WITH ORDINALITY AS i(item, ord)
		JOIN   external_titles t ON t.media_type = r.media_type AND t.tmdb_id = (i.item->>'tmdb_id')::int
		WHERE  r.user_id = $1 AND r.profile_id = $2 AND r.media_type = $3
		ORDER  BY i.ord`, userID, profileID, mediaType)
	if err != nil {
		return nil, nil, fmt.Errorf("read outside row for user %d profile %s %s: %w", userID, profileID, mediaType, err)
	}
	defer rows.Close()
	var out []externalRowTitle
	var reasons []string
	seen := map[string]struct{}{}
	for rows.Next() {
		var rt externalRowTitle
		var raw []byte
		var fetched *time.Time
		t := &rt.title
		if err := rows.Scan(&raw, &t.Key.MediaType, &t.Key.TMDBID, &t.Title, &t.Overview, &t.PosterPath, &t.BackdropPath,
			&t.ReleaseDate, &t.OriginalLanguage, &t.Popularity, &t.VoteAverage, &t.VoteCount,
			&t.USCertification, &fetched, &t.IMDbID, &t.TVDBID); err != nil {
			return nil, nil, fmt.Errorf("scan outside row item: %w", err)
		}
		if fetched != nil {
			t.DetailFetchedAt = *fetched
		}
		if err := json.Unmarshal(raw, &rt.item); err != nil {
			return nil, nil, fmt.Errorf("decode outside row item: %w", err)
		}
		for _, id := range rt.item.Reasons {
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				reasons = append(reasons, id)
			}
		}
		out = append(out, rt)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read outside row for user %d profile %s %s: %w", userID, profileID, mediaType, err)
	}
	return out, reasons, nil
}

// recommendableItemTitles returns the title of each of itemIDs that filter
// admits, as FilterRecommendableItemIDs admits them, keyed by content ID.
func (r *Repo) recommendableItemTitles(ctx context.Context, itemIDs []string, filter catalog.AccessFilter) (map[string]string, error) {
	out := map[string]string{}
	where, args, ok := itemAccessConditions(itemIDs, filter, recommendableMediaTypes)
	if !ok {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT mi.content_id, COALESCE(mi.title, '')
		FROM   media_items mi
		WHERE  `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read accessible item titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("scan accessible item title: %w", err)
		}
		out[id] = title
	}
	return out, rows.Err()
}

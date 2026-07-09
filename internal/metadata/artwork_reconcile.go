package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ArtworkObjectChecker is the S3 surface the reconciler needs: existence
// checks against the public asset bucket. Satisfied by *s3client.Client.
type ArtworkObjectChecker interface {
	ObjectExists(ctx context.Context, bucket, key string) (bool, error)
	Bucket() string
}

// nonProviderImageSchemesSQL mirrors isNonProviderImageScheme for use inside
// SQL predicates: source paths with these schemes cannot be re-downloaded.
const nonProviderImageSchemesSQL = `ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']`

const (
	artworkReconcileSampleTarget = 200
	artworkReconcileBatchSize    = 500
	artworkReconcileHeadWorkers  = 16
	artworkReconcileHeadTimeout  = 15 * time.Second
	artworkReconcileErrorBudget  = 200
	// artworkReconcileBulkThreshold: when at least this fraction of sampled
	// objects is missing, skip per-row verification for the large regenerable
	// surfaces and reset every cached row.
	artworkReconcileBulkThreshold = 0.95
	// artworkReconcileBulkMinSample: bulk reset additionally requires this
	// many *successful* probe samples. A probe degraded by transport errors
	// (errored requests are excluded from the sample) must not bulk-reset the
	// catalog off a handful of surviving 404s; small catalogs below this bar
	// simply take the per-row verify path, which is cheap at that size.
	artworkReconcileBulkMinSample = 25
)

// ArtworkReconcileStats summarizes one reconcile run.
type ArtworkReconcileStats struct {
	Mode          string `json:"mode"` // "verify" or "bulk_reset"
	Sampled       int    `json:"sampled"`
	SampleMissing int    `json:"sample_missing"`
	Checked       int    `json:"checked"`
	Verified      int    `json:"verified"`
	Requeued      int    `json:"requeued"` // reset to provider source; re-cached by the image cache pipeline
	Cleared       int    `json:"cleared"`  // no re-downloadable source; refilled by scans/enrichment or re-uploaded by an admin
	Errors        int    `json:"errors"`
	// SweepErrors is the subset of Errors from the sweep itself (skipped
	// rows). Probe errors don't reduce sweep completeness — probed keys are
	// re-checked by the sweep — so callers deciding whether the reconcile
	// fully covered the catalog must look here, not at Errors.
	SweepErrors int `json:"sweep_errors"`
}

// artworkSweepSurface describes one cached-path column the reconciler sweeps.
type artworkSweepSurface struct {
	name    string
	table   string
	keyCols []string // pagination key expressions; must form a unique order
	pathCol string
	// sourceCol holds the original source the row can be reset to. Empty for
	// surfaces without a re-downloadable source; their rows are always cleared.
	sourceCol string
	// clearSet is the SQL SET fragment applied when a row has no usable
	// source: it must clear pathCol and whatever companion state the owning
	// pipeline needs to refill the image.
	clearSet string
	// alwaysVerify forces per-row HEAD verification even in bulk-reset mode.
	// Used for small tables holding admin/user uploads, where a blind reset
	// would discard the last pointer to an object that survived migration.
	alwaysVerify bool
}

func (s artworkSweepSurface) cachedPredicate() string {
	return fmt.Sprintf(
		`coalesce(%s, '') NOT IN ('', '-') AND %s NOT LIKE '%%://%%'`,
		s.pathCol, s.pathCol,
	)
}

func (s artworkSweepSurface) remoteSourcePredicate() string {
	if s.sourceCol == "" {
		return "FALSE"
	}
	return fmt.Sprintf(
		`coalesce(%s, '') LIKE '%%://%%' AND lower(%s) NOT LIKE ALL (%s)`,
		s.sourceCol, s.sourceCol, nonProviderImageSchemesSQL,
	)
}

func (s artworkSweepSurface) resetSet() string {
	return fmt.Sprintf(`%s = %s, updated_at = NOW()`, s.pathCol, s.sourceCol)
}

// artworkSweepSurfaces lists every cached-artwork destination in the public
// bucket that lives in a plain table column.
//
// The metadata surfaces are kept in sync with EnqueueExistingProviderArtwork:
// resetting a path column here is what makes that query pick the row up
// again. Clearing media_items artwork also nulls last_refreshed so the book
// enrichment sweeps (which require last_refreshed IS NULL) re-extract
// embedded covers.
//
// Chapter thumbnails (JSONB on media_files) and branding assets
// (server_settings refs) have bespoke sweeps and are not listed here.
func artworkSweepSurfaces() []artworkSweepSurface {
	itemClear := func(pathCol string) string {
		return fmt.Sprintf(`%s = '', last_refreshed = NULL, updated_at = NOW()`, pathCol)
	}
	plainClear := func(pathCol string) string {
		return fmt.Sprintf(`%s = '', updated_at = NOW()`, pathCol)
	}
	return []artworkSweepSurface{
		{name: "item posters", table: "media_items", keyCols: []string{"content_id"}, pathCol: "poster_path", sourceCol: "poster_source_path", clearSet: itemClear("poster_path")},
		{name: "item backdrops", table: "media_items", keyCols: []string{"content_id"}, pathCol: "backdrop_path", sourceCol: "backdrop_source_path", clearSet: itemClear("backdrop_path")},
		{name: "item logos", table: "media_items", keyCols: []string{"content_id"}, pathCol: "logo_path", sourceCol: "logo_source_path", clearSet: itemClear("logo_path")},
		{name: "localized item posters", table: "media_item_localizations", keyCols: []string{"content_id", "language"}, pathCol: "poster_path", sourceCol: "poster_source_path", clearSet: plainClear("poster_path")},
		{name: "localized item backdrops", table: "media_item_localizations", keyCols: []string{"content_id", "language"}, pathCol: "backdrop_path", sourceCol: "backdrop_source_path", clearSet: plainClear("backdrop_path")},
		{name: "localized item logos", table: "media_item_localizations", keyCols: []string{"content_id", "language"}, pathCol: "logo_path", sourceCol: "logo_source_path", clearSet: plainClear("logo_path")},
		{name: "season posters", table: "seasons", keyCols: []string{"content_id"}, pathCol: "poster_path", sourceCol: "poster_source_path", clearSet: plainClear("poster_path")},
		{name: "localized season posters", table: "season_localizations", keyCols: []string{"season_content_id", "language"}, pathCol: "poster_path", sourceCol: "poster_source_path", clearSet: plainClear("poster_path")},
		{name: "episode stills", table: "episodes", keyCols: []string{"content_id"}, pathCol: "still_path", sourceCol: "still_source_path", clearSet: plainClear("still_path")},
		{name: "person photos", table: "people", keyCols: []string{"id::text"}, pathCol: "photo_path", sourceCol: "photo_source_path", clearSet: plainClear("photo_path")},

		// Admin/user uploads: no re-downloadable source. Clearing falls back
		// to the generated collage (admin collections), the generated poster
		// (user collections), or the default tile (library posters); admins
		// re-upload anything they want back. alwaysVerify protects surviving
		// uploads from blind bulk resets.
		{name: "collection posters", table: "library_collections", keyCols: []string{"id"}, pathCol: "poster_url", clearSet: `poster_url = '', poster_thumbhash = '', poster_auto_generated = FALSE, poster_from_template = FALSE, updated_at = NOW()`, alwaysVerify: true},
		{name: "collection backdrops", table: "library_collections", keyCols: []string{"id"}, pathCol: "backdrop_url", clearSet: `backdrop_url = '', backdrop_thumbhash = '', updated_at = NOW()`, alwaysVerify: true},
		{name: "user collection posters", table: "user_personal_collections", keyCols: []string{"id"}, pathCol: "poster_url", clearSet: `poster_url = '', poster_thumbhash = '', updated_at = NOW()`, alwaysVerify: true},
		{name: "library posters", table: "media_folders", keyCols: []string{"id::text"}, pathCol: "poster_path", clearSet: `poster_path = ''`, alwaysVerify: true},
	}
}

// ArtworkCacheReconciler verifies cached artwork keys against the public S3
// bucket and resets rows whose objects are missing, so the existing pipelines
// (image cache queue, book enrichment, chapter thumbnail backfill, collection
// collage generation) rebuild them in the currently configured storage.
type ArtworkCacheReconciler struct {
	pool *pgxpool.Pool
	s3   ArtworkObjectChecker
}

func NewArtworkCacheReconciler(pool *pgxpool.Pool, s3 ArtworkObjectChecker) *ArtworkCacheReconciler {
	if pool == nil || s3 == nil {
		return nil
	}
	return &ArtworkCacheReconciler{pool: pool, s3: s3}
}

// Run executes a full reconcile: probe, then either a bulk reset or a
// per-row verification sweep. It returns an error (leaving the storage
// fingerprint untouched at the caller) when storage cannot be reached or the
// error budget is exhausted, and never resets rows on the basis of transport
// errors.
func (r *ArtworkCacheReconciler) Run(ctx context.Context, progress func(percent float64, message string)) (ArtworkReconcileStats, error) {
	stats := ArtworkReconcileStats{Mode: "verify"}
	if r == nil || r.pool == nil || r.s3 == nil {
		return stats, fmt.Errorf("artwork reconcile: not configured")
	}
	if progress == nil {
		progress = func(float64, string) {}
	}

	surfaces := artworkSweepSurfaces()

	// Probe before anything else: it decides the mode, and in bulk mode the
	// per-surface count(*) queries (full scans on unindexable predicates)
	// are never needed — bulk resets report their own RowsAffected.
	progress(0, "Probing object storage")
	if err := r.probe(ctx, surfaces, &stats); err != nil {
		return stats, err
	}
	if stats.Sampled == 0 {
		progress(100, "No cached artwork to verify")
		return stats, nil
	}

	if shouldBulkReset(stats.Sampled, stats.SampleMissing) {
		stats.Mode = "bulk_reset"
		progress(5, fmt.Sprintf("Probe found %d/%d objects missing; resetting cached artwork", stats.SampleMissing, stats.Sampled))
		steps := len(surfaces) + 1
		for i, s := range surfaces {
			pct := 5 + 90*float64(i+1)/float64(steps)
			if s.alwaysVerify {
				// Small upload-holding tables: never blind-reset; a surviving
				// upload's row is the last pointer to its object.
				if err := r.sweepSurface(ctx, s, &stats, func(done int) {
					progress(pct, fmt.Sprintf("Verifying %s (%d rows)", s.name, done))
				}); err != nil {
					return stats, err
				}
				continue
			}
			if err := r.bulkResetSurface(ctx, s, &stats); err != nil {
				return stats, err
			}
			progress(pct, fmt.Sprintf("Reset %s", s.name))
		}
		if err := r.bulkResetChapterThumbnails(ctx, &stats); err != nil {
			return stats, err
		}
		progress(95, "Reset chapter thumbnails")
		return stats, nil
	}

	// Verify mode: count cached rows once so progress has a denominator.
	progress(2, "Counting cached artwork")
	totals := make([]int, len(surfaces))
	total := 0
	for i, s := range surfaces {
		n, err := r.countCached(ctx, s)
		if err != nil {
			return stats, err
		}
		totals[i] = n
		total += n
	}
	chapterTotal, err := r.countChapterThumbnailFiles(ctx)
	if err != nil {
		return stats, err
	}
	total += chapterTotal
	if total == 0 {
		progress(100, "No cached artwork to verify")
		return stats, nil
	}

	done := 0
	report := func(surfaceName string) func(int) {
		return func(surfaceDone int) {
			pct := 5 + 90*float64(done+surfaceDone)/float64(total)
			progress(pct, fmt.Sprintf("Verifying %s (%d/%d overall)", surfaceName, done+surfaceDone, total))
		}
	}

	for i, s := range surfaces {
		if totals[i] == 0 {
			continue
		}
		if err := r.sweepSurface(ctx, s, &stats, report(s.name)); err != nil {
			return stats, err
		}
		done += totals[i]
	}

	if chapterTotal > 0 {
		if err := r.sweepChapterThumbnails(ctx, &stats, report("chapter thumbnails")); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// shouldBulkReset decides between a blind bulk reset and per-row
// verification. Probe HEADs are ground truth, so a near-total miss rate
// means the bucket plainly does not hold the cache; the threshold is below
// 1.0 only so a handful of coincidentally-present keys cannot force millions
// of pointless per-row checks. The minimum-sample bar keeps a probe thinned
// out by transport errors (or a tiny catalog) on the safe per-row path.
func shouldBulkReset(sampled, missing int) bool {
	return sampled >= artworkReconcileBulkMinSample &&
		float64(missing) >= artworkReconcileBulkThreshold*float64(sampled)
}

func (r *ArtworkCacheReconciler) countCached(ctx context.Context, s artworkSweepSurface) (int, error) {
	var n int
	q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s`, s.table, s.cachedPredicate())
	if err := r.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("artwork reconcile: counting %s: %w", s.name, err)
	}
	return n, nil
}

// probe samples cached keys across all surfaces and HEADs them. A probe where
// every request errors aborts the run (storage unreachable ≠ objects missing).
func (r *ArtworkCacheReconciler) probe(ctx context.Context, surfaces []artworkSweepSurface, stats *ArtworkReconcileStats) error {
	perSurface := artworkReconcileSampleTarget / (len(surfaces) + 1)
	if perSurface < 1 {
		perSurface = 1
	}
	// Plain LIMIT sampling (no ORDER BY random(), which would full-scan and
	// sort every surface): the probe only has to answer "does the bucket
	// hold this cache at all", and any N stored keys answer that. Partial
	// migrations that skew the sample simply land in per-row verify mode,
	// which handles them correctly anyway.
	var keys []string
	for _, s := range surfaces {
		q := fmt.Sprintf(
			`SELECT %s FROM %s WHERE %s LIMIT $1`,
			s.pathCol, s.table, s.cachedPredicate(),
		)
		sampled, err := r.queryStrings(ctx, q, perSurface)
		if err != nil {
			return fmt.Errorf("artwork reconcile: sampling %s: %w", s.name, err)
		}
		keys = append(keys, sampled...)
	}
	chapterKeys, err := r.queryStrings(ctx, `
		SELECT e->>'thumbnail_path'
		FROM media_files, jsonb_array_elements(chapters) e
		WHERE chapters IS NOT NULL AND coalesce(e->>'thumbnail_path', '') <> ''
		LIMIT $1
	`, perSurface)
	if err != nil {
		return fmt.Errorf("artwork reconcile: sampling chapter thumbnails: %w", err)
	}
	keys = append(keys, chapterKeys...)
	if len(keys) == 0 {
		return nil
	}

	present, missing, errored := r.headBatch(ctx, keys)
	stats.Sampled = present + missing
	stats.SampleMissing = missing
	stats.Errors += errored
	// A probe that mostly errors is not a probe of the cache, it is a probe
	// of an outage: errored requests are excluded from the sample, so acting
	// on the survivors could bulk-reset the catalog off a handful of 404s.
	// Abort and leave the fingerprint stale; the next startup retries.
	if errored*2 > len(keys) {
		return fmt.Errorf("artwork reconcile: object storage unreliable: %d/%d probe requests failed", errored, len(keys))
	}
	return nil
}

func (r *ArtworkCacheReconciler) queryStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// headBatch checks the given keys with bounded concurrency and returns
// (present, missing, errored) counts.
func (r *ArtworkCacheReconciler) headBatch(ctx context.Context, keys []string) (present, missing, errored int) {
	verdicts := r.headKeys(ctx, keys)
	for _, v := range verdicts {
		switch {
		case v.err != nil:
			errored++
		case v.missing:
			missing++
		default:
			present++
		}
	}
	return present, missing, errored
}

type headVerdict struct {
	missing bool
	err     error
}

// headKeys HEADs every key with bounded concurrency, preserving order.
func (r *ArtworkCacheReconciler) headKeys(ctx context.Context, keys []string) []headVerdict {
	bucket := r.s3.Bucket()
	verdicts := make([]headVerdict, len(keys))
	var wg sync.WaitGroup
	sem := make(chan struct{}, artworkReconcileHeadWorkers)
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			exists, err := r.objectExistsWithRetry(ctx, bucket, key)
			verdicts[i] = headVerdict{missing: err == nil && !exists, err: err}
		}(i, key)
	}
	wg.Wait()
	return verdicts
}

func (r *ArtworkCacheReconciler) objectExistsWithRetry(ctx context.Context, bucket, key string) (bool, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Per-attempt deadline: a stalled HEAD must fail this attempt and
		// move on, not hold the retry loop open until the run's context dies.
		attemptCtx, cancel := context.WithTimeout(ctx, artworkReconcileHeadTimeout)
		exists, err := r.s3.ObjectExists(attemptCtx, bucket, key)
		cancel()
		if err == nil {
			return exists, nil
		}
		lastErr = err
		if attempt == maxAttempts-1 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		}
	}
	return false, lastErr
}

// bulkResetSurface resets every cached row without per-row verification. Rows
// with a re-downloadable provider source go back to that source (the enqueue
// loop re-caches them); rows without one are cleared so their owning pipeline
// can refill them.
func (r *ArtworkCacheReconciler) bulkResetSurface(ctx context.Context, s artworkSweepSurface, stats *ArtworkReconcileStats) error {
	if s.sourceCol != "" {
		requeue := fmt.Sprintf(
			`UPDATE %s SET %s WHERE %s AND %s`,
			s.table, s.resetSet(), s.cachedPredicate(), s.remoteSourcePredicate(),
		)
		tag, err := r.pool.Exec(ctx, requeue)
		if err != nil {
			return fmt.Errorf("artwork reconcile: bulk reset %s: %w", s.name, err)
		}
		stats.Requeued += int(tag.RowsAffected())
		stats.Checked += int(tag.RowsAffected())
	}

	clearSQL := fmt.Sprintf(
		`UPDATE %s SET %s WHERE %s AND NOT (%s)`,
		s.table, s.clearSet, s.cachedPredicate(), s.remoteSourcePredicate(),
	)
	tag, err := r.pool.Exec(ctx, clearSQL)
	if err != nil {
		return fmt.Errorf("artwork reconcile: bulk clear %s: %w", s.name, err)
	}
	stats.Cleared += int(tag.RowsAffected())
	stats.Checked += int(tag.RowsAffected())
	return nil
}

// sweptRow is one candidate row in the per-row verification sweep.
type sweptRow struct {
	keys         []string
	path         string
	remoteSource bool
}

func (r *ArtworkCacheReconciler) sweepSurface(ctx context.Context, s artworkSweepSurface, stats *ArtworkReconcileStats, onProgress func(done int)) error {
	var cursor []string
	done := 0
	for {
		rows, err := r.fetchSweepBatch(ctx, s, cursor)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		cursor = rows[len(rows)-1].keys

		if err := r.verifyAndReset(ctx, s, rows, stats); err != nil {
			return err
		}
		if stats.SweepErrors > artworkReconcileErrorBudget {
			return fmt.Errorf("artwork reconcile: aborting after %d sweep storage errors (errored rows were left untouched)", stats.SweepErrors)
		}
		done += len(rows)
		onProgress(done)
	}
}

func (r *ArtworkCacheReconciler) fetchSweepBatch(ctx context.Context, s artworkSweepSurface, cursor []string) ([]sweptRow, error) {
	var b strings.Builder
	args := make([]any, 0, len(cursor)+1)
	fmt.Fprintf(&b, `SELECT %s, %s, (%s) FROM %s WHERE %s`,
		strings.Join(s.keyCols, ", "), s.pathCol, s.remoteSourcePredicate(), s.table, s.cachedPredicate())
	if len(cursor) > 0 {
		placeholders := make([]string, len(cursor))
		for i, v := range cursor {
			args = append(args, v)
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		fmt.Fprintf(&b, ` AND (%s) > (%s)`, strings.Join(s.keyCols, ", "), strings.Join(placeholders, ", "))
	}
	args = append(args, artworkReconcileBatchSize)
	fmt.Fprintf(&b, ` ORDER BY %s LIMIT $%d`, strings.Join(s.keyCols, ", "), len(args))

	rows, err := r.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("artwork reconcile: fetching %s batch: %w", s.name, err)
	}
	defer rows.Close()

	out := make([]sweptRow, 0, artworkReconcileBatchSize)
	for rows.Next() {
		row := sweptRow{keys: make([]string, len(s.keyCols))}
		dest := make([]any, 0, len(s.keyCols)+2)
		for i := range row.keys {
			dest = append(dest, &row.keys[i])
		}
		dest = append(dest, &row.path, &row.remoteSource)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("artwork reconcile: scanning %s batch: %w", s.name, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artwork reconcile: iterating %s batch: %w", s.name, err)
	}
	return out, nil
}

func (r *ArtworkCacheReconciler) verifyAndReset(ctx context.Context, s artworkSweepSurface, batch []sweptRow, stats *ArtworkReconcileStats) error {
	keys := make([]string, len(batch))
	for i, row := range batch {
		keys[i] = row.path
	}
	verdicts := r.headKeys(ctx, keys)

	pkPredicate := keyEqualityPredicate(s.keyCols)
	var pgBatch pgx.Batch
	remoteByQueued := make([]bool, 0)
	for i, v := range verdicts {
		stats.Checked++
		switch {
		case v.err != nil:
			stats.Errors++
			stats.SweepErrors++
			slog.Warn("artwork reconcile: object check failed; leaving row untouched",
				"surface", s.name, "key", batch[i].path, "error", v.err)
		case v.missing:
			row := batch[i]
			args := make([]any, 0, len(row.keys)+1)
			for _, k := range row.keys {
				args = append(args, k)
			}
			args = append(args, row.path)
			var set string
			if row.remoteSource {
				set = s.resetSet()
			} else {
				set = s.clearSet
				slog.Warn("artwork reconcile: cached image missing with no re-downloadable source; cleared",
					"surface", s.name, "key", row.path, "row", strings.Join(row.keys, "/"))
			}
			pgBatch.Queue(fmt.Sprintf(`UPDATE %s SET %s WHERE %s AND %s = $%d`,
				s.table, set, pkPredicate, s.pathCol, len(args)), args...)
			remoteByQueued = append(remoteByQueued, row.remoteSource)
		default:
			stats.Verified++
		}
	}
	if pgBatch.Len() == 0 {
		return nil
	}
	results := r.pool.SendBatch(ctx, &pgBatch)
	defer func() { _ = results.Close() }()
	for _, remote := range remoteByQueued {
		tag, err := results.Exec()
		if err != nil {
			return fmt.Errorf("artwork reconcile: resetting %s row: %w", s.name, err)
		}
		if tag.RowsAffected() == 0 {
			// Row changed concurrently (metadata refresh, admin edit); leave it alone.
			continue
		}
		if remote {
			stats.Requeued++
		} else {
			stats.Cleared++
		}
	}
	return nil
}

func keyEqualityPredicate(keyCols []string) string {
	parts := make([]string, len(keyCols))
	for i, col := range keyCols {
		parts[i] = fmt.Sprintf("%s = $%d", col, i+1)
	}
	return strings.Join(parts, " AND ")
}

// --- Chapter thumbnails ---------------------------------------------------
//
// Chapter thumbnails live inside the media_files.chapters JSONB array
// (thumbnail_path / thumbnail_thumbhash per element). Clearing the path and
// the retry state makes the scheduled chapter_thumbnail_backfill task
// regenerate them from the media file.

const chapterThumbnailFilesPredicate = `chapters IS NOT NULL AND EXISTS (
	SELECT 1 FROM jsonb_array_elements(chapters) e
	WHERE coalesce(e->>'thumbnail_path', '') <> ''
)`

func (r *ArtworkCacheReconciler) countChapterThumbnailFiles(ctx context.Context) (int, error) {
	var n int
	q := `SELECT count(*) FROM media_files WHERE ` + chapterThumbnailFilesPredicate
	if err := r.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("artwork reconcile: counting chapter thumbnail files: %w", err)
	}
	return n, nil
}

func (r *ArtworkCacheReconciler) bulkResetChapterThumbnails(ctx context.Context, stats *ArtworkReconcileStats) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_files
		SET chapters = (
			SELECT jsonb_agg(
				CASE WHEN coalesce(e->>'thumbnail_path', '') <> ''
					THEN (e - 'thumbnail_retry_after' - 'thumbnail_failed_at' - 'thumbnail_last_error')
						|| '{"thumbnail_path": "", "thumbnail_thumbhash": ""}'::jsonb
					ELSE e
				END
				ORDER BY ord
			)
			FROM jsonb_array_elements(chapters) WITH ORDINALITY AS t(e, ord)
		),
		chapter_thumbnail_retry_after = NULL
		WHERE `+chapterThumbnailFilesPredicate)
	if err != nil {
		return fmt.Errorf("artwork reconcile: bulk clearing chapter thumbnails: %w", err)
	}
	stats.Cleared += int(tag.RowsAffected())
	stats.Checked += int(tag.RowsAffected())
	return nil
}

// chapterFileRow is one media_files row in the chapter thumbnail sweep.
// Chapters are decoded as generic maps so fields this code does not know
// about survive a rewrite.
type chapterFileRow struct {
	id       int64
	raw      []byte
	chapters []map[string]any
}

func (r *ArtworkCacheReconciler) sweepChapterThumbnails(ctx context.Context, stats *ArtworkReconcileStats, onProgress func(done int)) error {
	cursor := int64(0)
	done := 0
	for {
		rows, err := r.pool.Query(ctx, `
			SELECT id, chapters FROM media_files
			WHERE `+chapterThumbnailFilesPredicate+` AND id > $1
			ORDER BY id LIMIT $2
		`, cursor, artworkReconcileBatchSize)
		if err != nil {
			return fmt.Errorf("artwork reconcile: fetching chapter thumbnail batch: %w", err)
		}
		batch := make([]chapterFileRow, 0, artworkReconcileBatchSize)
		for rows.Next() {
			var f chapterFileRow
			if err := rows.Scan(&f.id, &f.raw); err != nil {
				rows.Close()
				return fmt.Errorf("artwork reconcile: scanning chapter thumbnail batch: %w", err)
			}
			batch = append(batch, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("artwork reconcile: iterating chapter thumbnail batch: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		cursor = batch[len(batch)-1].id

		if err := r.reconcileChapterBatch(ctx, batch, stats); err != nil {
			return err
		}
		if stats.SweepErrors > artworkReconcileErrorBudget {
			return fmt.Errorf("artwork reconcile: aborting after %d sweep storage errors (errored rows were left untouched)", stats.SweepErrors)
		}
		done += len(batch)
		onProgress(done)
	}
}

// reconcileChapterBatch verifies every chapter thumbnail across the whole
// batch in one HEAD fan-out — per-file checking would cap effective
// concurrency at one file's handful of chapters — then rewrites only the
// files whose arrays changed.
func (r *ArtworkCacheReconciler) reconcileChapterBatch(ctx context.Context, batch []chapterFileRow, stats *ArtworkReconcileStats) error {
	type chapterRef struct{ file, chapter int }
	var keys []string
	var refs []chapterRef
	for fi := range batch {
		f := &batch[fi]
		if err := json.Unmarshal(f.raw, &f.chapters); err != nil {
			stats.Errors++
			stats.SweepErrors++
			slog.Warn("artwork reconcile: unparseable chapters JSON; skipping file", "file_id", f.id, "error", err)
			f.chapters = nil
			continue
		}
		for ci, ch := range f.chapters {
			path, _ := ch["thumbnail_path"].(string)
			if strings.TrimSpace(path) == "" {
				continue
			}
			keys = append(keys, path)
			refs = append(refs, chapterRef{file: fi, chapter: ci})
		}
	}
	if len(keys) == 0 {
		return nil
	}

	verdicts := r.headKeys(ctx, keys)
	changed := make(map[int]bool, len(batch))
	for vi, v := range verdicts {
		stats.Checked++
		ref := refs[vi]
		switch {
		case v.err != nil:
			stats.Errors++
			stats.SweepErrors++
			slog.Warn("artwork reconcile: chapter thumbnail check failed; leaving chapter untouched",
				"file_id", batch[ref.file].id, "key", keys[vi], "error", v.err)
		case v.missing:
			ch := batch[ref.file].chapters[ref.chapter]
			ch["thumbnail_path"] = ""
			ch["thumbnail_thumbhash"] = ""
			delete(ch, "thumbnail_retry_after")
			delete(ch, "thumbnail_failed_at")
			delete(ch, "thumbnail_last_error")
			changed[ref.file] = true
			stats.Cleared++
		default:
			stats.Verified++
		}
	}

	for fi := range batch {
		if !changed[fi] {
			continue
		}
		f := batch[fi]
		updated, err := json.Marshal(f.chapters)
		if err != nil {
			return fmt.Errorf("artwork reconcile: encoding chapters for file %d: %w", f.id, err)
		}
		// Guard on the original JSON so a concurrent thumbnail-service write wins.
		tag, err := r.pool.Exec(ctx, `
			UPDATE media_files
			SET chapters = $1::jsonb, chapter_thumbnail_retry_after = NULL
			WHERE id = $2 AND chapters = $3::jsonb
		`, updated, f.id, f.raw)
		if err != nil {
			return fmt.Errorf("artwork reconcile: updating chapters for file %d: %w", f.id, err)
		}
		if tag.RowsAffected() == 0 {
			slog.Debug("artwork reconcile: chapters changed concurrently; skipped", "file_id", f.id)
		}
	}
	return nil
}

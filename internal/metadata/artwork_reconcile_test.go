package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeObjectChecker treats every key as present unless listed in missing or
// erroring. Defaulting to present keeps sweeps over rows seeded by other
// tests in the shared test database side-effect free.
type fakeObjectChecker struct {
	mu       sync.Mutex
	missing  map[string]bool
	erroring map[string]bool
	errorAll bool
	checked  map[string]int
}

func (f *fakeObjectChecker) Bucket() string { return "test-bucket" }

func (f *fakeObjectChecker) ObjectExists(_ context.Context, _ string, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checked == nil {
		f.checked = map[string]int{}
	}
	f.checked[key]++
	if f.errorAll || f.erroring[key] {
		return false, errors.New("simulated storage error")
	}
	return !f.missing[key], nil
}

func TestShouldBulkReset(t *testing.T) {
	if shouldBulkReset(0, 0) {
		t.Fatal("empty probe must not trigger bulk reset")
	}
	if shouldBulkReset(100, 94) {
		t.Fatal("94% missing is below the bulk threshold")
	}
	if !shouldBulkReset(100, 95) {
		t.Fatal("95% missing must trigger bulk reset")
	}
	// A probe thinned below the minimum successful-sample bar (transport
	// errors, tiny catalog) must take the safe per-row path even at a 100%
	// miss rate — a handful of surviving 404s is not a mandate to bulk-reset.
	if shouldBulkReset(artworkReconcileBulkMinSample-1, artworkReconcileBulkMinSample-1) {
		t.Fatal("below-minimum sample must not trigger bulk reset")
	}
	if !shouldBulkReset(artworkReconcileBulkMinSample, artworkReconcileBulkMinSample) {
		t.Fatal("all-missing probe at the minimum sample size must trigger bulk reset")
	}
}

func TestArtworkSweepSurfacesUseIndexablePaginationKeys(t *testing.T) {
	for _, surface := range artworkSweepSurfaces() {
		for _, keyCol := range surface.keyCols {
			if strings.Contains(keyCol.column, "::") {
				t.Fatalf("surface %q paginates on expression %q; use the native key column so PostgreSQL can use its index", surface.name, keyCol.column)
			}
		}
	}
}

func TestBuildSweepBatchQueryUsesNativeNumericKeys(t *testing.T) {
	var peopleSurface, folderSurface artworkSweepSurface
	for _, surface := range artworkSweepSurfaces() {
		switch surface.name {
		case "person photos":
			peopleSurface = surface
		case "library posters":
			folderSurface = surface
		}
	}
	if peopleSurface.name == "" || folderSurface.name == "" {
		t.Fatalf("surface lookup failed: person photos=%q library posters=%q", peopleSurface.name, folderSurface.name)
	}

	query, args, err := buildSweepBatchQuery(peopleSurface, []string{"128111764822294558"})
	if err != nil {
		t.Fatalf("build people query: %v", err)
	}
	if !strings.Contains(query, "SELECT (id)::text") ||
		!strings.Contains(query, "AND (id) > ($1)") ||
		!strings.Contains(query, "ORDER BY id LIMIT $2") {
		t.Fatalf("people query does not keep pagination on native id: %s", query)
	}
	if _, ok := args[0].(int64); !ok {
		t.Fatalf("people cursor type = %T, want int64", args[0])
	}

	_, args, err = buildSweepBatchQuery(folderSurface, []string{"42"})
	if err != nil {
		t.Fatalf("build folder query: %v", err)
	}
	if _, ok := args[0].(int32); !ok {
		t.Fatalf("folder cursor type = %T, want int32", args[0])
	}
}

func TestArtworkReconcileVerifySweep(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	id := func(name string) string { return fmt.Sprintf("arc-%s-%d", name, suffix) }
	key := func(name string) string { return fmt.Sprintf("tmdb/movies/arc-%d/%s/original.webp", suffix, name) }

	// Four items covering the sweep verdicts: intact, missing with a provider
	// source, missing with an upload source, and an uncached provider URL.
	seedItem := func(contentID, posterPath, posterSource string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path, last_refreshed)
			VALUES ($1, 'movie', 'ARC Test', 'matched', '{}'::text[], $2, $3, NOW())
		`, contentID, posterPath, posterSource); err != nil {
			t.Fatalf("seed item %s: %v", contentID, err)
		}
	}
	seedItem(id("intact"), key("intact"), "https://img.example/intact.jpg")
	seedItem(id("missing"), key("missing"), "https://img.example/missing.jpg")
	seedItem(id("upload"), key("upload"), "upload://admin/poster.jpg")
	seedItem(id("uncached"), "https://img.example/direct.jpg", "https://img.example/direct.jpg")

	personID := suffix
	personKey := key("person-missing")
	if _, err := pool.Exec(ctx, `
		INSERT INTO people (id, name, photo_path, photo_source_path)
		VALUES ($1, 'ARC Person', $2, 'https://img.example/person.jpg')
	`, personID, personKey); err != nil {
		t.Fatalf("seed person: %v", err)
	}

	var fileID int64
	chapters := fmt.Sprintf(
		`[{"index":0,"title":"One","thumbnail_path":%q,"thumbnail_thumbhash":"aGFzaA==","custom":"kept"},`+
			`{"index":1,"title":"Two","thumbnail_path":%q,"thumbnail_thumbhash":"aGFzaA==","thumbnail_failed_at":"2026-01-01T00:00:00Z"}]`,
		key("chapter-intact"), key("chapter-missing"),
	)
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled, poster_path) VALUES ('movies', 'ARC Folder', true, $1) RETURNING id`,
		fmt.Sprintf("library-posters/arc-%d.png", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, chapters)
		VALUES ($1, $2, $3, $4::jsonb) RETURNING id
	`, id("intact"), folderID, fmt.Sprintf("/arc-%d/movie.mkv", suffix), chapters).Scan(&fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_collections (id, library_id, slug, title, collection_type, poster_url, poster_thumbhash, poster_from_template)
		VALUES ($1, $2, $1, 'ARC Collection', 'manual', $3, 'aGFzaA==', TRUE)
	`, id("coll"), folderID, key("coll")); err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM library_collections WHERE id = $1`, id("coll"))
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = $1`, personID)
		for _, name := range []string{"intact", "missing", "upload", "uncached"} {
			_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id(name))
		}
	})

	checker := &fakeObjectChecker{missing: map[string]bool{
		key("missing"):         true,
		key("upload"):          true,
		personKey:              true,
		key("chapter-missing"): true,
		key("coll"):            true,
		fmt.Sprintf("library-posters/arc-%d.png", suffix): true,
	}}
	stats, err := NewArtworkCacheReconciler(pool, checker).Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Mode != "verify" {
		t.Fatalf("Mode = %q, want verify (fake checker defaults to present)", stats.Mode)
	}

	var posterPath string
	var lastRefreshed *time.Time
	mustScanItem := func(contentID string) (string, *time.Time) {
		if err := pool.QueryRow(ctx,
			`SELECT poster_path, last_refreshed FROM media_items WHERE content_id = $1`, contentID,
		).Scan(&posterPath, &lastRefreshed); err != nil {
			t.Fatalf("read item %s: %v", contentID, err)
		}
		return posterPath, lastRefreshed
	}

	if got, _ := mustScanItem(id("intact")); got != key("intact") {
		t.Fatalf("intact poster_path = %q, want untouched %q", got, key("intact"))
	}
	if got, _ := mustScanItem(id("missing")); got != "https://img.example/missing.jpg" {
		t.Fatalf("missing poster_path = %q, want reset to provider source", got)
	}
	if got, refreshed := mustScanItem(id("upload")); got != "" || refreshed != nil {
		t.Fatalf("upload poster_path = %q (last_refreshed %v), want cleared with last_refreshed NULL", got, refreshed)
	}
	if got, _ := mustScanItem(id("uncached")); got != "https://img.example/direct.jpg" {
		t.Fatalf("uncached poster_path = %q, want untouched provider URL", got)
	}
	if checker.checked["https://img.example/direct.jpg"] != 0 {
		t.Fatal("provider URLs must not be HEAD-checked")
	}

	var personPhoto string
	if err := pool.QueryRow(ctx, `SELECT photo_path FROM people WHERE id = $1`, personID).Scan(&personPhoto); err != nil {
		t.Fatalf("read person: %v", err)
	}
	if personPhoto != "https://img.example/person.jpg" {
		t.Fatalf("missing person photo_path = %q, want reset to provider source", personPhoto)
	}

	var rawChapters string
	var retryAfter *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT chapters::text, chapter_thumbnail_retry_after FROM media_files WHERE id = $1`, fileID,
	).Scan(&rawChapters, &retryAfter); err != nil {
		t.Fatalf("read chapters: %v", err)
	}
	assertContains := func(s, substr, what string) {
		t.Helper()
		if !strings.Contains(s, substr) {
			t.Fatalf("%s: %q not found in %s", what, substr, s)
		}
	}
	assertContains(rawChapters, key("chapter-intact"), "intact chapter thumbnail kept")
	assertContains(rawChapters, `"kept"`, "unknown chapter fields preserved")
	if strings.Contains(rawChapters, key("chapter-missing")) || strings.Contains(rawChapters, "thumbnail_failed_at") {
		t.Fatalf("missing chapter thumbnail not cleared: %s", rawChapters)
	}
	if retryAfter != nil {
		t.Fatal("chapter_thumbnail_retry_after not cleared")
	}

	var collPoster, collHash string
	var fromTemplate bool
	if err := pool.QueryRow(ctx,
		`SELECT poster_url, poster_thumbhash, poster_from_template FROM library_collections WHERE id = $1`, id("coll"),
	).Scan(&collPoster, &collHash, &fromTemplate); err != nil {
		t.Fatalf("read collection: %v", err)
	}
	if collPoster != "" || collHash != "" || fromTemplate {
		t.Fatalf("collection artwork not fully cleared: url=%q hash=%q from_template=%v", collPoster, collHash, fromTemplate)
	}

	var folderPoster string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_folders WHERE id = $1`, folderID).Scan(&folderPoster); err != nil {
		t.Fatalf("read folder: %v", err)
	}
	if folderPoster != "" {
		t.Fatalf("library poster not cleared: %q", folderPoster)
	}
}

type scriptedArtworkBatch struct {
	cursor      []string
	done        int
	verified    int
	sweepErrors int
}

type scriptedArtworkChapterBatch struct {
	cursor      int64
	rows        int
	verified    int
	sweepErrors int
}

type scriptedArtworkSweepCall struct {
	name   string
	cursor []string
	done   int
}

type scriptedArtworkChapterSweepCall struct {
	cursor int64
	done   int
}

type scriptedArtworkVerifySweeper struct {
	batches           map[string][]scriptedArtworkBatch
	chapterBatches    []scriptedArtworkChapterBatch
	calls             []scriptedArtworkSweepCall
	chapterCalls      []scriptedArtworkChapterSweepCall
	processed         map[string]int
	chaptersProcessed int
}

func (s *scriptedArtworkVerifySweeper) sweepSurfaceFrom(
	_ context.Context,
	surface artworkSweepSurface,
	stats *ArtworkReconcileStats,
	cursor []string,
	done int,
	onBatch func([]string, int, bool) error,
) error {
	s.calls = append(s.calls, scriptedArtworkSweepCall{
		name:   surface.name,
		cursor: append([]string(nil), cursor...),
		done:   done,
	})
	if s.processed == nil {
		s.processed = make(map[string]int)
	}
	for _, batch := range s.batches[surface.name] {
		s.processed[surface.name]++
		stats.Checked += batch.verified + batch.sweepErrors
		stats.Verified += batch.verified
		stats.Errors += batch.sweepErrors
		stats.SweepErrors += batch.sweepErrors
		if err := onBatch(batch.cursor, batch.done, batch.sweepErrors == 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *scriptedArtworkVerifySweeper) sweepChapterThumbnailsFrom(
	_ context.Context,
	stats *ArtworkReconcileStats,
	cursor int64,
	done int,
	onBatch func(int64, int, bool) error,
) error {
	s.chapterCalls = append(s.chapterCalls, scriptedArtworkChapterSweepCall{cursor: cursor, done: done})
	for _, batch := range s.chapterBatches {
		if batch.cursor <= cursor {
			continue
		}
		s.chaptersProcessed++
		stats.Checked += batch.verified + batch.sweepErrors
		stats.Verified += batch.verified
		stats.Errors += batch.sweepErrors
		stats.SweepErrors += batch.sweepErrors
		done += batch.rows
		if err := onBatch(batch.cursor, done, batch.sweepErrors == 0); err != nil {
			return err
		}
	}
	return nil
}

func TestArtworkReconcileStopsAtUnsafeBatchAndResumesFromLastCheckpoint(t *testing.T) {
	surfaces := []artworkSweepSurface{{name: "posters"}, {name: "later surface"}}
	checkpoint := ArtworkReconcileCheckpoint{
		Version: artworkReconcileCheckpointVersion,
		Totals:  []int{1001, 1},
		Stats:   ArtworkReconcileStats{Mode: ArtworkReconcileModeVerify},
	}
	firstRun := &scriptedArtworkVerifySweeper{batches: map[string][]scriptedArtworkBatch{
		"posters": {
			{cursor: []string{"0499"}, done: 500, verified: 500},
			{cursor: []string{"0999"}, done: 1000, verified: 499, sweepErrors: 1},
			{cursor: []string{"1000"}, done: 1001, verified: 1},
		},
		"later surface": {{cursor: []string{"later"}, done: 1, verified: 1}},
	}}
	var saved ArtworkReconcileCheckpoint
	saveCount := 0
	stats, err := runArtworkVerifySweep(context.Background(), firstRun, surfaces, checkpoint,
		func(next ArtworkReconcileCheckpoint) error {
			saveCount++
			saved = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err == nil || !strings.Contains(err.Error(), "last saved checkpoint") {
		t.Fatalf("first sweep error = %v, want checkpoint stop", err)
	}
	if stats.SweepErrors != 1 {
		t.Fatalf("first sweep errors = %d, want 1", stats.SweepErrors)
	}
	if firstRun.processed["posters"] != 2 || firstRun.processed["later surface"] != 0 || firstRun.chaptersProcessed != 0 {
		t.Fatalf("work after unsafe batch: processed=%v chapter_batches=%d", firstRun.processed, firstRun.chaptersProcessed)
	}
	if saveCount != 1 || saved.SurfaceDone != 500 || len(saved.SurfaceCursor) != 1 || saved.SurfaceCursor[0] != "0499" {
		t.Fatalf("saved checkpoint = %#v (save count %d), want safe first batch", saved, saveCount)
	}

	resumedRun := &scriptedArtworkVerifySweeper{batches: map[string][]scriptedArtworkBatch{
		"posters": {
			{cursor: []string{"0999"}, done: 1000, verified: 500},
			{cursor: []string{"1000"}, done: 1001, verified: 1},
		},
		"later surface": {{cursor: []string{"later"}, done: 1, verified: 1}},
	}}
	var completed ArtworkReconcileCheckpoint
	stats, err = runArtworkVerifySweep(context.Background(), resumedRun, surfaces, saved,
		func(next ArtworkReconcileCheckpoint) error {
			completed = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err != nil {
		t.Fatalf("resumed sweep: %v", err)
	}
	if len(resumedRun.calls) == 0 || resumedRun.calls[0].done != 500 ||
		len(resumedRun.calls[0].cursor) != 1 || resumedRun.calls[0].cursor[0] != "0499" {
		t.Fatalf("resume call = %#v, want cursor 0499 at 500 rows", resumedRun.calls)
	}
	if stats.Verified != 1002 || !completed.Complete() {
		t.Fatalf("resumed stats/checkpoint = %#v / %#v, want 1002 verified and complete", stats, completed)
	}
}

func TestArtworkReconcileStopsAndResumesChapterThumbnailsFromLastCheckpoint(t *testing.T) {
	checkpoint := ArtworkReconcileCheckpoint{
		Version:      artworkReconcileCheckpointVersion,
		ChapterTotal: 3,
		Stats:        ArtworkReconcileStats{Mode: ArtworkReconcileModeVerify},
	}
	firstRun := &scriptedArtworkVerifySweeper{chapterBatches: []scriptedArtworkChapterBatch{
		{cursor: 10, rows: 1, verified: 1},
		{cursor: 20, rows: 1, sweepErrors: 1},
		{cursor: 30, rows: 1, verified: 1},
	}}
	var saved ArtworkReconcileCheckpoint
	saveCount := 0

	stats, err := runArtworkVerifySweep(context.Background(), firstRun, nil, checkpoint,
		func(next ArtworkReconcileCheckpoint) error {
			saveCount++
			saved = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err == nil || !strings.Contains(err.Error(), "last saved checkpoint") {
		t.Fatalf("first chapter sweep error = %v, want checkpoint stop", err)
	}
	if stats.SweepErrors != 1 || firstRun.chaptersProcessed != 2 {
		t.Fatalf("first chapter sweep stats/work = %#v / %d batches, want 1 error after 2 batches", stats, firstRun.chaptersProcessed)
	}
	if saveCount != 1 || saved.ChapterCursor != 10 || saved.ChapterDone != 1 {
		t.Fatalf("saved chapter checkpoint = %#v (save count %d), want cursor 10 at 1 row", saved, saveCount)
	}

	resumedRun := &scriptedArtworkVerifySweeper{chapterBatches: []scriptedArtworkChapterBatch{
		{cursor: 10, rows: 1, verified: 1},
		{cursor: 20, rows: 1, verified: 1},
		{cursor: 30, rows: 1, verified: 1},
	}}
	var completed ArtworkReconcileCheckpoint
	stats, err = runArtworkVerifySweep(context.Background(), resumedRun, nil, saved,
		func(next ArtworkReconcileCheckpoint) error {
			completed = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err != nil {
		t.Fatalf("resumed chapter sweep: %v", err)
	}
	if len(resumedRun.chapterCalls) != 1 || resumedRun.chapterCalls[0].cursor != 10 || resumedRun.chapterCalls[0].done != 1 {
		t.Fatalf("chapter resume call = %#v, want cursor 10 at 1 row", resumedRun.chapterCalls)
	}
	if resumedRun.chaptersProcessed != 2 {
		t.Fatalf("resumed chapter batches = %d, want 2 (saved batch skipped)", resumedRun.chaptersProcessed)
	}
	if stats.Verified != 3 || !completed.Complete() {
		t.Fatalf("resumed chapter stats/checkpoint = %#v / %#v, want 3 verified and complete", stats, completed)
	}
}

func TestArtworkReconcileWithoutSaverContinuesAfterUnsafeBatches(t *testing.T) {
	surfaces := []artworkSweepSurface{{name: "posters"}, {name: "later surface"}}
	checkpoint := ArtworkReconcileCheckpoint{
		Version:      artworkReconcileCheckpointVersion,
		Totals:       []int{1, 1},
		ChapterTotal: 2,
		Stats:        ArtworkReconcileStats{Mode: ArtworkReconcileModeVerify},
	}
	sweeper := &scriptedArtworkVerifySweeper{
		batches: map[string][]scriptedArtworkBatch{
			"posters":       {{cursor: []string{"poster"}, done: 1, sweepErrors: 1}},
			"later surface": {{cursor: []string{"later"}, done: 1, verified: 1}},
		},
		chapterBatches: []scriptedArtworkChapterBatch{
			{cursor: 10, rows: 1, sweepErrors: 1},
			{cursor: 20, rows: 1, verified: 1},
		},
	}

	stats, err := runArtworkVerifySweep(
		context.Background(), sweeper, surfaces, checkpoint, nil, func(float64, string) {},
	)
	if err != nil {
		t.Fatalf("sweep without saver: %v", err)
	}
	if sweeper.processed["posters"] != 1 || sweeper.processed["later surface"] != 1 || sweeper.chaptersProcessed != 2 {
		t.Fatalf("work after unsafe batches: surfaces=%v chapter_batches=%d", sweeper.processed, sweeper.chaptersProcessed)
	}
	if stats.Verified != 2 || stats.SweepErrors != 2 {
		t.Fatalf("stats = %#v, want 2 verified and 2 sweep errors", stats)
	}
}

func TestArtworkReconcileLeavesRowsAloneOnStorageErrors(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("arc-err-%d", suffix)
	okContentID := fmt.Sprintf("arc-err-ok-%d", suffix)
	cachedKey := fmt.Sprintf("tmdb/movies/%s/poster/original.webp", contentID)
	okKey := fmt.Sprintf("tmdb/movies/%s/poster/original.webp", okContentID)
	seed := func(id, key string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
			VALUES ($1, 'movie', 'ARC Err', 'matched', '{}'::text[], $2, 'https://img.example/err.jpg')
		`, id, key); err != nil {
			t.Fatalf("seed item %s: %v", id, err)
		}
	}
	// The healthy sibling keeps the probe from concluding storage is
	// unreachable (an all-errored probe aborts before any sweep runs), so
	// the sweep-level skip-on-error behavior is what gets exercised.
	seed(contentID, cachedKey)
	seed(okContentID, okKey)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{contentID, okContentID})
	})

	checker := &fakeObjectChecker{erroring: map[string]bool{cachedKey: true}}
	var saved ArtworkReconcileCheckpoint
	stats, err := NewArtworkCacheReconciler(pool, checker).RunResumable(ctx, nil,
		func(next ArtworkReconcileCheckpoint) error {
			saved = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, nil)
	if err == nil || !strings.Contains(err.Error(), "last saved checkpoint") {
		t.Fatalf("RunResumable error = %v, want checkpoint stop", err)
	}
	if stats.Errors == 0 || stats.SweepErrors == 0 {
		t.Fatal("expected the erroring key to be counted")
	}
	if saved.SurfaceIndex != 0 || len(saved.SurfaceCursor) != 0 || saved.Finished {
		t.Fatalf("checkpoint advanced past an errored batch: %#v", saved)
	}

	var posterPath string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath); err != nil {
		t.Fatalf("read item: %v", err)
	}
	if posterPath != cachedKey {
		t.Fatalf("poster_path = %q, want untouched %q after storage error", posterPath, cachedKey)
	}
}

func TestArtworkReconcileAbortsWhenStorageUnreachable(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("arc-down-%d", suffix)
	cachedKey := fmt.Sprintf("tmdb/movies/%s/poster/original.webp", contentID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
		VALUES ($1, 'movie', 'ARC Down', 'matched', '{}'::text[], $2, 'https://img.example/down.jpg')
	`, contentID, cachedKey); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })

	// Every probe HEAD errors: storage is unreachable, which must abort the
	// run (missing ≠ unreachable) and leave the row untouched.
	checker := &fakeObjectChecker{errorAll: true}
	if _, err := NewArtworkCacheReconciler(pool, checker).Run(ctx, nil); err == nil {
		t.Fatal("Run with unreachable storage returned nil error")
	}

	var posterPath string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath); err != nil {
		t.Fatalf("read item: %v", err)
	}
	if posterPath != cachedKey {
		t.Fatalf("poster_path = %q, want untouched %q after unreachable storage", posterPath, cachedKey)
	}
}

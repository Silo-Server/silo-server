package scanner

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type variantQueryCountKey struct{}

type variantQueryTracer struct{}

func (variantQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if counter, ok := ctx.Value(variantQueryCountKey{}).(*atomic.Int64); ok {
		counter.Add(1)
	}
	return ctx
}

func (variantQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type variantFinalizeFixture struct {
	pool   *pgxpool.Pool
	scan   *Scanner
	folder *models.MediaFolder
	ids    map[string]int
}

type variantFinalizeRow struct {
	editionKey string
	partTotal  int
	missing    bool
	updatedAt  time.Time
}

func (fx variantFinalizeFixture) row(ctx context.Context, t *testing.T, name string) variantFinalizeRow {
	t.Helper()
	var row variantFinalizeRow
	if err := fx.pool.QueryRow(ctx, `
		SELECT edition_key, COALESCE(presentation_part_total, 0), missing_since IS NOT NULL, updated_at
		FROM media_files WHERE id = $1`, fx.ids[name]).Scan(
		&row.editionKey, &row.partTotal, &row.missing, &row.updatedAt,
	); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return row
}

// seedVariantFinalizeFixture builds a series folder with an in-scope season
// directory and files of the same episodes outside it.
func seedVariantFinalizeFixture(t *testing.T) variantFinalizeFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	cfg.ConnConfig.Tracer = variantQueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	root := fmt.Sprintf("/variant-finalize-%d", suffix)
	seriesID := fmt.Sprintf("variant-finalize-series-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Variant Finalize', 'matched', '{}'::text[])`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for episode := 1; episode <= 4; episode++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, $3, 'Episode')`, fmt.Sprintf("%s-e%d", seriesID, episode), seriesID, episode); err != nil {
			t.Fatalf("seed episode %d: %v", episode, err)
		}
	}

	season := root + "/Show/Season 01"
	files := []struct {
		name, path  string
		episode     int
		editionKey  string
		editionSrc  string
		kind, group string
		partIndex   int
		missing     bool
	}{
		// Split episode: part 1 in scope, part 2 in another directory.
		{name: "split-in", path: season + "/Show.S01E01.Part.1.mkv", episode: 1, kind: "split_episode", group: "show s01e01", partIndex: 1},
		{name: "split-out", path: root + "/Elsewhere/Show.S01E01.Part.2.mkv", episode: 1, kind: "split_episode", group: "show s01e01", partIndex: 2},
		// In scope with a stale edition.
		{name: "stale", path: season + "/Show.S01E02.mkv", episode: 2, editionKey: "stale", editionSrc: "heuristic_file"},
		// In scope, already finalized.
		{name: "steady", path: season + "/Show.S01E03.mkv", episode: 3},
		// In scope with an imported edition that scans keep.
		{name: "import", path: season + "/Show.S01E04.mkv", episode: 4, editionKey: "theatrical", editionSrc: editionSourceImport},
		// Missing in scope, and a stale file outside the scope: both are
		// left for later.
		{name: "missing", path: season + "/Show.S01E04.Old.mkv", episode: 4, editionKey: "stale", editionSrc: "heuristic_file", missing: true},
		{name: "outside-stale", path: root + "/Elsewhere/Show.S01E03.mkv", episode: 3, editionKey: "stale", editionSrc: "heuristic_file"},
	}
	ids := make(map[string]int, len(files))
	for _, file := range files {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (
				content_id, episode_id, media_folder_id, file_path, file_size,
				edition_key, edition_source,
				presentation_kind, presentation_group_key, presentation_part_index,
				missing_since, updated_at
			) VALUES (
				$1, $2, $3, $4, 1024,
				$5, $6,
				$7, $8, NULLIF($9, 0),
				CASE WHEN $10 THEN NOW() END, NOW() - interval '1 day'
			) RETURNING id`,
			seriesID, fmt.Sprintf("%s-e%d", seriesID, file.episode), folderID, file.path,
			file.editionKey, file.editionSrc,
			file.kind, file.group, file.partIndex,
			file.missing,
		).Scan(&id); err != nil {
			t.Fatalf("seed file %s: %v", file.name, err)
		}
		ids[file.name] = id
	}

	return variantFinalizeFixture{
		pool:   pool,
		scan:   &Scanner{fileRepo: NewFileRepository(pool)},
		folder: &models.MediaFolder{ID: folderID, Type: "series", Paths: []string{root}},
		ids:    ids,
	}
}

// A pass over a scope costs one narrow scope load, one owner lookup and one
// batched update. A pass with nothing to change writes nothing.
// TestFinalizeVariantsKeepsMatchSuppressionDB covers the columns the update
// leaves alone.
func TestFinalizeVariantsBatchesQueriesDB(t *testing.T) {
	fx := seedVariantFinalizeFixture(t)
	ctx := context.Background()
	scope := fx.folder.Paths[0] + "/Show/Season 01"

	before := map[string]variantFinalizeRow{}
	for name := range fx.ids {
		before[name] = fx.row(ctx, t, name)
	}

	var queries atomic.Int64
	if err := fx.scan.FinalizeVariantsByPathPrefix(context.WithValue(ctx, variantQueryCountKey{}, &queries), fx.folder, scope); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Scope load, one owner lookup for the scope's episodes, one batched update.
	if got := queries.Load(); got != 3 {
		t.Fatalf("finalize issued %d queries, want 3", got)
	}

	for _, name := range []string{"split-in", "split-out"} {
		row := fx.row(ctx, t, name)
		if row.partTotal != 2 {
			t.Fatalf("%s part total = %d, want 2", name, row.partTotal)
		}
		if !row.updatedAt.After(before[name].updatedAt) {
			t.Fatalf("%s updated_at not bumped", name)
		}
	}

	stale := fx.row(ctx, t, "stale")
	if stale.editionKey != "" {
		t.Fatalf("stale edition = %q, want it cleared", stale.editionKey)
	}
	if !stale.updatedAt.After(before["stale"].updatedAt) {
		t.Fatalf("stale updated_at not bumped")
	}

	for _, name := range []string{"steady", "import", "missing", "outside-stale"} {
		if got := fx.row(ctx, t, name); got != before[name] {
			t.Fatalf("%s changed: before %+v after %+v", name, before[name], got)
		}
	}

	// A second pass finds nothing to change and writes nothing.
	settled := map[string]variantFinalizeRow{}
	for name := range fx.ids {
		settled[name] = fx.row(ctx, t, name)
	}
	queries.Store(0)
	if err := fx.scan.FinalizeVariantsByPathPrefix(context.WithValue(ctx, variantQueryCountKey{}, &queries), fx.folder, scope); err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if got := queries.Load(); got != 2 {
		t.Fatalf("second finalize issued %d queries, want 2 (scope load and owner lookup)", got)
	}
	for name := range fx.ids {
		if got := fx.row(ctx, t, name); got != settled[name] {
			t.Fatalf("%s changed on the second pass: before %+v after %+v", name, settled[name], got)
		}
	}
}

// Movie discs belong to their content id, not an episode, so their siblings
// come from the content-owner lookup.
func TestFinalizeVariantsLoadsMovieDiscsOutsideTheScopeDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	cfg.ConnConfig.Tracer = variantQueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	root := fmt.Sprintf("/variant-finalize-movies-%d", suffix)
	movieID := fmt.Sprintf("variant-finalize-movie-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, movieID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, movieID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Discs', 'matched', '{}'::text[])`, movieID); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	scope := root + "/Discs (2010)"
	ids := map[string]int{}
	for name, path := range map[string]string{
		"disc-in":  scope + "/Discs.2010.CD1.mkv",
		"disc-out": root + "/Discs (2010) extra/Discs.2010.CD2.mkv",
	} {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
			VALUES ($1, $2, $3, 1024) RETURNING id`, movieID, folderID, path).Scan(&id); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
		ids[name] = id
	}

	scan := &Scanner{fileRepo: NewFileRepository(pool)}
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	var queries atomic.Int64
	if err := scan.FinalizeVariantsByPathPrefix(context.WithValue(ctx, variantQueryCountKey{}, &queries), folder, scope); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Scope load, the content-owner lookup, one batched update.
	if got := queries.Load(); got != 3 {
		t.Fatalf("finalize issued %d queries, want 3", got)
	}
	for name, id := range ids {
		var kind string
		var total int
		if err := pool.QueryRow(ctx, `SELECT presentation_kind, COALESCE(presentation_part_total, 0) FROM media_files WHERE id = $1`, id).Scan(&kind, &total); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if kind != "multipart_movie" || total != 2 {
			t.Fatalf("%s kind=%q total=%d, want multipart_movie with total 2", name, kind, total)
		}
	}
}

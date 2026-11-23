package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type skippedRootQueryRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *skippedRootQueryRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, data.SQL)
	return ctx
}

func (*skippedRootQueryRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *skippedRootQueryRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = nil
}

func (r *skippedRootQueryRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queries) == 0 {
		return ""
	}
	return r.queries[len(r.queries)-1]
}

type skippedRootPlanNode struct {
	NodeType     string                `json:"Node Type"`
	RelationName string                `json:"Relation Name"`
	IndexName    string                `json:"Index Name"`
	IndexCond    string                `json:"Index Cond"`
	Plans        []skippedRootPlanNode `json:"Plans"`
}

func (n skippedRootPlanNode) walk(visit func(skippedRootPlanNode)) {
	visit(n)
	for _, child := range n.Plans {
		child.walk(visit)
	}
}

func openSkippedRootPlanPool(t *testing.T) (*pgxpool.Pool, *skippedRootQueryRecorder) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	recorder := &skippedRootQueryRecorder{}
	cfg.ConnConfig.Tracer = recorder
	cfg.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.skipped_media_roots')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check skipped_media_roots table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the base schema")
	}
	return pool, recorder
}

func seedSkippedRootPlanFolder(t *testing.T, pool *pgxpool.Pool) (folderID int, base, specialRoot string) {
	t.Helper()
	ctx := t.Context()
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (name, type, enabled)
		VALUES ($1, 'movies', true)
		RETURNING id
	`, fmt.Sprintf("skipped-root-plan-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	base = fmt.Sprintf("/skipped-root-plan-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, file_size)
		SELECT $1, $2 || '/Studio-' || (g / 100) || '/Movie-' || g || '/movie-' || g || '.mp4', 1024
		FROM generate_series(1, 10000) g
	`, folderID, base); err != nil {
		t.Fatalf("seed folder files: %v", err)
	}

	specialRoot = base + `/Special_100%\Δ`
	seedFiles := []struct {
		path    string
		missing bool
	}{
		{path: specialRoot}, // exact path equal to root is not a descendant
		{path: specialRoot + "/part1.mp4"},
		{path: specialRoot + "/part2.mp4"},
		{path: specialRoot + "/sub/part3.mp4"},
		{path: specialRoot + "/gone.mp4", missing: true},
		{path: specialRoot + "0/sibling.mp4"},
		{path: specialRoot + "2/sibling.mp4"},
		{path: specialRoot + "-/sibling.mp4"},
	}
	for _, f := range seedFiles {
		var missingSince *time.Time
		if f.missing {
			now := time.Now().UTC()
			missingSince = &now
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, 1024, $3)
		`, folderID, f.path, missingSince); err != nil {
			t.Fatalf("seed special file %q: %v", f.path, err)
		}
	}
	if _, err := pool.Exec(ctx, `ANALYZE media_files`); err != nil {
		t.Fatalf("analyze media_files: %v", err)
	}
	return folderID, base, specialRoot
}

func requireSkippedRootPathRangeIndexed(t *testing.T, pool *pgxpool.Pool, query string) {
	t.Helper()
	ctx := t.Context()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	results, err := conn.Conn().PgConn().Exec(ctx, `EXPLAIN (GENERIC_PLAN, FORMAT JSON) `+query).ReadAll()
	if err != nil || len(results) != 1 || len(results[0].Rows) != 1 {
		t.Fatalf("explain generic plan: %v", err)
	}
	raw := results[0].Rows[0][0]
	var explained []struct {
		Plan skippedRootPlanNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &explained); err != nil || len(explained) != 1 {
		t.Fatalf("decode plan %s: %v", raw, err)
	}
	var rangeIndexed bool
	var scans []string
	explained[0].Plan.walk(func(node skippedRootPlanNode) {
		if node.RelationName == "media_files" && node.NodeType == "Seq Scan" {
			t.Errorf("generic plan reads media_files sequentially: %s", raw)
		}
		if node.IndexName == "" {
			return
		}
		scans = append(scans, node.NodeType+" "+node.IndexName+" "+node.IndexCond)
		if node.IndexName == "idx_media_files_folder_file_path_pattern" &&
			strings.Contains(node.IndexCond, "file_path ~>=~") &&
			strings.Contains(node.IndexCond, "file_path ~<~") {
			rangeIndexed = true
		}
	})
	if !rangeIndexed {
		t.Fatalf("generic plan does not bound file_path on idx_media_files_folder_file_path_pattern: %q", scans)
	}
}

// TestUpsertObservedFileUsesPathRangeUnderGenericPlanDB pins both the generic
// query plan and the descendant counting behavior of UpsertObservedFile.
// Under a generic prepared plan, strpos(file_path, $2 || '/') = 1 cannot use
// idx_media_files_folder_file_path_pattern and scans every media_files row of
// the folder; bytewise ~>=~ / ~<~ bounds keep the COUNT on the path index.
// It takes a few seconds because the planner needs analyzed statistics on a
// folder large enough that a folder-wide scan costs more than the index.
func TestUpsertObservedFileUsesPathRangeUnderGenericPlanDB(t *testing.T) {
	ctx := t.Context()
	pool, recorder := openSkippedRootPlanPool(t)
	folderID, base, specialRoot := seedSkippedRootPlanFolder(t, pool)
	repo := NewSkippedRootRepository(pool)

	recorder.reset()
	if err := repo.UpsertObservedFile(ctx, folderID, specialRoot, skippedReasonMissingFolderIDs, specialRoot+"/part1.mp4"); err != nil {
		t.Fatalf("upsert special root: %v", err)
	}
	upsertSQL := recorder.last()
	if upsertSQL == "" {
		t.Fatal("query recorder captured no SQL for UpsertObservedFile")
	}

	// Verify trailing slash cleanup and conflict update preserve accurate count.
	if err := repo.UpsertObservedFile(ctx, folderID, specialRoot+"/", skippedReasonSeriesInMovieLibrary, specialRoot+"/part2.mp4"); err != nil {
		t.Fatalf("upsert special root with trailing slash: %v", err)
	}
	emptyRoot := base + "/EmptyRoot"
	if err := repo.UpsertObservedFile(ctx, folderID, emptyRoot, skippedReasonMissingFolderIDs, emptyRoot+"/new.mp4"); err != nil {
		t.Fatalf("upsert empty root: %v", err)
	}

	roots, err := repo.ListByFolder(ctx, folderID)
	if err != nil {
		t.Fatalf("list skipped roots: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("skipped roots count = %d, want 2", len(roots))
	}
	gotByPath := make(map[string]struct {
		reason string
		sample string
		count  int
	}, len(roots))
	for _, r := range roots {
		gotByPath[r.RootPath] = struct {
			reason string
			sample string
			count  int
		}{reason: r.Reason, sample: r.SampleFilePath, count: r.FileCount}
	}
	if got := gotByPath[specialRoot]; got.count != 3 || got.reason != skippedReasonSeriesInMovieLibrary || got.sample != specialRoot+"/part2.mp4" {
		t.Fatalf("special root = %+v, want count=3 reason=%q sample=%q", got, skippedReasonSeriesInMovieLibrary, specialRoot+"/part2.mp4")
	}
	if got := gotByPath[emptyRoot]; got.count != 1 || got.reason != skippedReasonMissingFolderIDs {
		t.Fatalf("empty root = %+v, want count=1 reason=%q", got, skippedReasonMissingFolderIDs)
	}

	requireSkippedRootPathRangeIndexed(t, pool, upsertSQL)
}

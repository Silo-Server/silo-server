package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/literaryworks"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only queries carrying this context key are measured, excluding fixture setup,
// connection initialization, assertions, and cleanup.
type bookQueryTraceKey struct{}
type bookQueryStartKey struct{}
type bookQueryStart struct {
	at       time.Time
	literary bool
}
type bookQueryCounts struct {
	mu                       sync.Mutex
	total, literary          int
	elapsed, literaryElapsed time.Duration
}
type bookQueryTracer struct{}

func (bookQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if _, ok := ctx.Value(bookQueryTraceKey{}).(*bookQueryCounts); !ok {
		return ctx
	}
	return context.WithValue(ctx, bookQueryStartKey{}, bookQueryStart{time.Now(), strings.Contains(data.SQL, "literary_work")})
}
func (bookQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	counts, ok := ctx.Value(bookQueryTraceKey{}).(*bookQueryCounts)
	if !ok {
		return
	}
	start := ctx.Value(bookQueryStartKey{}).(bookQueryStart)
	elapsed := time.Since(start.at)
	counts.mu.Lock()
	defer counts.mu.Unlock()
	counts.total++
	counts.elapsed += elapsed
	if start.literary {
		counts.literary++
		counts.literaryElapsed += elapsed
	}
}
func newBookScanTestPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = bookQueryTracer{}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pool
}
func newBookScanTestFolder(t testing.TB, pool *pgxpool.Pool, kind string) *models.MediaFolder {
	t.Helper()
	folder := &models.MediaFolder{Type: kind, Enabled: true}
	if err := pool.QueryRow(context.Background(), `INSERT INTO media_folders (type,name,enabled) VALUES ($1,'Book skip test',true) RETURNING id`, kind).Scan(&folder.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// Remove works before items so no orphan test works survive the fixture.
		_, _ = pool.Exec(ctx, `DELETE FROM literary_works WHERE work_id IN (
   SELECT wi.work_id FROM literary_work_items wi JOIN media_item_libraries ml USING (content_id) WHERE ml.media_folder_id=$1)`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_item_libraries WHERE media_folder_id=$1)`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder.ID)
	})
	return folder
}

// Seed unchanged files directly so a malformed file will fail loudly if the
// scanner accidentally falls through to parsing instead of skipping it.
func seedUnchangedBooks(t testing.TB, pool *pgxpool.Pool, folder *models.MediaFolder, count int) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, count)
	roots := make([]string, count)
	canonicalRoots := make([]string, count)
	ids := make([]string, count)
	mtimes := make([]time.Time, count)
	for i := range count {
		ext := ".epub"
		root := dir
		if folder.Type == "audiobooks" {
			ext = ".m4b"
			root = filepath.Join(dir, fmt.Sprint(i))
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		paths[i] = filepath.Join(root, fmt.Sprint(i)+ext)
		if err := os.WriteFile(paths[i], []byte("book"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		mtimes[i] = normalizeFileModifiedAt(info.ModTime())
		roots[i] = paths[i]
		if folder.Type == "audiobooks" {
			roots[i] = root
		}
		canonicalRoots[i], err = canonicalWalkPath(roots[i])
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = fmt.Sprintf("book-skip-%d-%d", folder.ID, i)
	}
	kind := "ebook"
	if folder.Type == "audiobooks" {
		kind = "audiobook"
	}
	ctx := context.Background()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id,type,title,status,genres) SELECT id,$2,id,'local','{}'::text[] FROM unnest($1::text[]) id`, []any{ids, kind}},
		{`INSERT INTO media_item_libraries (content_id,media_folder_id) SELECT unnest($1::text[]),$2`, []any{ids, folder.ID}},
		{`INSERT INTO media_files (content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,file_modified_at,group_key_version)
    SELECT id,$1,path,root,canonical,4,mtime,$7 FROM unnest($2::text[],$3::text[],$4::text[],$5::text[],$6::timestamptz[]) AS f(id,path,root,canonical,mtime)`, []any{folder.ID, ids, paths, roots, canonicalRoots, mtimes, ebookGroupKeyVersion}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	return roots
}

func TestUnchangedBooksDoNotQueryLiteraryWorks(t *testing.T) {
	for _, kind := range []string{"ebooks", "audiobooks"} {
		t.Run(kind, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, kind)
			paths := seedUnchangedBooks(t, pool, folder, 1)
			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			s.SetLiteraryWorkLinker(literaryworks.NewService(literaryworks.NewRepository(pool)))
			counts := &bookQueryCounts{}
			ctx := context.WithValue(t.Context(), bookQueryTraceKey{}, counts)
			var skipped int64
			var err error
			if kind == "ebooks" {
				err = s.reconcileEbookFile(ctx, folder, paths[0], &skipped, newEbookGroupLocks())
			} else {
				err = s.reconcileAudiobookFolder(ctx, folder, paths[0], nil, &skipped)
			}
			if err != nil {
				t.Fatal(err)
			}
			if skipped != 1 {
				t.Fatalf("skipped = %d, want 1", skipped)
			}
			t.Logf("unchanged unlinked %s: total=%d literary=%d query_time=%s literary_time=%s", kind, counts.total, counts.literary, counts.elapsed, counts.literaryElapsed)
			if counts.literary != 0 {
				t.Fatalf("unchanged skip issued %d literary-works queries, want zero", counts.literary)
			}
		})
	}
}

// Run against an otherwise idle migrated Postgres, e.g. -run '^$' -bench
// BenchmarkUnchangedEbookScan -benchtime=3000x -count=3. Fixtures are outside timing.
func BenchmarkUnchangedEbookScan(b *testing.B) {
	pool := newBookScanTestPool(b)
	folder := newBookScanTestFolder(b, pool, "ebooks")
	paths := seedUnchangedBooks(b, pool, folder, 3000)
	s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
	s.SetLiteraryWorkLinker(literaryworks.NewService(literaryworks.NewRepository(pool)))
	groupLocks := newEbookGroupLocks()
	counts := &bookQueryCounts{}
	ctx := context.WithValue(context.Background(), bookQueryTraceKey{}, counts)
	var skipped int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.reconcileEbookFile(ctx, folder, paths[i%len(paths)], &skipped, groupLocks); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if skipped != int64(b.N) {
		b.Fatalf("skipped=%d, want %d", skipped, b.N)
	}
	b.ReportMetric(float64(counts.total)/float64(b.N), "queries/file")
	b.ReportMetric(float64(counts.literary)/float64(b.N), "literary-queries/file")
	b.ReportMetric(float64(counts.elapsed-counts.literaryElapsed)/float64(b.N)/1e3, "skip-query-us/file")
	b.ReportMetric(float64(counts.literaryElapsed)/float64(b.N)/1e3, "literary-query-us/file")
}

func TestEbookScanBatchesSkipQueries(t *testing.T) {
	pool := newBookScanTestPool(t)
	folder := newBookScanTestFolder(t, pool, "ebooks")
	paths := seedUnchangedBooks(t, pool, folder, 1001)
	folder.Paths = []string{filepath.Dir(paths[0])}
	s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
	s.SetLiteraryWorkLinker(literaryworks.NewService(literaryworks.NewRepository(pool)))
	counts := &bookQueryCounts{}
	ctx := context.WithValue(t.Context(), bookQueryTraceKey{}, counts)
	if err := s.scanEbookPaths(ctx, folder, folder.Paths, true); err != nil {
		t.Fatal(err)
	}
	t.Logf("1001 unchanged files: %d total queries, %d literary queries", counts.total, counts.literary)
	// Includes folder-level reconciliation and warnings, not just the skip check.
	// This budget leaves room for those fixed costs but rules out per-file SQL.
	if counts.total > 30 || counts.literary != 0 {
		t.Fatalf("unchanged scan: %d total queries, %d literary queries; want bounded batch queries and no literary queries", counts.total, counts.literary)
	}
}

func TestEbookSkipPreloadPreservesEligibility(t *testing.T) {
	tests := []struct {
		name, mutation string
		want           bool
	}{
		{"unchanged", "", true},
		{"size changed", "UPDATE media_files SET file_size=file_size+1 WHERE media_folder_id=$1", false},
		{"mtime changed", "UPDATE media_files SET file_modified_at=file_modified_at-interval '1 minute' WHERE media_folder_id=$1", false},
		{"mtime absent", "UPDATE media_files SET file_modified_at=NULL WHERE media_folder_id=$1", false},
		{"group version changed", "UPDATE media_files SET group_key_version=0 WHERE media_folder_id=$1", false},
		{"unmatched", "UPDATE media_items SET status=' UnMaTcHeD ' WHERE content_id IN (SELECT content_id FROM media_item_libraries WHERE media_folder_id=$1)", false},
		{"no content", "UPDATE media_files SET content_id=NULL WHERE media_folder_id=$1", false},
		{"different file path", "UPDATE media_files SET file_path=file_path||'.other' WHERE media_folder_id=$1", false},
		{"missing file", "UPDATE media_files SET missing_since=now() WHERE media_folder_id=$1", false},
		{"no file row", "DELETE FROM media_files WHERE media_folder_id=$1", false},
		{"multiple rows", `INSERT INTO media_files (media_folder_id,content_id,file_path,observed_root_path,file_size,file_modified_at,group_key_version)
    SELECT media_folder_id,content_id,file_path||'.other',observed_root_path,file_size,file_modified_at,group_key_version FROM media_files WHERE media_folder_id=$1`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, "ebooks")
			paths := seedUnchangedBooks(t, pool, folder, 1)
			if tt.mutation != "" {
				if _, err := pool.Exec(t.Context(), tt.mutation, folder.ID); err != nil {
					t.Fatal(err)
				}
			}
			counts := &bookQueryCounts{}
			ctx := context.WithValue(t.Context(), bookQueryTraceKey{}, counts)
			repo := NewFileRepository(pool)
			state, err := repo.loadEbookSkipState(ctx, folder.ID, paths)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			id, skip := unchangedEbookFile(state[paths[0]], paths[0], info.Size(), normalizeFileModifiedAt(info.ModTime()))
			if skip != tt.want || (skip && id == "") {
				t.Fatalf("skip=%v id=%q, want skip=%v", skip, id, tt.want)
			}
			if counts.total != 1 {
				t.Fatalf("preload queries=%d, want 1", counts.total)
			}
			other, err := repo.loadEbookSkipState(ctx, folder.ID+1000000, paths)
			if err != nil {
				t.Fatal(err)
			}
			if len(other) != 0 {
				t.Fatalf("preload leaked another library: %v", other)
			}
		})
	}
}

func BenchmarkUnchangedEbookBatch(b *testing.B) {
	pool := newBookScanTestPool(b)
	folder := newBookScanTestFolder(b, pool, "ebooks")
	paths := seedUnchangedBooks(b, pool, folder, 3000)
	s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
	s.SetLiteraryWorkLinker(literaryworks.NewService(literaryworks.NewRepository(pool)))
	groupLocks := newEbookGroupLocks()
	counts := &bookQueryCounts{}
	ctx := context.WithValue(context.Background(), bookQueryTraceKey{}, counts)
	var skipped int64
	b.ResetTimer()
	for start := 0; start < b.N; {
		offset := start % len(paths)
		count := min(ebookSkipBatchSize, b.N-start, len(paths)-offset)
		batch := paths[offset : offset+count]
		state, err := s.fileRepo.loadEbookSkipState(ctx, folder.ID, batch)
		if err != nil {
			b.Fatal(err)
		}
		for _, path := range batch {
			if err := s.reconcileEbookFileWithSkipState(ctx, folder, path, &skipped, groupLocks, state); err != nil {
				b.Fatal(err)
			}
		}
		start += count
	}
	b.StopTimer()
	if skipped != int64(b.N) {
		b.Fatalf("skipped=%d, want %d", skipped, b.N)
	}
	b.ReportMetric(float64(counts.total)/float64(b.N), "queries/file")
	b.ReportMetric(float64(counts.literary)/float64(b.N), "literary-queries/file")
	b.ReportMetric(float64(counts.elapsed-counts.literaryElapsed)/float64(b.N)/1e3, "skip-query-us/file")
}

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

// canonicalTestPath is path as the scanner resolves a book root.
func canonicalTestPath(t testing.TB, path string) string {
	t.Helper()
	physical, err := canonicalWalkPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return physical
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
				err = s.reconcileAudiobookFolder(ctx, folder, paths[0], nil, nil, time.Time{}, &skipped, nil)
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
		{"no library membership", "DELETE FROM media_item_libraries WHERE media_folder_id=$1", false},
		{"item deleted", "DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id=$1)", false},
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

// Duplicate copies of one recording share an item, and the book's runtime is
// its longest active copy whichever copy a scan writes last, as the runtime
// backfill computes it. A copy marked missing does not count.
func TestAudiobookRuntimeTakesLongestCopyPostgres(t *testing.T) {
	for _, tc := range []struct {
		name           string
		otherSeconds   int
		otherMissing   bool
		change         string
		runtimeMinutes int
	}{
		{name: "a longer copy elsewhere keeps its runtime", otherSeconds: 36180, runtimeMinutes: 603},
		{name: "a shorter copy elsewhere does not lower it", otherSeconds: 35900, runtimeMinutes: 600},
		{name: "a missing copy does not count", otherSeconds: 36180, otherMissing: true, runtimeMinutes: 600},
		{name: "marking the longest copy missing lowers runtime", otherSeconds: 36180, change: "missing", runtimeMinutes: 600},
		{name: "deleting the longest copy lowers runtime", otherSeconds: 36180, change: "delete", runtimeMinutes: 600},
		{name: "replacing the physical root drops its old duration", otherSeconds: 36180, change: "root", runtimeMinutes: 600},
		{name: "metadata refresh preserves file runtime", otherSeconds: 35900, change: "metadata", runtimeMinutes: 600},
		{name: "a locked runtime survives file changes", otherSeconds: 35900, change: "locked", runtimeMinutes: 73},
		{name: "unlocking runtime restores file duration", otherSeconds: 35900, change: "unlock", runtimeMinutes: 600},
		{name: "an unlocked metadata edit keeps file duration", otherSeconds: 35900, change: "edit", runtimeMinutes: 600},
		{name: "a locked metadata edit overrides file duration", otherSeconds: 35900, change: "locked-edit", runtimeMinutes: 73},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, "audiobooks")
			ctx := t.Context()
			dir := t.TempDir()
			path := filepath.Join(dir, "book.mp3")
			if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("runtime-copies-%d", folder.ID)
			var missing *time.Time
			if tc.otherMissing {
				now := time.Now()
				missing = &now
			}
			for _, stmt := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO media_items (content_id,type,title,status,genres,runtime) VALUES ($1,'audiobook','Book','local','{}'::text[],0)`, []any{id}},
				{`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, []any{id, folder.ID}},
				{`INSERT INTO media_files (content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,duration,missing_since)
    VALUES ($1,$2,'/elsewhere/m4b/book.m4b','/elsewhere/m4b','/elsewhere/m4b',4,$3,$4)`, []any{id, folder.ID, tc.otherSeconds, missing}},
			} {
				if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id=$1`, id) })
			if tc.change == "root" {
				if _, err := pool.Exec(ctx, `UPDATE media_files SET file_path=$2, observed_root_path=$3 WHERE content_id=$1`, id, path, dir); err != nil {
					t.Fatal(err)
				}
			}
			if tc.change == "locked" || tc.change == "unlock" {
				if _, err := pool.Exec(ctx, `UPDATE media_items SET runtime=73, locked_fields='{7}' WHERE content_id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}

			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			book := &parsedAudiobook{Title: "Book", Files: []parsedAudiobookFile{{
				Path: path, Duration: 36000, Size: info.Size(), ModifiedAt: normalizeFileModifiedAt(info.ModTime()),
			}}}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, id, dir, canonicalTestPath(t, dir), book, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			switch tc.change {
			case "missing":
				_, err = pool.Exec(ctx, `UPDATE media_files SET missing_since=now() WHERE content_id=$1 AND canonical_root_path='/elsewhere/m4b'`, id)
			case "delete":
				_, err = pool.Exec(ctx, `DELETE FROM media_files WHERE content_id=$1 AND canonical_root_path='/elsewhere/m4b'`, id)
			case "metadata":
				// Generic manual refresh and Identify both persist through this
				// repository, independently of the audiobook enrichment worker.
				err = s.itemRepo.Upsert(ctx, &models.MediaItem{ContentID: id, Type: "audiobook", Title: "Book", Runtime: 481})
			case "unlock":
				err = s.itemRepo.UpdateMetadata(ctx, id, &catalog.MetadataUpdate{LockedFields: new([]int{})})
			case "edit":
				err = s.itemRepo.UpdateMetadata(ctx, id, &catalog.MetadataUpdate{Runtime: new(73)})
			case "locked-edit":
				err = s.itemRepo.UpdateMetadata(ctx, id, &catalog.MetadataUpdate{Runtime: new(73), LockedFields: new([]int{7})})
			}
			if err != nil {
				t.Fatal(err)
			}
			assertBookRuntime(t, pool, id, tc.runtimeMinutes)
		})
	}
}

func assertBookRuntime(t *testing.T, pool *pgxpool.Pool, id string, want int) {
	t.Helper()
	var runtime int
	if err := pool.QueryRow(t.Context(), `SELECT runtime FROM media_items WHERE content_id=$1`, id).Scan(&runtime); err != nil {
		t.Fatal(err)
	}
	if runtime != want {
		t.Fatalf("%s: runtime = %d minutes, want %d", id, runtime, want)
	}
}

func TestAudiobookRuntimeConcurrentFileWritesPostgres(t *testing.T) {
	pool := newBookScanTestPool(t)
	firstFolder := newBookScanTestFolder(t, pool, "audiobooks")
	secondFolder := newBookScanTestFolder(t, pool, "audiobooks")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id := fmt.Sprintf("runtime-file-writes-%d", firstFolder.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,status,genres,runtime)
VALUES ($1,'audiobook','Book','local','{}',600)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2),($1,$3)`, id, firstFolder.ID, secondFolder.ID); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		folder  int
		root    string
		seconds int
	}{{firstFolder.ID, "/file-race/first", 36000}, {secondFolder.ID, "/file-race/second", 28800}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_files
(content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,duration)
VALUES ($1,$2,$3||'/book.m4b',$3,$3,4,$4)`, id, file.folder, file.root, file.seconds); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id=$1`, id) })

	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(context.Background()) }()
	if _, err := first.Exec(ctx, `UPDATE media_files SET missing_since=now() WHERE content_id=$1 AND media_folder_id=$2`, id, firstFolder.ID); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, err := second.Exec(ctx, `INSERT INTO media_files
(content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,duration)
VALUES ($1,$2,'/file-race/new/book.m4b','/file-race/new','/file-race/new',4,29400)`, id, secondFolder.ID)
		if err == nil {
			err = second.Commit(ctx)
		}
		done <- err
	}()
	// Different libraries avoid the existing per-library stats lock. Neither
	// transaction upserts an item, so this wait is in the file triggers.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`, first.Conn().PgConn().PID(), second.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("file write completed before the missing copy committed: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertBookRuntime(t, pool, id, 490)
}

func TestAudiobookRuntimeConcurrentCopiesPostgres(t *testing.T) {
	pool := newBookScanTestPool(t)
	folder := newBookScanTestFolder(t, pool, "audiobooks")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id := fmt.Sprintf("runtime-concurrent-%d", folder.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,status,genres,runtime)
VALUES ($1,'audiobook','Book','local','{}',600)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, folder.ID); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
	roots := []string{t.TempDir(), t.TempDir()}
	books := make([]*parsedAudiobook, len(roots))
	for i, root := range roots {
		path := filepath.Join(root, "book.mp3")
		if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		seconds := []int{36000, 28800}[i]
		if _, err := pool.Exec(ctx, `INSERT INTO media_files
(content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,duration)
VALUES ($1,$2,$3,$4,$4,4,$5)`, id, folder.ID, path, root, seconds); err != nil {
			t.Fatal(err)
		}
		books[i] = &parsedAudiobook{Title: "Book", Files: []parsedAudiobookFile{{
			Path: path, Duration: []int{18000, 29400}[i], Size: info.Size(), ModifiedAt: normalizeFileModifiedAt(info.ModTime()),
		}}}
	}

	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(context.Background()) }()
	if err := s.upsertAudiobookPresentationTx(ctx, first, folder, id, roots[0], canonicalTestPath(t, roots[0]), books[0], time.Time{}); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(context.Background()) }()
	done := make(chan error, 1)
	secondRoot := canonicalTestPath(t, roots[1])
	go func() {
		err := s.upsertAudiobookPresentationTx(ctx, second, folder, id, roots[1], secondRoot, books[1], time.Time{})
		if err == nil {
			err = second.Commit(ctx)
		}
		done <- err
	}()
	// Observe the second transaction waiting on the first, so its runtime
	// calculation starts with the first copy's old committed duration.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`, first.Conn().PgConn().PID(), second.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("second copy completed before the first committed: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertBookRuntime(t, pool, id, 490)
}

func TestAudiobookRuntimeConcurrentMetadataAndMissingPostgres(t *testing.T) {
	pool := newBookScanTestPool(t)
	folder := newBookScanTestFolder(t, pool, "audiobooks")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id := fmt.Sprintf("runtime-mixed-writers-%d", folder.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,status,genres,runtime)
VALUES ($1,'audiobook','Book','local','{}',600)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, folder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_files
(content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,duration) VALUES
($1,$2,'/mixed-writers/long/book.m4b','/mixed-writers/long','/mixed-writers/long',4,36000),
($1,$2,'/mixed-writers/short/book.m4b','/mixed-writers/short','/mixed-writers/short',4,28800)`, id, folder.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE content_id=$1`, id) })
	scan, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scan.Rollback(context.Background()) }()
	// Pause the scan after its item lock and before the Upsert's AFTER
	// trigger writes per-library stats, allowing a missing writer to race.
	if _, err := scan.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id=$1 FOR NO KEY UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	missing, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = missing.Rollback(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, err := missing.Exec(ctx, `UPDATE media_files SET missing_since=now() WHERE content_id=$1 AND canonical_root_path='/mixed-writers/long'`, id)
		if err == nil {
			err = missing.Commit(ctx)
		}
		done <- err
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`, scan.Conn().PgConn().PID(), missing.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("missing write completed before the scan released its item: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	repo := catalog.NewItemRepository(pool)
	if err := repo.UpsertTx(ctx, scan, &models.MediaItem{ContentID: id, Type: "audiobook", Title: "Book", Status: "local", Runtime: 600}); err != nil {
		t.Fatal(err)
	}
	if err := scan.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertBookRuntime(t, pool, id, 480)
}

// A rescan of a retagged book keeps the authors and narrators an admin locked
// with the crew lock, and replaces them when the lock is off.
func TestAudiobookRescanKeepsCrewLockedCreditsDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		locked string
		want   []string
	}{
		{"crew locked", "{5}", []string{"Stored Author", "Stored Narrator"}},
		{"unlocked", "{}", []string{"Tagged Author", "Tagged Narrator"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newBookScanTestPool(t)
			folder := newBookScanTestFolder(t, pool, "audiobooks")
			ctx := t.Context()
			id := fmt.Sprintf("credit-lock-%d", folder.ID)
			for _, stmt := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO media_items (content_id,type,title,status,genres,locked_fields) VALUES ($1,'audiobook','Book','local','{}'::text[],$2::integer[])`, []any{id, tc.locked}},
				{`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, []any{id, folder.ID}},
			} {
				if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
					t.Fatal(err)
				}
			}
			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, 0)
			var stored []models.ItemPerson
			for i, c := range []struct {
				name string
				kind models.PersonKind
			}{{"Stored Author", models.PersonKindAuthor}, {"Stored Narrator", models.PersonKindNarrator}} {
				personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: c.name})
				if err != nil {
					t.Fatal(err)
				}
				stored = append(stored, models.ItemPerson{Person: models.Person{ID: personID}, Kind: c.kind, SortOrder: i})
			}
			if err := s.itemRepo.ReplacePeople(ctx, id, stored); err != nil {
				t.Fatal(err)
			}

			if err := s.upsertAudiobookPeople(ctx, id, &parsedAudiobook{Author: "Tagged Author", Narrator: "Tagged Narrator"}); err != nil {
				t.Fatal(err)
			}
			people, err := s.itemRepo.GetPeople(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range people {
				got = append(got, p.Name)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("credits = %v, want %v", got, tc.want)
			}
		})
	}
}

// A library indexed when each disc folder was a book, and loose files in the
// library root merged into one book, converges on one book per disc set and
// one per root file, keeping the content IDs both groupings share: a folder's
// own book keeps its item and parts, and the next scan skips every book.
func TestAudiobookRegroupKeepsContentIDsDB(t *testing.T) {
	pool := newBookScanTestPool(t)
	files := []string{"Author/Book/Disc 1/01.mp3", "Author/Book/Disc 2/01.mp3", "Author/Loose One.mp3", "Author/Loose Two.mp3", "Author/Proper/01.mp3", "Root A.mp3", "Root B.mp3"}
	// On a case-sensitive filesystem: two root files whose names differ only
	// in case were parts 1 and 2 of one item, and only the first may keep it;
	// a file beside the indexed one that differs only in case is another file;
	// and a case-only rename keeps its item.
	caseSensitive := caseSensitiveTempDir(t)
	if caseSensitive {
		files = append(files, "Part.mp3", "part.mp3", "Song.mp3", "song.mp3", "track.mp3")
	}
	folder, root, path := newAudiobookLibraryWithFiles(t, pool, files...)
	ctx := t.Context()
	disc1, disc2, loose, rootLoose := seedEarlierAudiobookGrouping(t, pool, folder.ID, path)
	mixed := fmt.Sprintf("regroup-mixed-%d", folder.ID)
	diff, renamed := fmt.Sprintf("regroup-diff-%d", folder.ID), fmt.Sprintf("regroup-renamed-%d", folder.ID)
	if caseSensitive {
		seedAudiobookItem(t, pool, folder.ID, path, mixed, ".", "Part.mp3", "part.mp3")
		seedAudiobookItem(t, pool, folder.ID, path, diff, ".", "Song.mp3")
		seedAudiobookItem(t, pool, folder.ID, path, renamed, ".", "Track.mp3")
	}

	s := NewScanner(NewFileRepository(pool), fakeAudiobookFFprobe(t), nil, 1, false, 0)
	type book struct {
		id    string
		files int
	}
	snapshot := func() (map[string]book, map[string]string) {
		t.Helper()
		if err := s.ScanAudiobookFolder(ctx, folder, true); err != nil {
			t.Fatal(err)
		}
		rows, err := pool.Query(ctx, `SELECT mf.observed_root_path, mf.content_id, count(*), max(mf.probe_updated_at)::text
			FROM media_files mf JOIN media_item_libraries ml ON ml.content_id = mf.content_id AND ml.media_folder_id = mf.media_folder_id
			WHERE mf.media_folder_id = $1 AND mf.missing_since IS NULL GROUP BY 1, 2`, folder.ID)
		if err != nil {
			t.Fatal(err)
		}
		books, probed := map[string]book{}, map[string]string{}
		for rows.Next() {
			var rootPath, id, probedAt string
			var files int
			if err := rows.Scan(&rootPath, &id, &files, &probedAt); err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(root, rootPath)
			if _, dup := books[rel]; dup {
				t.Fatalf("book %s has files in two items", rel)
			}
			books[rel], probed[rel] = book{id, files}, probedAt
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return books, probed
	}

	books, probed := snapshot()
	wantBooks := 5
	if caseSensitive {
		wantBooks = 10
		upper, lower := books["Part.mp3"], books["part.mp3"]
		if upper.id != mixed || lower.id == "" || lower.id == mixed {
			t.Errorf("Part.mp3 = %+v and part.mp3 = %+v, want only Part.mp3 to keep %s", upper, lower, mixed)
		}
		if song, other := books["Song.mp3"], books["song.mp3"]; song.id != diff || other.id == "" || other.id == diff {
			t.Errorf("Song.mp3 = %+v and song.mp3 = %+v, want the second file to get its own item", song, other)
		}
		if got := books["track.mp3"]; got.id != renamed {
			t.Errorf("case-only rename = %+v, want it to keep %s", got, renamed)
		}
	}
	if len(books) != wantBooks {
		t.Fatalf("books = %v, want Author/Book, Author, Author/Proper, and one book per root file", books)
	}
	if got := books["Author/Book"]; got != (book{disc1, 2}) {
		t.Errorf("disc book = %+v, want disc 1's item with both discs", got)
	}
	if got := books["Author"]; got != (book{loose, 2}) {
		t.Errorf("author folder book = %+v, want its item %s with both loose files", got, loose)
	}
	if got := books["Root A.mp3"]; got != (book{rootLoose, 1}) {
		t.Errorf("first root file = %+v, want the merged item %s", got, rootLoose)
	}
	second, proper := books["Root B.mp3"], books["Author/Proper"]
	if second.id == "" || second.id == rootLoose || proper.id == "" || proper.id == second.id {
		t.Errorf("the second root file and the sibling book need items of their own: %v", books)
	}
	var disc2Libraries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_item_libraries WHERE content_id = $1`, disc2).Scan(&disc2Libraries); err != nil {
		t.Fatal(err)
	}
	if disc2Libraries != 0 {
		t.Errorf("disc 2's emptied item is still in the library")
	}

	again, probedAgain := snapshot()
	if !reflect.DeepEqual(again, books) || !reflect.DeepEqual(probedAgain, probed) {
		t.Errorf("rescan changed or re-probed books:\n first %v %v\nsecond %v %v", books, probed, again, probedAgain)
	}
}

// seedEarlierAudiobookGrouping stores the earlier grouping: one item per disc,
// both loose author files as parts of one item rooted at the author folder,
// and both loose root files as parts of one item rooted at the library root.
func seedEarlierAudiobookGrouping(t *testing.T, pool *pgxpool.Pool, folderID int, path func(string) string) (disc1, disc2, loose, rootLoose string) {
	t.Helper()
	disc1, disc2 = fmt.Sprintf("regroup-d1-%d", folderID), fmt.Sprintf("regroup-d2-%d", folderID)
	loose, rootLoose = fmt.Sprintf("regroup-loose-%d", folderID), fmt.Sprintf("regroup-root-%d", folderID)
	seedAudiobookItem(t, pool, folderID, path, disc1, "Author/Book/Disc 1", "Author/Book/Disc 1/01.mp3")
	seedAudiobookItem(t, pool, folderID, path, disc2, "Author/Book/Disc 2", "Author/Book/Disc 2/01.mp3")
	seedAudiobookItem(t, pool, folderID, path, loose, "Author", "Author/Loose One.mp3", "Author/Loose Two.mp3")
	seedAudiobookItem(t, pool, folderID, path, rootLoose, ".", "Root A.mp3", "Root B.mp3")
	return disc1, disc2, loose, rootLoose
}

// seedAudiobookItem stores one indexed audiobook item rooted at root, with
// its files as parts in the order given.
func seedAudiobookItem(t *testing.T, pool *pgxpool.Pool, folderID int, path func(string) string, id, root string, files ...string) {
	t.Helper()
	ctx := t.Context()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id,type,title,status,genres) VALUES ($1,'audiobook',$1,'local','{}'::text[])`, []any{id}},
		{`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, []any{id, folderID}},
		// Earlier scans could leave several items under one root; the first keeps the claim.
		{`INSERT INTO media_item_roots (media_folder_id,canonical_root_path,content_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, []any{folderID, path(root), id}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, rel := range files {
		var part any
		if len(files) > 1 {
			part = i + 1
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_files (content_id,media_folder_id,file_path,observed_root_path,canonical_root_path,file_size,presentation_part_index)
			VALUES ($1,$2,$3,$4,$4,1,$5)`, id, folderID, path(rel), path(root), part); err != nil {
			t.Fatal(err)
		}
	}
}

// caseSensitiveTempDir reports whether files under a temp directory whose
// names differ only in case are distinct.
func caseSensitiveTempDir(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "x"), "x")
	_, err := os.Stat(filepath.Join(dir, "X"))
	return err != nil
}

// newAudiobookLibraryWithFiles creates an audiobook library rooted at a temp
// directory holding a copy of a real mp3 at each relative path. path maps a
// relative path to its absolute one.
func newAudiobookLibraryWithFiles(t *testing.T, pool *pgxpool.Pool, files ...string) (folder *models.MediaFolder, root string, path func(string) string) {
	t.Helper()
	folder = newBookScanTestFolder(t, pool, "audiobooks")
	root = t.TempDir()
	folder.Paths = []string{root}
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_folder_paths (media_folder_id,path) VALUES ($1,$2)`, folder.ID, root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folder_paths WHERE media_folder_id=$1`, folder.ID)
	})
	src, err := os.ReadFile("testdata/audiobook_fixtures/multi_file/part1.mp3")
	if err != nil {
		t.Fatal(err)
	}
	path = func(rel string) string { return filepath.Join(root, rel) }
	for _, rel := range files {
		if err := os.MkdirAll(filepath.Dir(path(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path(rel), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return folder, root, path
}

// A folder whose contents cannot all be read is held: its indexed books stay
// as they are, through the missing-file sweep, until it reads cleanly. Here
// one book has an unreadable disc, and an extras-named folder is indexed as a
// book of its own beside a disc-named folder that holds no audio and then
// cannot be read either.
func TestAudiobookUnreadableDiscKeepsIndexedBookDB(t *testing.T) {
	pool := newBookScanTestPool(t)
	folder, _, path := newAudiobookLibraryWithFiles(t, pool,
		"Author/Book/CD1/01.mp3", "Author/Book/CD2/01.mp3",
		"Author/Other/CD1/cover.jpg", "Author/Other/Extras/e.mp3")
	ctx := t.Context()
	// Empty the trash on every scan, with no grace period, so a file wrongly
	// marked missing would be deleted at once.
	s := NewScanner(NewFileRepository(pool), fakeAudiobookFFprobe(t), nil, 1, true, 0)
	snapshot := func() []string {
		t.Helper()
		// A walk failure leaves the scan's warning set; only the rows matter here.
		_ = s.ScanAudiobookFolder(ctx, folder, true)
		rows, err := pool.Query(ctx, `SELECT mf.file_path || ' ' || mf.content_id || ' ' || COALESCE(mf.presentation_part_index, 0) || '/' ||
			COALESCE(mf.presentation_part_total, 0) || ' ' || (mf.missing_since IS NULL) || ' ' || mf.probe_updated_at::text ||
			' member=' || EXISTS (SELECT 1 FROM media_item_libraries ml WHERE ml.content_id = mf.content_id AND ml.media_folder_id = mf.media_folder_id)
			FROM media_files mf WHERE mf.media_folder_id = $1 ORDER BY mf.file_path`, folder.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	indexed := snapshot()
	if len(indexed) != 3 || !strings.Contains(indexed[0], " 1/2 true ") || !strings.Contains(indexed[1], " 2/2 true ") ||
		!strings.Contains(indexed[2], "Extras/e.mp3") || !strings.Contains(indexed[2], " 0/0 true ") {
		t.Fatalf("indexed rows = %q, want both discs as parts 1/2 and 2/2, and the extras folder as a book", indexed)
	}
	// Replace each folder with a symlink to a missing target, a read failure
	// that holds even when the test runs as root, where a chmod would not.
	locked := []string{path("Author/Book/CD2"), path("Author/Other/CD1")}
	parked := t.TempDir()
	for i, dir := range locked {
		aside := filepath.Join(parked, strconv.Itoa(i))
		if err := os.Rename(dir, aside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(parked, "missing"), dir); err != nil {
			t.Fatal(err)
		}
	}
	if got := snapshot(); !slices.Equal(got, indexed) {
		t.Errorf("with folders unreadable the indexed books changed:\n got %q\nwant %q", got, indexed)
	}
	for i, dir := range locked {
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(parked, strconv.Itoa(i)), dir); err != nil {
			t.Fatal(err)
		}
	}
	if got := snapshot(); !slices.Equal(got, indexed) {
		t.Errorf("once the folders read again the unchanged books changed:\n got %q\nwant %q", got, indexed)
	}
}

// fakeAudiobookFFprobe writes an ffprobe stand-in that reports every file as
// a one-minute mp3, so a scan runs where no ffprobe is installed.
func fakeAudiobookFFprobe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, path, `#!/bin/sh
cat <<'JSON'
{"format":{"format_name":"mp3","duration":"60.0","size":"1","bit_rate":"64000","tags":{"artist":"Author"}},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","channels":2}],"chapters":[]}
JSON
`)
	return path
}

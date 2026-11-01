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
			if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, id, dir, book); err != nil {
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
	if err := s.upsertAudiobookPresentationTx(ctx, first, folder, id, roots[0], books[0]); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		err := s.upsertAudiobookPresentationTx(ctx, second, folder, id, roots[1], books[1])
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

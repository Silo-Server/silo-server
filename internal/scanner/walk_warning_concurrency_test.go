package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Run another scanner's database write at a deterministic point in the older
// scan, without sleeps or a production-only synchronization hook.
type warningInterleaveTracer struct {
	match func(string) bool
	run   func()
	fired atomic.Bool
}

func (t *warningInterleaveTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if t.match(data.SQL) && t.fired.CompareAndSwap(false, true) {
		t.run()
	}
	return ctx
}

func (*warningInterleaveTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func warningInterleavePool(t *testing.T, pool *pgxpool.Pool, tracer *warningInterleaveTracer) *pgxpool.Pool {
	t.Helper()
	config := pool.Config()
	config.ConnConfig.Tracer = tracer
	traced, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	return traced
}

func TestHealthyFullScanPreservesConcurrentPartialWarning(t *testing.T) {
	for _, scan := range []struct {
		kind      string
		withFiles bool
	}{
		{kind: "audiobooks"},
		{kind: "ebooks"},
		{kind: "movies"},
		{kind: "movies", withFiles: true},
		{kind: "manga"},
	} {
		for _, initialWarning := range []bool{false, true} {
			name := scan.kind
			if scan.withFiles {
				name += "/with_files"
			}
			if initialWarning {
				name += "/previous_warning"
			} else {
				name += "/initially_clean"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				pool := newDeadRootTestPool(t)
				folderID := seedDeadRootTestFolder(t, pool, scan.kind, "Concurrent partial walk")
				root := t.TempDir()
				if scan.withFiles {
					if err := os.WriteFile(filepath.Join(root, "Example (2020).mkv"), []byte("fake movie payload"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				folder := &models.MediaFolder{ID: folderID, Type: scan.kind, Paths: []string{root}}
				newer := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
				if initialWarning {
					if err := newer.folderRepo.SetScanWarning(ctx, folderID, partialWalkWarningCode, "Earlier incomplete walk", time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				var expected *models.MediaFolder
				tracer := &warningInterleaveTracer{
					match: func(sql string) bool { return strings.Contains(sql, "FROM media_files") },
					run: func() {
						unreadable := filepath.Join(root, "unreadable")
						if err := os.Symlink(filepath.Join(root, "missing"), unreadable); err != nil {
							t.Fatal(err)
						}
						if _, err := newer.ScanSubtree(ctx, folder, root); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(unreadable); err != nil {
							t.Fatal(err)
						}
						// The newer scan may run on a node whose clock is behind.
						if _, err := pool.Exec(ctx, `UPDATE media_folders SET scan_warning_at = $2 WHERE id = $1`, folderID, time.Now().Add(-time.Hour)); err != nil {
							t.Fatal(err)
						}
						if err := newer.folderRepo.AllowEmptyCleanupOnce(ctx, folderID); err != nil {
							t.Fatal(err)
						}
						var err error
						expected, err = newer.folderRepo.GetByID(ctx, folderID)
						if err != nil {
							t.Fatal(err)
						}
					},
				}
				older := NewScanner(NewFileRepository(warningInterleavePool(t, pool, tracer)), "", nil, 1, true, 0)
				if _, err := older.ScanFolder(ctx, folder); err != nil {
					t.Fatal(err)
				}
				if !tracer.fired.Load() || expected == nil || expected.ScanWarningCode == nil {
					t.Fatal("the overlapping scoped scan did not record its warning")
				}
				actual, err := newer.folderRepo.GetByID(ctx, folderID)
				if err != nil {
					t.Fatal(err)
				}
				if actual.ScanWarningCode == nil || *actual.ScanWarningCode != *expected.ScanWarningCode ||
					actual.ScanWarningMessage == nil || *actual.ScanWarningMessage != *expected.ScanWarningMessage ||
					actual.ScanWarningAt == nil || !actual.ScanWarningAt.Equal(*expected.ScanWarningAt) || !actual.AllowEmptyCleanupOnce {
					t.Fatalf("older full scan changed newer warning or allowance: got %+v; want %+v", actual, expected)
				}
				if _, err := newer.ScanFolder(ctx, folder); err != nil {
					t.Fatal(err)
				}
				actual, err = newer.folderRepo.GetByID(ctx, folderID)
				if err != nil {
					t.Fatal(err)
				}
				if actual.ScanWarningCode != nil || actual.ScanWarningMessage != nil || actual.ScanWarningAt != nil || actual.AllowEmptyCleanupOnce {
					t.Fatalf("fresh healthy full scan did not clear stale warning and allowance: %+v", actual)
				}
			})
		}
	}
}

func TestRecoveredAudiobookWarningSuffixPreservesConcurrentUpdate(t *testing.T) {
	ctx := context.Background()
	pool := newDeadRootTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Concurrent warning suffix")
	root := t.TempDir()
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}}
	newer := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
	const original = "Cleanup requires confirmation."
	const replacement = original + "\nPartial scan: 2 paths remain unreadable."
	warnedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := newer.folderRepo.SetScanWarning(ctx, folderID, "empty_root", original+"\nPartial scan: 1 path was unreadable.", warnedAt); err != nil {
		t.Fatal(err)
	}
	tracer := &warningInterleaveTracer{
		match: func(sql string) bool {
			return strings.Contains(sql, "UPDATE media_folders") && strings.Contains(sql, "scan_warning_code")
		},
		run: func() {
			// Even identical code/timestamp must not hide a changed message.
			if err := newer.folderRepo.SetScanWarning(ctx, folderID, "empty_root", replacement, warnedAt); err != nil {
				t.Fatal(err)
			}
			if err := newer.folderRepo.AllowEmptyCleanupOnce(ctx, folderID); err != nil {
				t.Fatal(err)
			}
		},
	}
	older := NewScanner(NewFileRepository(warningInterleavePool(t, pool, tracer)), "", nil, 1, true, 0)
	if err := older.ScanAudiobookFolder(ctx, folder, true); err != nil {
		t.Fatal(err)
	}
	actual, err := newer.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		t.Fatal(err)
	}
	if !tracer.fired.Load() || actual.ScanWarningMessage == nil || *actual.ScanWarningMessage != replacement || !actual.AllowEmptyCleanupOnce {
		t.Fatalf("older scan overwrote concurrent warning suffix or allowance: %+v", actual)
	}
	if err := newer.ScanAudiobookFolder(ctx, folder, true); err != nil {
		t.Fatal(err)
	}
	actual, err = newer.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ScanWarningCode == nil || *actual.ScanWarningCode != "empty_root" ||
		actual.ScanWarningMessage == nil || *actual.ScanWarningMessage != original ||
		actual.ScanWarningAt == nil || !actual.ScanWarningAt.Equal(warnedAt) || !actual.AllowEmptyCleanupOnce {
		t.Fatalf("recovered scan should strip only the stale suffix: %+v", actual)
	}
}

func TestPartialWalkWarningPreservesConcurrentCleanupWarning(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		for _, initialCode := range []string{"", "dead_root"} {
			t.Run(fmt.Sprintf("preserve=%t/initial=%s", preserve, initialCode), func(t *testing.T) {
				ctx := t.Context()
				pool := newDeadRootTestPool(t)
				folderID := seedDeadRootTestFolder(t, pool, "ebooks", "Concurrent cleanup warning")
				newer := NewScanner(NewFileRepository(pool), "", nil, 1, true, 0)
				if initialCode != "" {
					if err := newer.folderRepo.SetScanWarning(ctx, folderID, initialCode, "Previous outage", time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				const message = "Scan found 0 media files; cleanup requires confirmation."
				warnedAt := time.Now().UTC().Truncate(time.Microsecond)
				tracer := &warningInterleaveTracer{
					match: func(sql string) bool {
						return strings.Contains(sql, "UPDATE media_folders") && strings.Contains(sql, "scan_warning_code")
					},
					run: func() {
						if err := newer.folderRepo.SetScanWarning(ctx, folderID, "empty_root", message, warnedAt); err != nil {
							t.Fatal(err)
						}
						if err := newer.folderRepo.AllowEmptyCleanupOnce(ctx, folderID); err != nil {
							t.Fatal(err)
						}
					},
				}
				older := NewScanner(NewFileRepository(warningInterleavePool(t, pool, tracer)), "", nil, 1, true, 0)
				if err := older.setPartialWalkWarning(ctx, folderID, 1, preserve); err != nil {
					t.Fatal(err)
				}
				actual, err := newer.folderRepo.GetByID(ctx, folderID)
				if err != nil {
					t.Fatal(err)
				}
				if !tracer.fired.Load() || actual.ScanWarningCode == nil || *actual.ScanWarningCode != "empty_root" ||
					actual.ScanWarningMessage == nil || *actual.ScanWarningMessage != message ||
					actual.ScanWarningAt == nil || !actual.ScanWarningAt.Equal(warnedAt) || !actual.AllowEmptyCleanupOnce {
					t.Fatalf("partial scan overwrote concurrent cleanup warning or allowance: %+v", actual)
				}
			})
		}
	}
}

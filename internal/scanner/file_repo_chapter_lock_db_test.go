package scanner

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestChapterLockSessionsRespectPoolBudgetDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	for _, maxConns := range []int32{1, 2, 8, 32} {
		t.Run(strconv.Itoa(int(maxConns)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cfg, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MaxConns = maxConns
			cfg.MinConns = 0
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			first, second := NewFileRepository(pool), NewFileRepository(pool)
			limit := min(4, max(1, int(maxConns)/4))
			baseID := int(time.Now().UnixNano())
			var names []string
			var releases []func()
			for i := range limit {
				_, release, acquired, err := first.TryLockChapterThumbnails(ctx, baseID+i)
				if err != nil || !acquired {
					t.Fatalf("lock %d: acquired=%v err=%v", i, acquired, err)
				}
				t.Cleanup(release)
				releases = append(releases, release)
				names = append(names, "silo-chapter-thumbnails:"+strconv.Itoa(baseID+i))
			}
			// Waiting admission shares capacity across repositories without opening
			// another session. Cancellation releases each waiting worker.
			var workers sync.WaitGroup
			for i := range 128 {
				workers.Go(func() {
					waitCtx, waitCancel := context.WithTimeout(ctx, 50*time.Millisecond)
					defer waitCancel()
					_, release, acquired, err := second.TryLockChapterThumbnails(waitCtx, baseID+limit+i)
					if release != nil {
						release()
					}
					if !errors.Is(err, context.DeadlineExceeded) || acquired {
						t.Errorf("saturated admission: acquired=%v err=%v", acquired, err)
					}
				})
			}
			workers.Wait()
			// Every admitted lock leaves the ordinary pool usable, including the
			// single-connection fallback used by extraction/settings reads.
			queryCtx, queryCancel := context.WithTimeout(ctx, time.Second)
			defer queryCancel()
			var active int
			if err := pool.QueryRow(queryCtx, `SELECT count(*) FROM pg_stat_activity
				WHERE application_name=ANY($1::text[])`, names).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if active != limit {
				t.Fatalf("active chapter sessions=%d, want %d", active, limit)
			}
			if got := pool.Stat().AcquiredConns(); maxConns > 1 && got != int32(limit) {
				t.Fatalf("held pooled connections=%d, want %d", got, limit)
			}
			for _, release := range releases {
				release()
				release()
			}
			chapterLockSessions.Lock()
			_, retained := chapterLockSessions.budgets[pool]
			chapterLockSessions.Unlock()
			if retained {
				t.Fatal("session registry retained the pool after all releases")
			}
			// Releasing admission permits the next file to run.
			_, release, acquired, err := second.TryLockChapterThumbnails(ctx, baseID+1024)
			if err != nil || !acquired {
				t.Fatalf("next admission: acquired=%v err=%v", acquired, err)
			}
			release()
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE application_name=ANY($1::text[])`, names).Scan(&active); err != nil || active != 0 {
				t.Fatalf("released sessions: count=%d err=%v", active, err)
			}
		})
	}
}

func TestChapterLockFailedConnectReleasesBudgetDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := NewFileRepository(pool)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, acquired, err := repo.TryLockChapterThumbnails(ctx, 1); err == nil || acquired {
		t.Fatalf("canceled connect: acquired=%v err=%v", acquired, err)
	}
	_, release, acquired, err := repo.TryLockChapterThumbnails(t.Context(), int(time.Now().UnixNano()))
	if err != nil || !acquired {
		t.Fatalf("failure retained admission: acquired=%v err=%v", acquired, err)
	}
	release()
}

func TestChapterLockSessionsSerializeDistinctPoolsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	newReplica := func() *FileRepository {
		t.Helper()
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns = 2
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return NewFileRepository(pool)
	}
	first, second := newReplica(), newReplica()
	fileID := int(time.Now().UnixNano())
	_, release, acquired, err := first.TryLockChapterThumbnails(ctx, fileID)
	if err != nil || !acquired {
		t.Fatalf("first replica: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(release)
	_, secondRelease, secondAcquired, secondErr := second.TryLockChapterThumbnails(ctx, fileID)
	if secondRelease != nil {
		secondRelease()
	}
	if secondErr != nil || secondAcquired {
		t.Fatalf("replica contention: acquired=%v err=%v", secondAcquired, secondErr)
	}
	release()
	_, secondRelease, secondAcquired, secondErr = second.TryLockChapterThumbnails(ctx, fileID)
	if secondErr != nil || !secondAcquired {
		t.Fatalf("replica after release: acquired=%v err=%v", secondAcquired, secondErr)
	}
	secondRelease()
}

func TestChapterLockReusesHealthySessionsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	newReplica := func() (*FileRepository, *pgxpool.Pool) {
		t.Helper()
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns, cfg.MinConns = 2, 0
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return NewFileRepository(pool), pool
	}
	first, pool := newReplica()
	second, contendedPool := newReplica()
	fileID := int(time.Now().UnixNano())
	var firstPID int
	for i := range 5 {
		lockCtx, release, acquired, err := first.TryLockChapterThumbnails(ctx, fileID)
		if err != nil || !acquired {
			t.Fatalf("acquire %d: acquired=%v err=%v", i, acquired, err)
		}
		t.Cleanup(release)
		var pid int
		if err := first.chapterStateWriter(lockCtx, fileID).QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstPID = pid
		} else if pid != firstPID {
			t.Fatalf("healthy extraction reconnected: PID=%d, first=%d", pid, firstPID)
		}
		for range 5 {
			_, failedRelease, gotLock, err := second.TryLockChapterThumbnails(ctx, fileID)
			if failedRelease != nil {
				failedRelease()
			}
			if err != nil || gotLock {
				t.Fatalf("contended attempt: acquired=%v err=%v", gotLock, err)
			}
		}
		if got := contendedPool.Stat().NewConnsCount(); got != 1 {
			t.Fatalf("healthy contention created %d physical connections, want 1", got)
		}
		release()
		if pool.Stat().AcquiredConns() != 0 || contendedPool.Stat().AcquiredConns() != 0 {
			t.Fatal("release retained a pooled connection")
		}
		var name string
		if err := pool.QueryRow(ctx, `SELECT pg_backend_pid(), current_setting('application_name')`).Scan(&pid, &name); err != nil {
			t.Fatal(err)
		}
		if pid != firstPID || name == "silo-chapter-thumbnails:"+strconv.Itoa(fileID) {
			t.Fatalf("released connection: PID=%d name=%q, want original PID and application name", pid, name)
		}
	}
	if got := pool.Stat().NewConnsCount(); got != 1 {
		t.Fatalf("healthy releases created %d physical connections, want 1", got)
	}
	_, release, acquired, err := second.TryLockChapterThumbnails(ctx, fileID)
	if err != nil || !acquired {
		t.Fatalf("release did not unlock for the other replica: acquired=%v err=%v", acquired, err)
	}
	release()
}

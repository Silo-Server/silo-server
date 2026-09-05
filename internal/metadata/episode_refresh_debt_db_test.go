package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func episodeRefreshDebtTestPool(t *testing.T) (*pgxpool.Pool, *seasonEpisodeQueryTracer) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &seasonEpisodeQueryTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// A session-local table isolates these assertions from the database's
	// catalog and avoids requiring an entire migrated Silo deployment.
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE episodes (content_id TEXT PRIMARY KEY, series_id TEXT NOT NULL);
	CREATE TEMP TABLE metadata_refresh_debt (
		target_type TEXT NOT NULL, content_id TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0, reason_mask BIGINT NOT NULL DEFAULT 0,
		next_refresh_at TIMESTAMPTZ NOT NULL, claimed_at TIMESTAMPTZ,
		lease_expires_at TIMESTAMPTZ, last_attempt_at TIMESTAMPTZ, last_success_at TIMESTAMPTZ,
		attempt_count INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (target_type, content_id)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return pool, tracer
}

func TestRefreshSeriesEpisodeMetadataStateDebtQueries(t *testing.T) {
	pool, tracer := episodeRefreshDebtTestPool(t)
	ctx := t.Context()
	for _, count := range []int{1, 100, 1000} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("episodes=%d/mixed=%t", count, mixed), func(t *testing.T) {
				if _, err := pool.Exec(ctx, `TRUNCATE metadata_refresh_debt, episodes`); err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC().Truncate(time.Microsecond)
				episodes := newFakeEpisodeRepo()
				items := newFakeItemRepo()
				items.items["series"] = &models.MediaItem{ContentID: "series"}
				for i := range count {
					id := fmt.Sprintf("episode-%d", i)
					episodes.episodes[id] = &models.Episode{ContentID: id, SeriesID: "series", Title: "Complete", TmdbID: "123"}
				}
				if _, err := pool.Exec(ctx, `INSERT INTO episodes SELECT 'episode-' || n, 'series' FROM generate_series(0, $1::int - 1) n`, count); err != nil {
					t.Fatal(err)
				}
				if mixed {
					episodes.episodes["incomplete"] = &models.Episode{ContentID: "incomplete", SeriesID: "series", Title: "TBA"}
				}
				_, err := pool.Exec(ctx, `INSERT INTO metadata_refresh_debt (target_type, content_id, next_refresh_at)
					SELECT 'episode', 'episode-' || n, NOW() FROM generate_series(0, $1::int - 1) n;
				`, count)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO metadata_refresh_debt (target_type, content_id, next_refresh_at)
					VALUES ('item', 'episode-0', NOW()), ('episode', 'unrelated', NOW())`); err != nil {
					t.Fatal(err)
				}
				service := &MetadataService{itemRepo: items, episodeRepo: episodes, refreshDebtRepo: NewRefreshDebtRepository(pool)}
				tracer.reset()
				started := time.Now()
				service.refreshSeriesEpisodeMetadataState(ctx, "series", now)
				queries := tracer.count()
				t.Logf("complete=%d mixed=%t queries=%d elapsed=%s", count, mixed, queries, time.Since(started))
				wantQueries := 2
				if mixed {
					wantQueries += 2
				}
				if queries != wantQueries {
					t.Fatalf("queries = %d, want %d", queries, wantQueries)
				}
				var remaining int
				if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM metadata_refresh_debt`).Scan(&remaining); err != nil {
					t.Fatal(err)
				}
				wantRemaining := 2
				if mixed {
					wantRemaining++
					debt, err := service.refreshDebtRepo.GetTarget(ctx, RefreshTargetEpisode, "incomplete")
					if err != nil || debt.ReasonMask != RefreshDebtReasonEpisodeIncomplete || !debt.NextRefreshAt.Equal(now.Add(24*time.Hour)) {
						t.Fatalf("incomplete debt = %#v, error = %v", debt, err)
					}
				}
				if remaining != wantRemaining || items.items["series"].EpisodeMetadataIncomplete != mixed {
					t.Fatalf("remaining debt = %d, want %d; series incomplete = %t", remaining, wantRemaining, items.items["series"].EpisodeMetadataIncomplete)
				}
			})
		}
	}
}

// Keep the complete episode first so this interleaving also preserves the
// behavior of the original per-episode cleanup loop.
type orderedDebtEpisodeRepo struct {
	metadataEpisodeRepo
	episodes []*models.Episode
}

func (r *orderedDebtEpisodeRepo) ListBySeries(context.Context, string) ([]*models.Episode, error) {
	return r.episodes, nil
}

type failureDuringEpisodeSyncRepo struct {
	*RefreshDebtRepository
	retryAt time.Time
}

func (r *failureDuringEpisodeSyncRepo) GetTarget(ctx context.Context, targetType, contentID string) (*models.MetadataRefreshDebt, error) {
	if targetType == RefreshTargetEpisode && contentID == "incomplete" {
		// Another refresh records a failure after ListBySeries returned a
		// complete snapshot, while this refresh is syncing another episode.
		if err := r.MarkTargetFailure(ctx, RefreshTargetEpisode, "complete", 300,
			RefreshDebtReasonEpisodeIncomplete, r.retryAt, 2, "newer provider failure"); err != nil {
			return nil, err
		}
	}
	return r.RefreshDebtRepository.GetTarget(ctx, targetType, contentID)
}

func TestRefreshSeriesEpisodeMetadataStateRetainsFailureDuringIncompleteSync(t *testing.T) {
	pool, tracer := episodeRefreshDebtTestPool(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	debts := &failureDuringEpisodeSyncRepo{
		RefreshDebtRepository: NewRefreshDebtRepository(pool),
		retryAt:               now.Add(time.Hour),
	}
	if err := debts.MarkTargetFailure(ctx, RefreshTargetEpisode, "complete", 300,
		RefreshDebtReasonEpisodeIncomplete, now, 1, "old failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes VALUES ('complete', 'series'), ('incomplete', 'series')`); err != nil {
		t.Fatal(err)
	}
	items := newFakeItemRepo()
	items.items["series"] = &models.MediaItem{ContentID: "series"}
	episodes := &orderedDebtEpisodeRepo{episodes: []*models.Episode{
		{ContentID: "complete", SeriesID: "series", Title: "Complete", TmdbID: "123"},
		{ContentID: "incomplete", SeriesID: "series", Title: "TBA"},
	}}
	service := &MetadataService{itemRepo: items, episodeRepo: episodes, refreshDebtRepo: debts}
	tracer.reset()
	service.refreshSeriesEpisodeMetadataState(ctx, "series", now)
	tracer.mu.Lock()
	deletes := 0
	for _, query := range tracer.queries {
		if strings.HasPrefix(strings.TrimSpace(query), "DELETE FROM metadata_refresh_debt") {
			deletes++
		}
	}
	tracer.mu.Unlock()
	if deletes != 1 {
		t.Fatalf("debt DELETE statements = %d, want 1", deletes)
	}
	debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "complete")
	if err != nil {
		t.Fatalf("newer retry was lost: %v", err)
	}
	if debt.AttemptCount != 2 || debt.LastError != "newer provider failure" || !debt.NextRefreshAt.Equal(debts.retryAt) {
		t.Fatalf("newer retry = %#v", debt)
	}
}

type failureDuringEpisodeListRepo struct {
	*orderedDebtEpisodeRepo
	recordFailure func(context.Context) error
}

func (r *failureDuringEpisodeListRepo) ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error) {
	episodes, err := r.orderedDebtEpisodeRepo.ListBySeries(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	// The completeness snapshot is already read when another refresh finishes.
	if err := r.recordFailure(ctx); err != nil {
		return nil, err
	}
	return episodes, nil
}

func TestRefreshSeriesEpisodeMetadataStateRetainsFailureAfterEpisodeSnapshot(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			pool, _ := episodeRefreshDebtTestPool(t)
			ctx := t.Context()
			debts := NewRefreshDebtRepository(pool)
			now := time.Now().UTC().Truncate(time.Microsecond)
			if _, err := pool.Exec(ctx, `INSERT INTO episodes VALUES ('complete', 'series'), ('unchanged', 'series')`); err != nil {
				t.Fatal(err)
			}
			if err := debts.MarkTargetFailure(ctx, RefreshTargetEpisode, "unchanged", 300,
				RefreshDebtReasonEpisodeIncomplete, now, 1, "old failure"); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := debts.MarkTargetFailure(ctx, RefreshTargetEpisode, "complete", 300,
					RefreshDebtReasonEpisodeIncomplete, now, 1, "old failure"); err != nil {
					t.Fatal(err)
				}
			}
			items := newFakeItemRepo()
			items.items["series"] = &models.MediaItem{ContentID: "series"}
			episodes := &failureDuringEpisodeListRepo{
				orderedDebtEpisodeRepo: &orderedDebtEpisodeRepo{episodes: []*models.Episode{
					{ContentID: "complete", SeriesID: "series", Title: "Complete", TmdbID: "123"},
					{ContentID: "unchanged", SeriesID: "series", Title: "Complete", TmdbID: "456"},
				}},
				recordFailure: func(ctx context.Context) error {
					return debts.MarkTargetFailure(ctx, RefreshTargetEpisode, "complete", 300,
						RefreshDebtReasonEpisodeIncomplete, now.Add(time.Hour), 2, "newer provider failure")
				},
			}
			service := &MetadataService{itemRepo: items, episodeRepo: episodes, refreshDebtRepo: debts}
			service.refreshSeriesEpisodeMetadataState(ctx, "series", now)
			debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "complete")
			if err != nil {
				t.Fatalf("newer retry was lost: %v", err)
			}
			if debt.AttemptCount != 2 || debt.LastError != "newer provider failure" || !debt.NextRefreshAt.Equal(now.Add(time.Hour)) {
				t.Fatalf("newer retry = %#v", debt)
			}
			if _, err := debts.GetTarget(ctx, RefreshTargetEpisode, "unchanged"); !errors.Is(err, ErrRefreshDebtNotFound) {
				t.Fatalf("unchanged complete debt error = %v, want ErrRefreshDebtNotFound", err)
			}
		})
	}
}

func TestDeleteEpisodeDebtsRetainsSameTransactionRewrite(t *testing.T) {
	pool, _ := episodeRefreshDebtTestPool(t)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO metadata_refresh_debt (target_type, content_id, next_refresh_at)
		VALUES ('episode', 'complete', NOW())`); err != nil {
		t.Fatal(err)
	}
	// Both writes have the same xmin and NOW(). Only the tuple location changes.
	var version string
	if err := tx.QueryRow(ctx, `SELECT xmin::text || ':' || ctid::text FROM metadata_refresh_debt
		WHERE target_type = 'episode' AND content_id = 'complete'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE metadata_refresh_debt SET last_error = 'newer provider failure',
		updated_at = NOW() WHERE target_type = 'episode' AND content_id = 'complete'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	debts := NewRefreshDebtRepository(pool)
	if err := debts.DeleteEpisodeDebts(ctx, []string{"complete"}, map[string]string{"complete": version}); err != nil {
		t.Fatal(err)
	}
	debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "complete")
	if err != nil || debt.LastError != "newer provider failure" {
		t.Fatalf("rewritten debt = %#v, error = %v", debt, err)
	}
}

func TestDeleteEpisodeDebtsRetainsConcurrentUpdateWhileDeleteWaits(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// A private schema lets the updater, deleter and lock observer use separate
	// connections without touching deployment data. This test needs migrations.
	schema := fmt.Sprintf("episode_debt_wait_%d", time.Now().UnixNano())
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("cleaning up test schema: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `CREATE TABLE metadata_refresh_debt (LIKE public.metadata_refresh_debt INCLUDING ALL);
		CREATE TABLE episodes (content_id TEXT PRIMARY KEY, series_id TEXT NOT NULL);
		INSERT INTO episodes VALUES ('complete', 'series')`); err != nil {
		t.Fatal(err)
	}
	debts := NewRefreshDebtRepository(pool)
	if err := debts.MarkTargetFailure(ctx, RefreshTargetEpisode, "complete", 300,
		RefreshDebtReasonEpisodeIncomplete, time.Now(), 1, "old failure"); err != nil {
		t.Fatal(err)
	}
	versions, err := debts.SnapshotEpisodeDebts(ctx, "series")
	if err != nil {
		t.Fatal(err)
	}
	deleterConfig := config.Copy()
	deleterConfig.MaxConns = 1
	deleterPool, err := pgxpool.NewWithConfig(ctx, deleterConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deleterPool.Close)
	var deleterPID int
	if err := deleterPool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&deleterPID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	updaterPID := tx.Conn().PgConn().PID()
	if _, err := tx.Exec(ctx, `UPDATE metadata_refresh_debt SET last_error = 'newer provider failure',
		attempt_count = 2 WHERE target_type = 'episode' AND content_id = 'complete'`); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() {
		deleted <- NewRefreshDebtRepository(deleterPool).DeleteEpisodeDebts(ctx, []string{"complete"}, versions)
	}()
	for {
		select {
		case err := <-deleted:
			t.Fatalf("DELETE finished before waiting for the updater: %v", err)
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT $1::int = ANY(pg_blocking_pids($2::int))`, updaterPID, deleterPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	debt, err := debts.GetTarget(ctx, RefreshTargetEpisode, "complete")
	if err != nil || debt.LastError != "newer provider failure" || debt.AttemptCount != 2 {
		t.Fatalf("concurrent retry = %#v, error = %v", debt, err)
	}
}

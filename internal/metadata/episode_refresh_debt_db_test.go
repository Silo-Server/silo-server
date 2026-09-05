package metadata

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRefreshSeriesEpisodeMetadataStateDebtQueries(t *testing.T) {
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
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE metadata_refresh_debt (
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
	for _, count := range []int{1, 100, 1000} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("episodes=%d/mixed=%t", count, mixed), func(t *testing.T) {
				if _, err := pool.Exec(ctx, `TRUNCATE metadata_refresh_debt`); err != nil {
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
				wantQueries := 1
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

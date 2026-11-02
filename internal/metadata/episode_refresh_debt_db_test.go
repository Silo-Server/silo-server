package metadata

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestRestartTerminalEpisodeDebtDB covers restarting terminal series debt for
// a newly aired by-date file: the row keeps its other reasons and only
// restarts while it is terminal, not due, unleased, and last attempted before
// the file aired, as checked when the statement runs.
func TestRestartTerminalEpisodeDebtDB(t *testing.T) {
	pool, _ := episodeRefreshDebtTestPool(t)
	repo := NewRefreshDebtRepository(pool)
	ctx := t.Context()
	pending := RefreshDebtReasonCoreMetadataIncomplete | RefreshDebtReasonTrailersRequested | RefreshDebtReasonProviderArtwork
	for _, tc := range []struct {
		name          string
		reasonMask    int64
		attempts      int
		set           string
		attemptedDays int
		wantRestarted bool
	}{
		{name: "parked episode debt", reasonMask: RefreshDebtReasonEpisodeIncomplete, attempts: refreshDebtEpisodeTerminalAttempts, wantRestarted: true},
		{name: "terminal debt for other reasons", reasonMask: pending, attempts: 5, wantRestarted: true},
		{name: "attempts left", reasonMask: pending, attempts: refreshDebtEpisodeTerminalAttempts - 1},
		{name: "due", reasonMask: pending, attempts: 5, set: `next_refresh_at = NOW() - INTERVAL '1 minute'`},
		{name: "leased", reasonMask: pending, attempts: 5, set: `lease_expires_at = NOW() + INTERVAL '15 minutes'`},
		{name: "attempted after the file aired", reasonMask: pending, attempts: 5, attemptedDays: -2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `DELETE FROM metadata_refresh_debt`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO metadata_refresh_debt
				(target_type, content_id, priority, reason_mask, next_refresh_at, last_attempt_at, attempt_count, last_error)
				VALUES ('item', 'series-1', 50, $1, NOW() + INTERVAL '30 days', NOW() - INTERVAL '1 day', $2, 'not listed')`,
				tc.reasonMask, tc.attempts); err != nil {
				t.Fatal(err)
			}
			if tc.set != "" {
				if _, err := pool.Exec(ctx, `UPDATE metadata_refresh_debt SET `+tc.set); err != nil {
					t.Fatal(err)
				}
			}
			before, err := repo.GetTarget(ctx, RefreshTargetItem, "series-1")
			if err != nil {
				t.Fatal(err)
			}

			now := time.Now()
			err = repo.RestartTerminalEpisodeDebt(ctx, RefreshTargetItem, "series-1", now.AddDate(0, 0, tc.attemptedDays), now)
			if err != nil {
				t.Fatal(err)
			}
			after, err := repo.GetTarget(ctx, RefreshTargetItem, "series-1")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantRestarted {
				if after.ReasonMask != before.ReasonMask|RefreshDebtReasonEpisodeIncomplete || after.AttemptCount != before.AttemptCount ||
					!after.NextRefreshAt.Equal(before.NextRefreshAt) || after.Priority != before.Priority ||
					!reflect.DeepEqual(after.LeaseExpiresAt, before.LeaseExpiresAt) {
					t.Fatalf("rejected reset should only add the episode reason: after=%+v before=%+v", after, before)
				}
				return
			}
			if after.ReasonMask != tc.reasonMask|RefreshDebtReasonEpisodeIncomplete || after.AttemptCount != 0 ||
				after.Priority != refreshDebtPriority(RefreshDebtReasonEpisodeIncomplete) || after.NextRefreshAt.After(now) ||
				after.LastError != "not listed" {
				t.Fatalf("restarted row = %+v, want reasons %d due now with no attempts", after, tc.reasonMask|RefreshDebtReasonEpisodeIncomplete)
			}
		})
	}
}

// A core refresh must not settle away the only retry for an unlinked by-date
// file when its due or leased debt cannot restart yet.
func TestByDateEpisodeDebtSurvivesCoreRefreshDB(t *testing.T) {
	for _, leased := range []bool{false, true} {
		t.Run(fmt.Sprintf("leased=%t", leased), func(t *testing.T) {
			pool, _ := episodeRefreshDebtTestPool(t)
			repo := NewRefreshDebtRepository(pool)
			h := newFallbackTestHarness()
			h.service.refreshDebtRepo = repo
			ctx := t.Context()
			const seriesID = "series-daily-behind"
			seedAirDateSeries(ctx, t, h, seriesID,
				&models.Episode{ContentID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1, Title: "Wednesday", TvdbID: "1001", AirDate: mustDate(t, "2024-05-01")})
			addAirDateFile(h, seriesID, 301, "/media/tv/Daily Show (2024)/Daily Show (2024) - 2024-05-06 - Episode.mkv")
			_, err := pool.Exec(ctx, `INSERT INTO metadata_refresh_debt
				(target_type, content_id, priority, reason_mask, next_refresh_at, last_attempt_at, attempt_count, lease_expires_at)
				VALUES ('item', $1, 150, $2, NOW() - INTERVAL '1 minute', '2024-05-06 12:00:00+00', 5,
				CASE WHEN $3 THEN NOW() + INTERVAL '15 minutes' ELSE NULL END)`, seriesID, RefreshDebtReasonCoreMetadataIncomplete, leased)
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.Get(ctx, seriesID)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
				t.Fatal(err)
			}
			after, err := repo.Get(ctx, seriesID)
			if err != nil {
				t.Fatal(err)
			}
			if after.AttemptCount != before.AttemptCount || after.Priority != before.Priority ||
				!after.NextRefreshAt.Equal(before.NextRefreshAt) || !reflect.DeepEqual(after.LeaseExpiresAt, before.LeaseExpiresAt) {
				t.Fatalf("sweep changed schedule or claim: before=%+v after=%+v", before, after)
			}
			if after.ReasonMask != RefreshDebtReasonCoreMetadataIncomplete|RefreshDebtReasonEpisodeIncomplete {
				t.Errorf("sweep reasons = %d, want core and episode debt", after.ReasonMask)
			}

			// The provider completes core metadata but still omits the new episode.
			item, err := h.itemRepo.GetByID(ctx, seriesID)
			if err != nil {
				t.Fatal(err)
			}
			item.TmdbID, item.Overview = "123", "Complete"
			item.PosterPath, item.BackdropPath = testPosterPath, testBackdropPath
			item.RatingTMDB = new(7.5)
			if err := h.itemRepo.Upsert(ctx, item); err != nil {
				t.Fatal(err)
			}
			if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
				t.Fatal(err)
			}
			if err := h.service.syncRefreshDebtForItem(ctx, seriesID); err != nil {
				t.Fatal(err)
			}
			settled, err := repo.Get(ctx, seriesID)
			if err != nil {
				t.Fatalf("core refresh lost the episode retry: %v", err)
			}
			if settled.ReasonMask != RefreshDebtReasonEpisodeIncomplete || settled.AttemptCount != before.AttemptCount ||
				settled.LeaseExpiresAt != nil || !settled.NextRefreshAt.After(time.Now()) {
				t.Fatalf("settled debt = %+v, want episode retry with its backoff", settled)
			}
		})
	}
}

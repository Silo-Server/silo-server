package dashmetrics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// samplerTestPool opens a pool on a fresh schema holding empty copies of the
// tables the sampler reads and writes, so the test sees only its own rows on a
// shared migrated database.
func samplerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{fmt.Sprintf("dashmetrics_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, table := range []string{
		"dashboard_metric_samples",
		"dashboard_node_egress_samples",
		"dashboard_metric_daily",
		"dashboard_node_egress_daily",
		"stream_nodes",
		"playback_sessions_sync",
	} {
		if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, table, table)); err != nil {
			t.Fatal(err)
		}
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSampleSharedRecordsEgressPerNodeDB: the shared row keeps the cluster's
// node egress total, and every enabled, healthy node that is serving traffic
// gets a dashboard_node_egress_samples row from the same snapshot. Idle,
// disabled, and unhealthy nodes write no row. A replica that loses the race
// for the minute's shared row writes no node rows either, so a minute's split
// always comes from the replica whose total won.
func TestSampleSharedRecordsEgressPerNodeDB(t *testing.T) {
	pool := samplerTestPool(t)
	ctx := t.Context()

	nodeID := func(name string, enabled, healthy bool, kbps int) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO stream_nodes (name, type, url, enabled, healthy, egress_kbps)
			VALUES ($1, 'proxy', 'http://' || $1, $2, $3, $4) RETURNING id
		`, name, enabled, healthy, kbps).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	busy := nodeID("busy", true, true, 4000)
	other := nodeID("other", true, true, 1500)
	nodeID("idle", true, true, 0)
	nodeID("disabled", false, true, 9000)
	nodeID("unhealthy", true, false, 7000)

	samples := func() []string {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT source, egress_kbps FROM dashboard_metric_samples
			UNION ALL
			SELECT 'node:' || node_id, egress_kbps FROM dashboard_node_egress_samples
			ORDER BY 2 DESC
		`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var source string
			var kbps int64
			err := row.Scan(&source, &kbps)
			return fmt.Sprintf("%s=%d", source, kbps), err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	NewSampler(pool, nil, "replica-a").sampleShared(ctx)
	want := []string{
		"shared=5500",
		fmt.Sprintf("node:%d=4000", busy),
		fmt.Sprintf("node:%d=1500", other),
	}
	if got := samples(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}

	// A replica that loses the race for a minute's shared row must not land
	// node rows either. Stand in for the winner with shared rows for this
	// minute and the next, so the check holds even if the call below crosses a
	// minute boundary.
	if _, err := pool.Exec(ctx, `TRUNCATE dashboard_metric_samples, dashboard_node_egress_samples`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples (bucket, source, egress_kbps)
		SELECT date_trunc('minute', now()) + make_interval(mins => m), 'shared', 1 FROM generate_series(0, 1) m
	`); err != nil {
		t.Fatal(err)
	}
	NewSampler(pool, nil, "replica-b").sampleShared(ctx)
	if got := samples(); fmt.Sprint(got) != fmt.Sprint([]string{"shared=1", "shared=1"}) {
		t.Fatalf("after a losing replica: samples = %v, want only the winner's shared rows", got)
	}
}

func seedDownsampleTestSamples(t *testing.T, pool *pgxpool.Pool, m1, m2, mRecent, mToday time.Time) {
	t.Helper()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples (bucket, source, streams_total, streams_direct, streams_remux, streams_transcode, egress_kbps, download_egress_kbps)
		VALUES ($1, 'shared', 5, 3, 1, 1, 12000, 0),
		       ($1, 'proc:api-1', 0, 0, 0, 0, 3000, 1000),
		       ($2, 'shared', 8, 5, 2, 1, 8000, 0),
		       ($2, 'proc:api-1', 0, 0, 0, 0, 2000, 500),
		       ($3, 'shared', 2, 2, 0, 0, 4000, 0),
		       ($4, 'shared', 9, 9, 0, 0, 20000, 0)`,
		m1, m2, mRecent, mToday); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps)
		VALUES ($1, 10, 7000), ($1, 20, 5000),
		       ($2, 10, 5000), ($2, 20, 3000),
		       ($3, 10, 4000),
		       ($4, 10, 20000)`,
		m1, m2, mRecent, mToday); err != nil {
		t.Fatal(err)
	}
}

func assertOldDayRollup(t *testing.T, pool *pgxpool.Pool, oldDay, m1, m2 time.Time) {
	t.Helper()
	ctx := t.Context()
	var peakStreams, peakDirect, peakRemux, peakTranscode int
	var peakEgress, peakDL, apiPeak int
	var egressBytes, dlBytes, apiBytes int64
	var peakStreamsAt, peakEgressAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT peak_streams, peak_streams_direct, peak_streams_remux, peak_streams_transcode,
		       peak_egress_kbps, peak_download_kbps, api_peak_egress_kbps,
		       egress_bytes, download_egress_bytes, api_egress_bytes,
		       peak_streams_at, peak_egress_at
		FROM dashboard_metric_daily WHERE day = $1`, oldDay.Format("2006-01-02")).Scan(
		&peakStreams, &peakDirect, &peakRemux, &peakTranscode,
		&peakEgress, &peakDL, &apiPeak,
		&egressBytes, &dlBytes, &apiBytes,
		&peakStreamsAt, &peakEgressAt,
	); err != nil {
		t.Fatalf("query oldDay daily row: %v", err)
	}
	if peakStreams != 8 || peakDirect != 5 || peakRemux != 2 || peakTranscode != 1 {
		t.Fatalf("oldDay stream peaks = %d(%d/%d/%d), want 8(5/2/1)", peakStreams, peakDirect, peakRemux, peakTranscode)
	}
	if peakEgress != 15000 || peakDL != 1000 || apiPeak != 3000 ||
		egressBytes != 187500000 || dlBytes != 11250000 || apiBytes != 37500000 {
		t.Fatalf("oldDay egress = peak %d dl %d api %d bytes %d/%d/%d", peakEgress, peakDL, apiPeak, egressBytes, dlBytes, apiBytes)
	}
	if !peakStreamsAt.Equal(m2) || !peakEgressAt.Equal(m1) {
		t.Fatalf("oldDay peak timestamps = streams %v egress %v, want %v and %v", peakStreamsAt, peakEgressAt, m2, m1)
	}
}

// TestDownsampleCompletedDaysAndPruneDB verifies that completed UTC days roll
// up into dashboard_metric_daily and dashboard_node_egress_daily before
// retention pruning, that today's partial UTC day is left unarchived, that
// monotonic upserts preserve totals if an older replica pruned raw rows early,
// and that pruning aligns to whole UTC days.
func TestDownsampleCompletedDaysAndPruneDB(t *testing.T) {
	pool := samplerTestPool(t)
	ctx := t.Context()

	todayUTC := time.Now().UTC().Truncate(24 * time.Hour)
	oldDay := todayUTC.AddDate(0, 0, -40)
	recentDay := todayUTC.AddDate(0, 0, -2)
	m1 := oldDay.Add(10 * time.Hour)
	m2 := oldDay.Add(10*time.Hour + time.Minute)

	seedDownsampleTestSamples(t, pool, m1, m2, recentDay.Add(14*time.Hour), todayUTC.Add(time.Minute))

	s := NewSampler(pool, nil, "replica-a")
	if err := s.downsampleCompletedDays(ctx); err != nil {
		t.Fatalf("downsampleCompletedDays: %v", err)
	}
	assertOldDayRollup(t, pool, oldDay, m1, m2)

	var todayCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_metric_daily WHERE day = $1`, todayUTC.Format("2006-01-02")).Scan(&todayCount); err != nil || todayCount != 0 {
		t.Fatalf("today daily rows = %d (err %v), want 0", todayCount, err)
	}

	var node10Bytes int64
	var node10Peak int
	if err := pool.QueryRow(ctx, `SELECT egress_bytes, peak_egress_kbps FROM dashboard_node_egress_daily WHERE day = $1 AND node_id = 10`, oldDay.Format("2006-01-02")).Scan(&node10Bytes, &node10Peak); err != nil {
		t.Fatal(err)
	}
	if node10Bytes != (7000+5000)*7500 || node10Peak != 7000 {
		t.Fatalf("node 10 oldDay rollup = bytes %d peak %d, want %d and 7000", node10Bytes, node10Peak, (7000+5000)*7500)
	}

	// Simulate partial raw deletion before re-rollup: monotonic GREATEST preserves full totals.
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_metric_samples WHERE bucket = $1`, m1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_node_egress_samples WHERE bucket = $1`, m1); err != nil {
		t.Fatal(err)
	}
	if err := s.downsampleCompletedDays(ctx); err != nil {
		t.Fatalf("second downsampleCompletedDays: %v", err)
	}
	var egressBytes int64
	var peakEgress int
	if err := pool.QueryRow(ctx, `SELECT egress_bytes, peak_egress_kbps FROM dashboard_metric_daily WHERE day = $1`, oldDay.Format("2006-01-02")).Scan(&egressBytes, &peakEgress); err != nil {
		t.Fatal(err)
	}
	if egressBytes != 187500000 || peakEgress != 15000 {
		t.Fatalf("after partial raw delete + rerollup: bytes=%d peak=%d, want 187500000 and 15000", egressBytes, peakEgress)
	}

	s.pruneExpired(ctx)
	var oldRawCount, remainingRawCount, dailyCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_metric_samples WHERE bucket < $1`, recentDay).Scan(&oldRawCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_metric_samples`).Scan(&remainingRawCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_metric_daily`).Scan(&dailyCount); err != nil {
		t.Fatal(err)
	}
	if oldRawCount != 0 || remainingRawCount != 2 || dailyCount != 2 {
		t.Fatalf("after prune: oldRaw=%d remainingRaw=%d daily=%d, want 0, 2, 2", oldRawCount, remainingRawCount, dailyCount)
	}
}

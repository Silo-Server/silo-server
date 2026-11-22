// Package dashmetrics records the admin dashboard time series that cannot be
// reconstructed after the fact: how many streams were running (split by play
// method) and how much egress the deployment served. Live sessions leave no
// per-minute trace once they end, and node egress is a rolling average that is
// overwritten on every health check, so both have to be sampled as they happen.
//
// One row per minute per source lands in dashboard_metric_samples, and rows
// older than the retention window below are pruned once an hour:
//
//   - "shared" is the cluster-wide snapshot. Every replica writes it with
//     INSERT ... ON CONFLICT DO NOTHING, so the first writer for a minute wins
//     and the others collapse. Replica snapshots differ only by sub-second
//     timing, which is below the resolution a dashboard chart can show, so this
//     is deliberately cheaper than coordinating with an advisory lock.
//   - "proc:<node_id>" carries the viewer egress served by one API process,
//     measured from the local stream-telemetry registry. stream_nodes only
//     describes external stream nodes, so without these rows a single-server
//     deployment would chart zero egress forever. egress_kbps is the process
//     total; download_egress_kbps is the file-transfer subset of that total
//     (see computeEgressDelta), so the dashboard can split playback from
//     download traffic.
//
// Alongside the shared row, dashboard_node_egress_samples gets one row per
// stream node serving traffic: a breakdown of the node egress the shared row
// already counts, kept in its own table so nothing that sums
// dashboard_metric_samples counts it twice. Only the replica whose shared row
// won the minute writes it, so a minute's split and total come from one
// snapshot. Idle nodes write no row.
//
// Sampling is best-effort: every failure is logged and swallowed. A missed
// minute is a gap in the chart, never a failed request or a dead server.
package dashmetrics

import (
	"context"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

const (
	// component is the slog component key every line from this package carries.
	component = "dashmetrics"

	// sampleInterval matches the minute resolution of the samples table.
	sampleInterval = time.Minute

	// sampleTickTimeout bounds one tick's database work, comfortably under the
	// interval so a wedged pool costs missed minutes, not a stuck sampler.
	sampleTickTimeout = 30 * time.Second

	// retentionDays is how much history the charts can show — a month, so the
	// dashboard's widest range has samples to draw. 1440 minutes a day times 31
	// days is ~45k rows per source, and sources are (1 + replicas), so the table
	// stays in the low hundreds of thousands of rows at most. The per-node table
	// grows the same way with the number of busy nodes. Reads bucket the
	// minutes down before returning them (internal/api/handlers), so a wide
	// window costs the same on the wire as a narrow one.
	retentionDays = 31

	// bytesPerKbpsMinute converts one sampled minute at 1 kbps into bytes:
	// 1000 bits/s * 60 s / 8 bits per byte.
	bytesPerKbpsMinute = 7500

	// downsampleLockKey serializes the hourly completed-day rollup and prune
	// across API replicas so only one replica runs the work per tick.
	downsampleLockKey int64 = 0x53494c4f4441494c // "SILODAIL"
)

// Sampler writes one dashboard_metric_samples row per minute for as long as it
// runs. Its state is owned by the single goroutine Start launches; nothing else
// reads or mutates it.
type Sampler struct {
	pool      *pgxpool.Pool
	telemetry *streamtelemetry.Registry // nil when stream telemetry is disabled
	source    string                    // "proc:<node_id>"
	interval  time.Duration

	// lastBucket is the minute the last tick wrote, so a ticker that fires
	// twice inside one minute does not spend an INSERT that ON CONFLICT would
	// only discard — which would silently drop the egress bytes it carried.
	lastBucket       time.Time
	startupRollupRan bool

	// prevBytes holds the cumulative viewer bytes per telemetry session and
	// transfer at the previous tick; lastEgressAt is when it was taken.
	prevBytes    map[string]int64
	lastEgressAt time.Time

	stopOnce sync.Once
	stop     chan struct{}
}

// NewSampler builds a sampler for this process. telemetry may be nil, in which
// case only the shared cluster row is written. nodeID identifies this process
// among the replicas; it falls back to the hostname when empty.
func NewSampler(pool *pgxpool.Pool, telemetry *streamtelemetry.Registry, nodeID string) *Sampler {
	if nodeID == "" {
		nodeID, _ = os.Hostname()
	}
	if nodeID == "" {
		nodeID = "unknown"
	}
	return &Sampler{
		pool:      pool,
		telemetry: telemetry,
		source:    "proc:" + nodeID,
		interval:  sampleInterval,
		stop:      make(chan struct{}),
	}
}

// Start samples once immediately — which also establishes the egress baseline —
// and then every minute until ctx is canceled or Stop is called.
func (s *Sampler) Start(ctx context.Context) {
	if s == nil || s.pool == nil {
		return
	}
	go func() {
		s.sampleOnce(ctx, time.Now())

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case at := <-ticker.C:
				s.sampleOnce(ctx, at)
			}
		}
	}()
}

// Stop ends the sampling goroutine. It is safe to call more than once.
func (s *Sampler) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
	})
}

// sampleOnce writes this minute's rows and, once an hour, prunes expired ones.
//
// Every tick's database work runs under its own deadline: the sampler is one
// goroutine, and an Exec left on the lifetime context during a wedged pool
// would block it — and with it every later sample and the retention prune —
// for as long as the outage lasts. A bounded tick turns that into a bounded
// gap in the chart instead.
func (s *Sampler) sampleOnce(ctx context.Context, at time.Time) {
	bucket := sampleBucket(at)
	if bucket.Equal(s.lastBucket) {
		return
	}
	s.lastBucket = bucket

	ctx, cancel := context.WithTimeout(ctx, sampleTickTimeout)
	defer cancel()

	s.sampleShared(ctx)
	s.sampleProcessEgress(ctx, at)

	// Downsample completed UTC days into dashboard_metric_daily and
	// dashboard_node_egress_daily on startup and at the top of each hour,
	// strictly before pruning expired minute samples. If the rollup fails, skip
	// pruning on this tick so unsaved days are never lost.
	if !s.startupRollupRan || at.Minute() == 0 {
		s.startupRollupRan = true
		if err := s.downsampleCompletedDays(ctx); err != nil {
			slog.WarnContext(ctx, "failed to downsample daily dashboard metrics; skipping prune", "component", component, "error", err)
			return
		}
		if at.Minute() == 0 {
			s.pruneExpired(ctx)
		}
	}
}

// sampleShared records the cluster-wide stream counts and node egress, plus the
// per-node split of that egress. Counting and inserting happen in one statement
// so no replica can read one minute's state and write it into another's bucket.
// The split is written only when this statement's shared row won the minute,
// so it always adds up to the total it breaks down.
func (s *Sampler) sampleShared(ctx context.Context) {
	_, err := s.pool.Exec(ctx, `
		WITH nodes AS (
			SELECT id, egress_kbps FROM stream_nodes WHERE enabled AND healthy
		), shared AS (
			INSERT INTO dashboard_metric_samples
				(bucket, source, streams_total, streams_direct, streams_remux, streams_transcode, egress_kbps)
			SELECT date_trunc('minute', now()), 'shared',
				(SELECT COUNT(*) FROM playback_sessions_sync),
				(SELECT COUNT(*) FROM playback_sessions_sync WHERE play_method = 'direct'),
				(SELECT COUNT(*) FROM playback_sessions_sync WHERE play_method = 'remux'),
				(SELECT COUNT(*) FROM playback_sessions_sync WHERE play_method = 'transcode'),
				(SELECT COALESCE(SUM(egress_kbps), 0) FROM nodes)
			ON CONFLICT (bucket, source) DO NOTHING
			RETURNING bucket
		)
		INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps)
		SELECT shared.bucket, nodes.id, nodes.egress_kbps
		FROM shared CROSS JOIN nodes
		WHERE nodes.egress_kbps > 0
		ON CONFLICT (bucket, node_id) DO NOTHING
	`)
	if err != nil {
		slog.WarnContext(ctx, "failed to sample shared dashboard metrics", "component", component, "error", err)
	}
}

// sampleProcessEgress records the viewer egress this process served since the
// previous tick. The row is bucketed on the database clock — the same clock the
// shared row and the read window use — so a skewed host cannot land streams and
// their egress in adjacent minutes, or write a "future" row the dashboard's
// server-anchored grid would drop. Two ticks that map onto one DB minute merge
// by GREATEST, which keeps the peak (the read side is peak-preserving anyway)
// instead of silently discarding the second delta; taking each column's max
// independently preserves download <= total because it holds per row.
func (s *Sampler) sampleProcessEgress(ctx context.Context, at time.Time) {
	if s.telemetry == nil {
		return
	}

	// Sweep rather than Snapshot: Snapshot reports byte totals as of the last
	// telemetry sweep, and a sweep interval configured above one minute would
	// make ticks in between read zero growth and the next one attribute
	// several minutes of bytes to a single minute — a spike that never
	// happened. Sweep collects the live counters now.
	delta, next := computeEgressDelta(s.prevBytes, s.telemetry.Sweep())
	previous, previousAt := s.prevBytes, s.lastEgressAt
	s.prevBytes, s.lastEgressAt = next, at

	// The very first snapshot carries every byte served since the process
	// started. Charting that as one minute of egress would draw a spike that
	// never happened, so the first tick only establishes the baseline.
	if previous == nil || previousAt.IsZero() {
		return
	}

	// Zero minutes are written too: an idle server should draw a line along the
	// baseline, not a gap that reads as "no data". egress_kbps stays the total
	// this process served (its pre-split meaning), while download_egress_kbps
	// carries the file-transfer subset. The subset is clamped under the total
	// after rounding so a reader deriving playback as total - download can
	// never see a negative minute from two independent roundings.
	elapsed := at.Sub(previousAt)
	totalKbps := egressKbps(delta.Total, elapsed)
	downloadKbps := min(egressKbps(delta.Download, elapsed), totalKbps)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples (bucket, source, egress_kbps, download_egress_kbps)
		VALUES (date_trunc('minute', now()), $1, $2, $3)
		ON CONFLICT (bucket, source) DO UPDATE
		SET egress_kbps = GREATEST(dashboard_metric_samples.egress_kbps, EXCLUDED.egress_kbps),
		    download_egress_kbps = GREATEST(dashboard_metric_samples.download_egress_kbps, EXCLUDED.download_egress_kbps)
	`, s.source, totalKbps, downloadKbps)
	if err != nil {
		slog.WarnContext(ctx, "failed to sample process egress", "component", component, "source", s.source, "error", err)
	}
}

// downsampleCompletedDays rolls up every completed UTC calendar day currently
// present in dashboard_metric_samples and dashboard_node_egress_samples into
// dashboard_metric_daily and dashboard_node_egress_daily.
//
// Only complete UTC days strictly before today UTC are rolled up, and
// pruneExpired removes whole UTC days at a time, so every day present in the
// raw sample tables has all of its recorded minutes intact. A non-blocking
// transaction-scoped advisory lock avoids redundant work across replicas.
const downsampleMetricDailySQL = `
	WITH cutoff AS (
		SELECT (date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS today_utc
	), minutes AS (
		SELECT
			(bucket AT TIME ZONE 'UTC')::date AS day,
			bucket,
			MAX(streams_total)     FILTER (WHERE source = 'shared') AS streams_total,
			MAX(streams_direct)    FILTER (WHERE source = 'shared') AS streams_direct,
			MAX(streams_remux)     FILTER (WHERE source = 'shared') AS streams_remux,
			MAX(streams_transcode) FILTER (WHERE source = 'shared') AS streams_transcode,
			COALESCE(SUM(egress_kbps), 0)::bigint AS egress_kbps,
			COALESCE(SUM(download_egress_kbps), 0)::bigint AS download_egress_kbps,
			COALESCE(SUM(egress_kbps) FILTER (WHERE source LIKE 'proc:%'), 0)::bigint AS api_egress_kbps
		FROM dashboard_metric_samples, cutoff
		WHERE bucket < cutoff.today_utc
		GROUP BY 1, 2
	), stream_peaks AS (
		SELECT DISTINCT ON (day)
			day,
			COALESCE(streams_direct, 0)::integer    AS peak_streams_direct,
			COALESCE(streams_remux, 0)::integer     AS peak_streams_remux,
			COALESCE(streams_transcode, 0)::integer AS peak_streams_transcode,
			bucket AS peak_streams_at
		FROM minutes
		WHERE streams_total IS NOT NULL
		ORDER BY day, streams_total DESC, bucket ASC
	), egress_peaks AS (
		SELECT DISTINCT ON (day)
			day,
			bucket AS peak_egress_at
		FROM minutes
		ORDER BY day, egress_kbps DESC, bucket ASC
	), agg AS (
		SELECT
			day,
			COALESCE(MAX(streams_total), 0)::integer     AS peak_streams,
			COALESCE(MAX(streams_direct), 0)::integer    AS peak_direct,
			COALESCE(MAX(streams_remux), 0)::integer     AS peak_remux,
			COALESCE(MAX(streams_transcode), 0)::integer AS peak_transcode,
			COALESCE(MAX(egress_kbps), 0)::bigint        AS peak_egress_kbps,
			COALESCE(MAX(download_egress_kbps), 0)::bigint AS peak_download_kbps,
			(COALESCE(SUM(egress_kbps), 0) * $1)::bigint          AS egress_bytes,
			(COALESCE(SUM(download_egress_kbps), 0) * $1)::bigint AS download_egress_bytes,
			(COALESCE(SUM(api_egress_kbps), 0) * $1)::bigint      AS api_egress_bytes,
			COALESCE(MAX(api_egress_kbps), 0)::bigint             AS api_peak_egress_kbps
		FROM minutes
		GROUP BY day
	)
	INSERT INTO dashboard_metric_daily (
		day, peak_streams, peak_direct, peak_remux, peak_transcode,
		peak_streams_direct, peak_streams_remux, peak_streams_transcode,
		peak_egress_kbps, peak_download_kbps,
		egress_bytes, download_egress_bytes,
		api_egress_bytes, api_peak_egress_kbps,
		peak_streams_at, peak_egress_at, updated_at
	)
	SELECT
		agg.day, agg.peak_streams, agg.peak_direct, agg.peak_remux, agg.peak_transcode,
		COALESCE(sp.peak_streams_direct, 0),
		COALESCE(sp.peak_streams_remux, 0),
		COALESCE(sp.peak_streams_transcode, 0),
		agg.peak_egress_kbps, agg.peak_download_kbps,
		agg.egress_bytes, agg.download_egress_bytes,
		agg.api_egress_bytes, agg.api_peak_egress_kbps,
		sp.peak_streams_at, ep.peak_egress_at, now()
	FROM agg
	LEFT JOIN stream_peaks sp ON sp.day = agg.day
	LEFT JOIN egress_peaks ep ON ep.day = agg.day
	ON CONFLICT (day) DO UPDATE SET
		peak_streams           = GREATEST(dashboard_metric_daily.peak_streams, EXCLUDED.peak_streams),
		peak_direct            = GREATEST(dashboard_metric_daily.peak_direct, EXCLUDED.peak_direct),
		peak_remux             = GREATEST(dashboard_metric_daily.peak_remux, EXCLUDED.peak_remux),
		peak_transcode         = GREATEST(dashboard_metric_daily.peak_transcode, EXCLUDED.peak_transcode),
		peak_streams_direct    = CASE WHEN EXCLUDED.peak_streams >= dashboard_metric_daily.peak_streams THEN EXCLUDED.peak_streams_direct ELSE dashboard_metric_daily.peak_streams_direct END,
		peak_streams_remux     = CASE WHEN EXCLUDED.peak_streams >= dashboard_metric_daily.peak_streams THEN EXCLUDED.peak_streams_remux ELSE dashboard_metric_daily.peak_streams_remux END,
		peak_streams_transcode = CASE WHEN EXCLUDED.peak_streams >= dashboard_metric_daily.peak_streams THEN EXCLUDED.peak_streams_transcode ELSE dashboard_metric_daily.peak_streams_transcode END,
		peak_egress_kbps       = GREATEST(dashboard_metric_daily.peak_egress_kbps, EXCLUDED.peak_egress_kbps),
		peak_download_kbps     = GREATEST(dashboard_metric_daily.peak_download_kbps, EXCLUDED.peak_download_kbps),
		egress_bytes           = GREATEST(dashboard_metric_daily.egress_bytes, EXCLUDED.egress_bytes),
		download_egress_bytes  = GREATEST(dashboard_metric_daily.download_egress_bytes, EXCLUDED.download_egress_bytes),
		api_egress_bytes       = GREATEST(dashboard_metric_daily.api_egress_bytes, EXCLUDED.api_egress_bytes),
		api_peak_egress_kbps   = GREATEST(dashboard_metric_daily.api_peak_egress_kbps, EXCLUDED.api_peak_egress_kbps),
		peak_streams_at        = CASE WHEN EXCLUDED.peak_streams >= dashboard_metric_daily.peak_streams THEN COALESCE(EXCLUDED.peak_streams_at, dashboard_metric_daily.peak_streams_at) ELSE dashboard_metric_daily.peak_streams_at END,
		peak_egress_at         = CASE WHEN EXCLUDED.peak_egress_kbps >= dashboard_metric_daily.peak_egress_kbps THEN COALESCE(EXCLUDED.peak_egress_at, dashboard_metric_daily.peak_egress_at) ELSE dashboard_metric_daily.peak_egress_at END,
		updated_at             = now()
`

const downsampleNodeEgressDailySQL = `
	WITH cutoff AS (
		SELECT (date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS today_utc
	)
	INSERT INTO dashboard_node_egress_daily (day, node_id, egress_bytes, peak_egress_kbps, updated_at)
	SELECT
		(bucket AT TIME ZONE 'UTC')::date AS day,
		node_id,
		(COALESCE(SUM(egress_kbps), 0) * $1)::bigint AS egress_bytes,
		COALESCE(MAX(egress_kbps), 0)::bigint        AS peak_egress_kbps,
		now()
	FROM dashboard_node_egress_samples, cutoff
	WHERE bucket < cutoff.today_utc
	GROUP BY 1, 2
	ON CONFLICT (day, node_id) DO UPDATE SET
		egress_bytes     = GREATEST(dashboard_node_egress_daily.egress_bytes, EXCLUDED.egress_bytes),
		peak_egress_kbps = GREATEST(dashboard_node_egress_daily.peak_egress_kbps, EXCLUDED.peak_egress_kbps),
		updated_at       = now()
`

func (s *Sampler) downsampleCompletedDays(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, downsampleLockKey).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	if err := execDownsampleStatements(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func execDownsampleStatements(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, downsampleMetricDailySQL, int64(bytesPerKbpsMinute)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, downsampleNodeEgressDailySQL, int64(bytesPerKbpsMinute)); err != nil {
		return err
	}
	return nil
}

// pruneExpired drops whole UTC days older than the retention window so raw
// samples never lose half a day before downsampleCompletedDays rolls it up.
func (s *Sampler) pruneExpired(ctx context.Context) {
	for _, table := range []string{"dashboard_metric_samples", "dashboard_node_egress_samples"} {
		_, err := s.pool.Exec(ctx, `
			DELETE FROM `+table+`
			WHERE bucket < ((date_trunc('day', now() AT TIME ZONE 'UTC') - make_interval(days => $1)) AT TIME ZONE 'UTC')
		`, retentionDays)
		if err != nil {
			slog.WarnContext(ctx, "failed to prune dashboard metric samples", "component", component, "table", table, "error", err)
		}
	}
}

// sampleBucket truncates a sample time to the minute it belongs to, in UTC.
func sampleBucket(at time.Time) time.Time {
	return at.UTC().Truncate(time.Minute)
}

// egressKbps converts a byte delta over an elapsed period into kilobits per
// second. A non-positive delta or elapsed period is reported as zero rather
// than as a negative rate.
func egressKbps(deltaBytes int64, elapsed time.Duration) int64 {
	if deltaBytes <= 0 || elapsed <= 0 {
		return 0
	}
	return int64(math.Round(float64(deltaBytes) * 8 / 1000 / elapsed.Seconds()))
}

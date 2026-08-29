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
	// stays in the low hundreds of thousands of rows at most. Reads bucket the
	// minutes down before returning them (internal/api/handlers), so a wide
	// window costs the same on the wire as a narrow one.
	retentionDays = 31
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
	lastBucket time.Time

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

	// Retention runs in-band rather than as its own timer: the table is tiny
	// and one DELETE an hour costs less than another goroutine.
	if at.Minute() == 0 {
		s.pruneExpired(ctx)
	}
}

// sampleShared records the cluster-wide stream counts and node egress. Counting
// and inserting happen in one statement so no replica can read one minute's
// state and write it into another's bucket.
func (s *Sampler) sampleShared(ctx context.Context) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples
			(bucket, source, streams_total, streams_direct, streams_remux, streams_transcode, egress_kbps)
		SELECT date_trunc('minute', now()), 'shared',
			(SELECT COUNT(*) FROM playback_sessions_sync),
			(SELECT COUNT(*) FROM playback_sessions_sync WHERE play_method = 'direct'),
			(SELECT COUNT(*) FROM playback_sessions_sync WHERE play_method = 'remux'),
			(SELECT COUNT(*) FROM playback_sessions_sync WHERE play_method = 'transcode'),
			(SELECT COALESCE(SUM(egress_kbps), 0) FROM stream_nodes WHERE enabled AND healthy)
		ON CONFLICT (bucket, source) DO NOTHING
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

// pruneExpired drops samples older than the retention window.
func (s *Sampler) pruneExpired(ctx context.Context) {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM dashboard_metric_samples
		WHERE bucket < now() - make_interval(days => $1)
	`, retentionDays)
	if err != nil {
		slog.WarnContext(ctx, "failed to prune dashboard metric samples", "component", component, "error", err)
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

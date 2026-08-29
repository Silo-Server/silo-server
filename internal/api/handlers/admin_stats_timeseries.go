package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

const (
	adminTimeseriesCacheTTL    = 30 * time.Second
	adminTimeseriesCachePrefix = "hours="

	adminTimeseriesDefaultHours = 24
	adminTimeseriesMinHours     = 1
	// adminTimeseriesMaxHours is the sampler's retention window
	// (internal/dashmetrics), so the widest window a client can ask for is the
	// widest one the table can answer.
	adminTimeseriesMaxHours = 744

	// adminTimeseriesResolutionSeconds is the sampler's minute cadence
	// (internal/dashmetrics) and therefore the finest bucket a read can return.
	// The response reports the bucket it actually used, so the client sizes gaps
	// from the data instead of hardcoding a second copy of this number.
	adminTimeseriesResolutionSeconds = 60

	// adminTimeseriesMaxPoints is the point budget a response aims to stay
	// under. A dashboard widget is a few hundred CSS pixels wide, so a month of
	// minutes would spend megabytes drawing sub-pixel detail nobody can see.
	adminTimeseriesMaxPoints = 750
)

// timeseriesBucketSeconds picks the display bucket for a window, keeping every
// response under adminTimeseriesMaxPoints points.
//
// The thresholds are fixed rather than derived from the budget so that two
// clients asking for neighboring windows land on the same bucket — a bucket
// that drifted with the window would make consecutive reads incomparable, and
// the cache key (which is the window) would no longer imply the resolution.
func timeseriesBucketSeconds(hours int) int {
	switch {
	case hours <= 2:
		return adminTimeseriesResolutionSeconds // 120 points at most
	case hours <= 48:
		return 300 // 5 minutes, 576 points at most
	case hours <= 336:
		return 1800 // 30 minutes, 672 points at most
	default:
		return 7200 // 2 hours, 372 points at most for a 31-day window
	}
}

// AdminTimeseriesPoint is one display bucket of sampled dashboard metrics —
// one sampled minute for short windows, several minutes collapsed for wide
// ones. Stream counts come from the cluster-wide "shared" sample; egress sums
// every source for a minute, so node egress and the egress each API process
// served are both included.
//
// A bucket that spans several minutes reports the peak minute of each column,
// never an average: concurrency and egress are read to answer "how bad did it
// get", and a mean would hide exactly that.
//
// EgressKbps keeps its pre-split meaning — every source's total viewer egress —
// so charts drawn from it alone stay truthful. DownloadEgressKbps is the
// additive file-transfer subset of that total (offline/direct downloads, ebook
// and ABS file fetches, measured by the API processes; node egress cannot be
// split and therefore counts entirely outside the subset). A client shows the
// split as download versus egress − download; the sampler clamps the subset
// under the total per minute, and both columns take their per-bucket MAX over
// the same minutes, so the difference is never negative. Samples written before
// the split report a zero subset.
type AdminTimeseriesPoint struct {
	T                  time.Time `json:"t"`
	Streams            int64     `json:"streams"`
	Direct             int64     `json:"direct"`
	Remux              int64     `json:"remux"`
	Transcode          int64     `json:"transcode"`
	EgressKbps         int64     `json:"egress_kbps"`
	DownloadEgressKbps int64     `json:"download_egress_kbps"`
}

// AdminTimeseries is the GET /admin/stats/timeseries body.
//
// Buckets the sampler missed — a restart, a paused process, a server that was
// simply off — are absent from Points rather than zero-filled: a gap and an
// idle bucket are different facts and the chart draws them differently.
// OldestSampleAt is nil until the sampler has written anything, which is how
// the dashboard knows to say it is still collecting data.
//
// ResolutionSeconds is the bucket this window was aggregated into, not a
// constant: it widens with the requested window (timeseriesBucketSeconds).
type AdminTimeseries struct {
	ResolutionSeconds int                    `json:"resolution_seconds"`
	From              time.Time              `json:"from"`
	To                time.Time              `json:"to"`
	OldestSampleAt    *time.Time             `json:"oldest_sample_at"`
	Points            []AdminTimeseriesPoint `json:"points"`
}

// AdminTimeseriesSource returns cached or freshly queried samples for a window
// of hours.
type AdminTimeseriesSource interface {
	Get(ctx context.Context, hours int) (*AdminTimeseries, error)
	Invalidate()
}

// AdminTimeseriesProvider serves sampled dashboard metrics with a short
// in-process TTL and optional cross-node invalidation via the shared event bus,
// mirroring AdminStatsProvider.
//
// The cached payload is a pointer because cache.TTLCache requires a comparable
// value type and this struct carries a slice.
type AdminTimeseriesProvider struct {
	pool  *pgxpool.Pool
	cache *cache.TTLCache[*AdminTimeseries]
	ttl   time.Duration
}

var _ AdminTimeseriesSource = (*AdminTimeseriesProvider)(nil)

// NewAdminTimeseriesProvider creates a cached provider and subscribes it to the
// playback/admin invalidation channels when an event bus is configured.
func NewAdminTimeseriesProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminTimeseriesProvider, error) {
	provider := &AdminTimeseriesProvider{
		pool:  pool,
		cache: cache.NewTTLCache[*AdminTimeseries](),
		ttl:   adminTimeseriesCacheTTL,
	}

	if bus == nil || ctx == nil {
		return provider, nil
	}

	handler := func(cache.Event) {
		provider.Invalidate()
	}
	for _, channel := range []string{cache.ChannelAdmin, cache.ChannelPlayback} {
		if err := bus.Subscribe(ctx, channel, handler); err != nil {
			provider.Close()
			return nil, fmt.Errorf("subscribing admin timeseries provider to %s: %w", channel, err)
		}
	}

	return provider, nil
}

// Get returns the cached window when available, otherwise queries Postgres.
//
// The window is clamped before the key is built, so two requests the server
// would answer identically share one entry. The bucket size is a function of
// the clamped window, so the key needs nothing else to stay honest.
func (p *AdminTimeseriesProvider) Get(ctx context.Context, hours int) (*AdminTimeseries, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("admin timeseries provider is not configured")
	}
	hours = clampQueryInt(hours, adminTimeseriesMinHours, adminTimeseriesMaxHours)
	key := adminTimeseriesCachePrefix + strconv.Itoa(hours)
	if series, ok := p.cache.Get(key); ok {
		return series, nil
	}

	series, err := queryAdminTimeseries(ctx, p.pool, hours)
	if err != nil {
		return nil, err
	}
	p.cache.Set(key, series, p.ttl)
	return series, nil
}

// Invalidate drops every cached window.
func (p *AdminTimeseriesProvider) Invalidate() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.InvalidatePrefix(adminTimeseriesCachePrefix)
}

// Close stops the background TTL sweeper.
func (p *AdminTimeseriesProvider) Close() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.Close()
}

// HandleGetTimeseries handles GET /admin/stats/timeseries.
func (h *AdminHandler) HandleGetTimeseries(w http.ResponseWriter, r *http.Request) {
	hours, err := parseClampedIntQuery(
		r, "hours",
		adminTimeseriesDefaultHours,
		adminTimeseriesMinHours,
		adminTimeseriesMaxHours,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var series *AdminTimeseries
	switch {
	case h.TimeseriesSource != nil:
		if isTruthyQuery(r.URL.Query().Get("refresh")) {
			h.TimeseriesSource.Invalidate()
		}
		series, err = h.TimeseriesSource.Get(r.Context(), hours)
	case h.pool != nil:
		series, err = queryAdminTimeseries(r.Context(), h.pool, hours)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get dashboard timeseries")
		return
	}

	writeJSON(w, http.StatusOK, series)
}

// timeseriesRow is one grouped display bucket as it comes back from Postgres.
// The stream counts are nullable: a bucket can hold process egress rows without
// a shared row when a replica sampled egress but every attempt at the shared
// row lost the race or failed.
type timeseriesRow struct {
	Bucket             time.Time
	Streams            *int64
	Direct             *int64
	Remux              *int64
	Transcode          *int64
	EgressKbps         int64
	DownloadEgressKbps int64
}

// assembleTimeseries turns grouped rows into response points, reading a missing
// shared sample as zero streams rather than dropping the bucket — the egress it
// carries is still real.
func assembleTimeseries(rows []timeseriesRow) []AdminTimeseriesPoint {
	points := make([]AdminTimeseriesPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, AdminTimeseriesPoint{
			T:                  row.Bucket.UTC(),
			Streams:            nullableCount(row.Streams),
			Direct:             nullableCount(row.Direct),
			Remux:              nullableCount(row.Remux),
			Transcode:          nullableCount(row.Transcode),
			EgressKbps:         row.EgressKbps,
			DownloadEgressKbps: row.DownloadEgressKbps,
		})
	}
	return points
}

func nullableCount(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func queryAdminTimeseries(ctx context.Context, pool *pgxpool.Pool, hours int) (*AdminTimeseries, error) {
	if pool == nil {
		return nil, fmt.Errorf("database not configured")
	}

	bucketSeconds := timeseriesBucketSeconds(hours)

	// Two passes, in one statement. The inner one collapses the sources of a
	// single minute: MAX over the shared source rather than SUM, because
	// replicas may each have written the same minute's cluster-wide counts and
	// those describe one cluster, not several — while egress is the opposite,
	// since every source served different bytes.
	//
	// The outer one collapses those minutes into the display bucket by taking
	// the peak minute of each column. Averaging would erase the spikes these
	// charts exist to show; a bucket therefore answers "the worst minute in
	// here", and its columns may come from different minutes.
	rows, err := pool.Query(ctx, `
		WITH minutes AS (
			SELECT bucket,
			       MAX(streams_total)     FILTER (WHERE source = 'shared') AS streams_total,
			       MAX(streams_direct)    FILTER (WHERE source = 'shared') AS streams_direct,
			       MAX(streams_remux)     FILTER (WHERE source = 'shared') AS streams_remux,
			       MAX(streams_transcode) FILTER (WHERE source = 'shared') AS streams_transcode,
			       COALESCE(SUM(egress_kbps), 0)::bigint AS egress_kbps,
			       COALESCE(SUM(download_egress_kbps), 0)::bigint AS download_egress_kbps
			FROM dashboard_metric_samples
			WHERE bucket >= now() - make_interval(hours => $1)
			GROUP BY bucket
		)
		SELECT date_bin(make_interval(secs => $2), bucket, 'epoch') AS display_bucket,
		       MAX(streams_total),
		       MAX(streams_direct),
		       MAX(streams_remux),
		       MAX(streams_transcode),
		       MAX(egress_kbps)::bigint,
		       MAX(download_egress_kbps)::bigint
		FROM minutes
		GROUP BY display_bucket
		ORDER BY display_bucket
	`, hours, float64(bucketSeconds))
	if err != nil {
		return nil, fmt.Errorf("querying dashboard metric samples: %w", err)
	}
	defer rows.Close()

	grouped := make([]timeseriesRow, 0, hours*3600/bucketSeconds+1)
	for rows.Next() {
		var row timeseriesRow
		if err := rows.Scan(
			&row.Bucket,
			&row.Streams,
			&row.Direct,
			&row.Remux,
			&row.Transcode,
			&row.EgressKbps,
			&row.DownloadEgressKbps,
		); err != nil {
			return nil, fmt.Errorf("scanning dashboard metric sample: %w", err)
		}
		grouped = append(grouped, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dashboard metric samples: %w", err)
	}

	series := &AdminTimeseries{
		ResolutionSeconds: bucketSeconds,
		Points:            assembleTimeseries(grouped),
	}

	// The window bounds come from the same clock the samples were bucketed
	// with, so a client comparing them against point timestamps never sees the
	// API server's clock skew.
	var oldest *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT MIN(bucket),
		       now() - make_interval(hours => $1),
		       now()
		FROM dashboard_metric_samples
	`, hours).Scan(&oldest, &series.From, &series.To); err != nil {
		return nil, fmt.Errorf("querying dashboard metric sample window: %w", err)
	}
	if oldest != nil {
		utc := oldest.UTC()
		series.OldestSampleAt = &utc
	}
	series.From = series.From.UTC()
	series.To = series.To.UTC()

	return series, nil
}

package handlers

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

	// bytesPerKbpsMinute converts one sampled minute at 1 kbps into bytes:
	// 1000 bits/s × 60 s ÷ 8 bits per byte.
	bytesPerKbpsMinute = 1000 * adminTimeseriesResolutionSeconds / 8
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
	// EgressBySource splits EgressKbps by AdminTimeseriesEgressSource key, taken
	// from the bucket's peak-egress minute so the parts belong to the same
	// minute as the total. Egress no source accounts for, such as minutes
	// sampled before per-node rows existed, is the remainder. /api/v1 is frozen,
	// so only v2 emits it.
	EgressBySource map[string]int64 `json:"-"`
}

// AdminTimeseriesEgressSource names one key of AdminTimeseriesPoint.EgressBySource:
// a stream node ("node:<id>", from dashboard_node_egress_samples) or every API
// process together ("api", the "proc:" rows).
type AdminTimeseriesEgressSource struct {
	Key    string
	Kind   string // EgressSourceKindNode or EgressSourceKindAPI
	NodeID int    // zero for the API source
	Name   string
	// EgressBytes is what the source served over the whole window, integrated
	// from every sampled minute like AdminTimeseries.EgressBytes.
	EgressBytes int64
}

// Kinds of AdminTimeseriesEgressSource.
const (
	EgressSourceKindAPI  = "api"
	EgressSourceKindNode = "node"
)

const (
	egressSourceAPIKey  = "api"
	egressSourceAPIName = "API server"
)

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
//
// EgressBytes and DownloadEgressBytes are the data transferred over the whole
// window, integrated from every sampled minute rather than from Points: the
// points are peak-per-bucket, so summing them would count a one-minute spike
// as if it lasted the whole bucket. Minutes the sampler missed contribute
// nothing, and node egress is a rolling average, so the totals are a close
// estimate rather than exact byte accounting. /api/v1 is frozen, so only v2
// emits them (json:"-").
type AdminTimeseries struct {
	ResolutionSeconds   int        `json:"resolution_seconds"`
	From                time.Time  `json:"from"`
	To                  time.Time  `json:"to"`
	OldestSampleAt      *time.Time `json:"oldest_sample_at"`
	EgressBytes         int64      `json:"-"`
	DownloadEgressBytes int64      `json:"-"`
	// EgressSources lists every source with samples in the window, which
	// covers every key a point's EgressBySource uses, the API source first and
	// nodes by name. v2 only.
	EgressSources []AdminTimeseriesEgressSource `json:"-"`
	Points        []AdminTimeseriesPoint        `json:"points"`
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

	series, err := h.ReadAdminTimeseries(r.Context(), hours, isTruthyQuery(r.URL.Query().Get("refresh")))
	if errors.Is(err, ErrAdminDashboardUnavailable) {
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
	// The Sum columns add up every sampled minute's rate in the bucket, which
	// the transfer totals integrate; the Kbps columns above are the peak.
	EgressKbpsSum         int64
	DownloadEgressKbpsSum int64
}

// sumTransferredBytes integrates the per-minute rates of every bucket into
// total bytes transferred over the window.
func sumTransferredBytes(rows []timeseriesRow) (egressBytes, downloadBytes int64) {
	for _, row := range rows {
		egressBytes += row.EgressKbpsSum * bytesPerKbpsMinute
		downloadBytes += row.DownloadEgressKbpsSum * bytesPerKbpsMinute
	}
	return egressBytes, downloadBytes
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

	// Every read below shares one snapshot, so a sample committed between them
	// cannot give the newest point a split larger than its total.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("beginning dashboard timeseries read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Two passes, in one statement. The inner one collapses the sources of a
	// single minute: MAX over the shared source rather than SUM, because
	// replicas may each have written the same minute's cluster-wide counts and
	// those describe one cluster, not several — while egress is the opposite,
	// since every source served different bytes.
	//
	// The outer one collapses those minutes into the display bucket by taking
	// the peak minute of each column. Averaging would erase the spikes these
	// charts exist to show; a bucket therefore answers "the worst minute in
	// here", and its columns may come from different minutes. The trailing
	// SUM columns are the exception: they feed the window's transfer totals,
	// which need every minute's rate, not the peak.
	rows, err := tx.Query(ctx, `
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
		       MAX(download_egress_kbps)::bigint,
		       SUM(egress_kbps)::bigint,
		       SUM(download_egress_kbps)::bigint
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
			&row.EgressKbpsSum,
			&row.DownloadEgressKbpsSum,
		); err != nil {
			return nil, fmt.Errorf("scanning dashboard metric sample: %w", err)
		}
		grouped = append(grouped, row)
	}
	// The transaction's connection runs one query at a time, so free it for
	// the reads below rather than at return.
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dashboard metric samples: %w", err)
	}

	series := &AdminTimeseries{
		ResolutionSeconds: bucketSeconds,
		Points:            assembleTimeseries(grouped),
	}
	series.EgressBytes, series.DownloadEgressBytes = sumTransferredBytes(grouped)
	if err := queryEgressBySource(ctx, tx, hours, bucketSeconds, series); err != nil {
		return nil, err
	}

	// The window bounds come from the same clock the samples were bucketed
	// with, so a client comparing them against point timestamps never sees the
	// API server's clock skew.
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `
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

// egressPartsSQL is every source's egress per sampled minute inside the
// window ($1 hours): all API processes folded into "api", and each node's row.
const egressPartsSQL = `
	SELECT bucket, 'api' AS source_key, egress_kbps
	FROM dashboard_metric_samples
	WHERE source LIKE 'proc:%' AND bucket >= now() - make_interval(hours => $1)
	UNION ALL
	SELECT bucket, 'node:' || node_id, egress_kbps
	FROM dashboard_node_egress_samples
	WHERE bucket >= now() - make_interval(hours => $1)
`

// queryEgressBySource fills each point's EgressBySource and the series'
// EgressSources. For every display bucket it picks the minute whose total
// egress peaked and reads that minute's node rows and API process rows; taking
// each source's own peak instead would add up to more than the point's egress
// whenever sources peak in different minutes. The per-minute total here must
// stay the same SUM queryAdminTimeseries peaks over, or the split and the
// point would describe different minutes.
func queryEgressBySource(ctx context.Context, tx pgx.Tx, hours, bucketSeconds int, series *AdminTimeseries) error {
	rows, err := tx.Query(ctx, `
		WITH minutes AS (
			SELECT bucket, SUM(egress_kbps) AS egress_kbps
			FROM dashboard_metric_samples
			WHERE bucket >= now() - make_interval(hours => $1)
			GROUP BY bucket
		), peaks AS (
			SELECT DISTINCT ON (display_bucket) display_bucket, bucket
			FROM (
				SELECT date_bin(make_interval(secs => $2), bucket, 'epoch') AS display_bucket, bucket, egress_kbps
				FROM minutes
			) binned
			ORDER BY display_bucket, egress_kbps DESC, bucket
		), parts AS (`+egressPartsSQL+`)
		SELECT peaks.display_bucket, parts.source_key, SUM(parts.egress_kbps)::bigint
		FROM peaks
		JOIN parts ON parts.bucket = peaks.bucket
		GROUP BY 1, 2
	`, hours, float64(bucketSeconds))
	if err != nil {
		return fmt.Errorf("querying dashboard egress by source: %w", err)
	}

	byBucket := make(map[time.Time]map[string]int64)
	for rows.Next() {
		var bucket time.Time
		var key string
		var kbps int64
		if err := rows.Scan(&bucket, &key, &kbps); err != nil {
			rows.Close()
			return fmt.Errorf("scanning dashboard egress by source: %w", err)
		}
		bucket = bucket.UTC()
		if byBucket[bucket] == nil {
			byBucket[bucket] = make(map[string]int64)
		}
		byBucket[bucket][key] = kbps
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating dashboard egress by source: %w", err)
	}

	for i := range series.Points {
		bySource := byBucket[series.Points[i].T]
		if bySource == nil {
			bySource = map[string]int64{}
		}
		series.Points[i].EgressBySource = bySource
	}

	// Window totals cover every sampled minute, not only the peak minutes the
	// points show, so a source that is busy all window but never at a bucket's
	// peak is still listed and ranked by what it served.
	rows, err = tx.Query(ctx, `
		SELECT source_key, SUM(egress_kbps)::bigint FROM (`+egressPartsSQL+`) parts GROUP BY 1
	`, hours)
	if err != nil {
		return fmt.Errorf("querying dashboard egress source totals: %w", err)
	}
	totals := make(map[string]int64)
	for rows.Next() {
		var key string
		var kbpsMinutes int64
		if err := rows.Scan(&key, &kbpsMinutes); err != nil {
			rows.Close()
			return fmt.Errorf("scanning dashboard egress source total: %w", err)
		}
		totals[key] = kbpsMinutes * bytesPerKbpsMinute
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating dashboard egress source totals: %w", err)
	}

	sources, err := describeEgressSources(ctx, tx, totals)
	if err != nil {
		return err
	}
	series.EgressSources = sources
	return nil
}

// describeEgressSources names the sources that served egress in the window,
// keyed with their window totals in bytes. A node row
// outlives the node it describes, so a deleted node gets a placeholder name
// rather than disappearing from the chart.
func describeEgressSources(ctx context.Context, tx pgx.Tx, totals map[string]int64) ([]AdminTimeseriesEgressSource, error) {
	sources := make([]AdminTimeseriesEgressSource, 0, len(totals))
	var nodeIDs []int
	for key := range totals {
		if key == egressSourceAPIKey {
			sources = append(sources, AdminTimeseriesEgressSource{Key: key, Kind: EgressSourceKindAPI, Name: egressSourceAPIName, EgressBytes: totals[key]})
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(key, "node:"))
		if err != nil {
			return nil, fmt.Errorf("parsing dashboard egress source %q: %w", key, err)
		}
		nodeIDs = append(nodeIDs, id)
	}

	names := make(map[int]string, len(nodeIDs))
	if len(nodeIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT id, name FROM stream_nodes WHERE id = ANY($1)`, nodeIDs)
		if err != nil {
			return nil, fmt.Errorf("querying dashboard egress source names: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, fmt.Errorf("scanning dashboard egress source name: %w", err)
			}
			names[id] = name
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterating dashboard egress source names: %w", err)
		}
	}
	for _, id := range nodeIDs {
		name, ok := names[id]
		if !ok {
			name = fmt.Sprintf("Removed node %d", id)
		}
		key := fmt.Sprintf("node:%d", id)
		sources = append(sources, AdminTimeseriesEgressSource{Key: key, Kind: EgressSourceKindNode, NodeID: id, Name: name, EgressBytes: totals[key]})
	}

	slices.SortFunc(sources, func(a, b AdminTimeseriesEgressSource) int {
		if (a.Kind == EgressSourceKindAPI) != (b.Kind == EgressSourceKindAPI) {
			if a.Kind == EgressSourceKindAPI {
				return -1
			}
			return 1
		}
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Key, b.Key))
	})
	return sources, nil
}

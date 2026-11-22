package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

const (
	adminInsightsCacheTTL       = 60 * time.Second
	adminInsightsCacheKeyPrefix = "insights:"

	InsightsRange7d     = "7d"
	InsightsRange30d    = "30d"
	InsightsRange90d    = "90d"
	InsightsRange1y     = "1y"
	InsightsRangeAll    = "all"
	InsightsRangeCustom = "custom"

	InsightsBucketDay   = "day"
	InsightsBucketWeek  = "week"
	InsightsBucketMonth = "month"

	insightsCustomMaxSpanDays = 3660
	insightsDefaultPeopleLim  = 50
	insightsMaxPeopleLim      = 100
	insightsDefaultMinPlays   = 5
	insightsMaxMinPlays       = 1000
)

// ErrInvalidInsightsQuery marks client-supplied filter or range validation errors.
var ErrInvalidInsightsQuery = errors.New("invalid insights query")

// AdminInsightsWindowQuery holds the shared Insights filter bar parameters.
type AdminInsightsWindowQuery struct {
	Range     string
	FromDate  string // YYYY-MM-DD (local date in TZ), used when Range == "custom"
	ToDate    string // YYYY-MM-DD (local date in TZ, inclusive), used when Range == "custom"
	TZ        string // IANA timezone name; empty defaults to UTC
	LibraryID int
	UserID    int
}

// AdminInsightsPeopleQuery extends the shared window query with People table controls.
type AdminInsightsPeopleQuery struct {
	AdminInsightsWindowQuery
	Search string
	Sort   string // watch_time | plays | direct_pct | completion_pct | last_seen
	Dir    string // asc | desc
	Limit  int
	Offset int
}

// AdminInsightsDevicesQuery extends the shared window query with Devices controls.
type AdminInsightsDevicesQuery struct {
	AdminInsightsWindowQuery
	MinPlays int
}

// AdminInsightsBandwidthQuery holds the time-range parameters for Bandwidth.
type AdminInsightsBandwidthQuery struct {
	Range    string
	FromDate string
	ToDate   string
	TZ       string
}

// AdminInsightsCoverage reports honest pre-upgrade data boundaries so the UI
// never conflates missing legacy telemetry with zeroes.
type AdminInsightsCoverage struct {
	OldestHistoryAt    *time.Time
	OldestMeasuredAt   *time.Time
	OldestRouteFactsAt *time.Time
	OldestMetricAt     *time.Time
	TotalPlays         int64
	MeasuredPlays      int64
	RouteFactsPlays    int64
}

// AdminInsightsActivitySummary aggregates KPIs for the Activity tab.
type AdminInsightsActivitySummary struct {
	Plays                 int64
	Movies                int64
	Episodes              int64
	OtherMedia            int64
	WatchSeconds          int64
	MeasuredWatchSeconds  int64
	MeasuredPlays         int64
	CompletedPlays        int64
	CompletionRate        float64
	DirectPlays           int64
	RemuxPlays            int64
	TranscodePlays        int64
	UniqueAccounts        int64
	UniqueProfiles        int64
	PeakConcurrentStreams int64
	PeakConcurrentAt      *time.Time
}

// AdminInsightsActivityBucket is one day/week/month bar on the Activity chart.
type AdminInsightsActivityBucket struct {
	BucketStart  time.Time
	LabelDate    string // YYYY-MM-DD in requested TZ
	Plays        int64
	WatchSeconds int64
	Direct       int64
	Remux        int64
	Transcode    int64
	Movies       int64
	Episodes     int64
	OtherMedia   int64
}

// AdminInsightsHeatmapCell is one non-zero (day_of_week, hour) cell in the
// admin's requested timezone (day_of_week 0=Monday..6=Sunday, hour 0..23).
type AdminInsightsHeatmapCell struct {
	DayOfWeek    int
	Hour         int
	Plays        int64
	WatchSeconds int64
}

// AdminInsightsPeakStreamsPoint is one UTC calendar day's peak concurrent
// stream counts, combining dashboard_metric_daily with live samples.
type AdminInsightsPeakStreamsPoint struct {
	Date                 string // YYYY-MM-DD (UTC)
	PeakStreams          int64
	PeakDirect           int64
	PeakRemux            int64
	PeakTranscode        int64
	PeakStreamsDirect    int64
	PeakStreamsRemux     int64
	PeakStreamsTranscode int64
	PeakStreamsAt        *time.Time
}

// AdminInsightsActivity is the payload for GET /api/v2/admin/insights/activity.
type AdminInsightsActivity struct {
	From        time.Time
	To          time.Time
	TZ          string
	BucketUnit  string
	Coverage    AdminInsightsCoverage
	Summary     AdminInsightsActivitySummary
	Buckets     []AdminInsightsActivityBucket
	Heatmap     []AdminInsightsHeatmapCell
	PeakStreams []AdminInsightsPeakStreamsPoint
}

// AdminInsightsPersonProfile is one household profile nested under an account.
type AdminInsightsPersonProfile struct {
	ProfileID      string
	ProfileName    string
	IsLive         bool
	Plays          int64
	WatchSeconds   int64
	MeasuredPlays  int64
	DirectPlays    int64
	RemuxPlays     int64
	TranscodePlays int64
	CompletedPlays int64
	CompletionRate float64
	LastSeenAt     *time.Time
}

// AdminInsightsPersonAccount is one account row on the People leaderboard.
type AdminInsightsPersonAccount struct {
	UserID         int
	Username       string
	Role           string
	IsLive         bool
	Plays          int64
	Movies         int64
	Episodes       int64
	WatchSeconds   int64
	MeasuredPlays  int64
	MeasuredShare  float64
	DirectPlays    int64
	RemuxPlays     int64
	TranscodePlays int64
	DirectRate     float64
	CompletedPlays int64
	CompletionRate float64
	TopClient      *string
	LastSeenAt     *time.Time
	Profiles       []AdminInsightsPersonProfile
}

// AdminInsightsPeople is the payload for GET /api/v2/admin/insights/people.
type AdminInsightsPeople struct {
	From           time.Time
	To             time.Time
	TZ             string
	TotalAccounts  int64
	ActiveAccounts int64
	ActiveProfiles int64
	LiveAccounts   int64
	Coverage       AdminInsightsCoverage
	Accounts       []AdminInsightsPersonAccount
}

// AdminInsightsDevicesSummary aggregates KPIs for Devices & Quality.
type AdminInsightsDevicesSummary struct {
	TotalPlays          int64
	DirectPlays         int64
	RemuxPlays          int64
	TranscodePlays      int64
	DirectRate          float64
	RouteFactsPlays     int64
	HWTranscodePlays    int64
	SWTranscodePlays    int64
	SubtitleBurnInPlays int64
	HDRToneMapPlays     int64
	HDRToneMapHWPlays   int64
	HDRToneMapSWPlays   int64
}

// AdminInsightsClientCodecCell is one cell of the Client × Source Video Codec matrix.
type AdminInsightsClientCodecCell struct {
	SourceVideoCodec string
	Plays            int64
	DirectPlays      int64
	RemuxPlays       int64
	TranscodePlays   int64
	DirectRate       float64
}

// AdminInsightsClientMatrixRow groups plays by (client_name, device_platform).
type AdminInsightsClientMatrixRow struct {
	ClientName       string
	DevicePlatform   string
	IsJellyfinClient bool
	TotalPlays       int64
	DirectPlays      int64
	RemuxPlays       int64
	TranscodePlays   int64
	DirectRate       float64
	Cells            []AdminInsightsClientCodecCell
}

// AdminInsightsAdaptationReason counts one canonical adaptation reason code.
type AdminInsightsAdaptationReason struct {
	Reason         string
	Plays          int64
	ShareOfAdapted float64
}

// AdminInsightsLabeledCount is a generic key + play count pair for compact
// breakdowns (HW accel backends, HDR tone-map modes, burn-in subtitle codecs).
type AdminInsightsLabeledCount struct {
	Key   string
	Plays int64
}

// AdminInsightsTopTranscodingAccount ranks accounts with the most transcodes.
type AdminInsightsTopTranscodingAccount struct {
	UserID         int
	Username       string
	TranscodePlays int64
	TotalPlays     int64
	TranscodeRate  float64
	TopReason      *string
}

// AdminInsightsDevices is the payload for GET /api/v2/admin/insights/devices.
type AdminInsightsDevices struct {
	From                  time.Time
	To                    time.Time
	TZ                    string
	Coverage              AdminInsightsCoverage
	Summary               AdminInsightsDevicesSummary
	Codecs                []string
	Matrix                []AdminInsightsClientMatrixRow
	AdaptationReasons     []AdminInsightsAdaptationReason
	HWAccelBreakdown      []AdminInsightsLabeledCount
	ToneMapBreakdown      []AdminInsightsLabeledCount
	SubtitleBurnInCodecs  []AdminInsightsLabeledCount
	TopTranscodingAccount []AdminInsightsTopTranscodingAccount
}

// AdminInsightsBandwidthSummary aggregates KPIs for the Bandwidth tab.
type AdminInsightsBandwidthSummary struct {
	TotalEgressBytes     int64
	PlaybackEgressBytes  int64
	DownloadEgressBytes  int64
	APIEgressBytes       int64
	NodeEgressBytes      int64
	AvgDailyBytes        int64
	PeakEgressKbps       int64
	PeakEgressAt         *time.Time
	PeakDownloadKbps     int64
	ActiveDays           int64
	RemotePlays          int64
	LocalPlays           int64
	UnknownLocationPlays int64
}

// AdminInsightsEgressSource summarizes one bandwidth source over the window.
type AdminInsightsEgressSource struct {
	Key            string
	Kind           string // "api" | "node"
	NodeID         int
	Name           string
	NodeType       *string // "proxy" | "transcode" for node; nil for api/removed
	EgressBytes    int64
	PeakEgressKbps int64
	ShareOfTotal   float64
}

// AdminInsightsBandwidthPoint is one UTC calendar day's volume & peak rates.
type AdminInsightsBandwidthPoint struct {
	Date                string // YYYY-MM-DD (UTC)
	EgressBytes         int64
	PlaybackEgressBytes int64
	DownloadEgressBytes int64
	APIEgressBytes      int64
	PeakEgressKbps      int64
	PeakDownloadKbps    int64
	BytesBySource       map[string]int64
	PeakKbpsBySource    map[string]int64
}

// AdminInsightsBandwidth is the payload for GET /api/v2/admin/insights/bandwidth.
type AdminInsightsBandwidth struct {
	From               time.Time
	To                 time.Time
	TZ                 string
	OldestMetricAt     *time.Time
	OldestRouteFactsAt *time.Time
	HasRemoteNodes     bool
	Summary            AdminInsightsBandwidthSummary
	Sources            []AdminInsightsEgressSource
	Points             []AdminInsightsBandwidthPoint
}

// AdminInsightsSource provides cached or freshly queried Insights payloads.
type AdminInsightsSource interface {
	Activity(ctx context.Context, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error)
	People(ctx context.Context, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error)
	Devices(ctx context.Context, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error)
	Bandwidth(ctx context.Context, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error)
	Invalidate()
}

// AdminInsightsProvider caches standard preset Insights reads in memory and
// invalidates on admin/playback bus events.
type AdminInsightsProvider struct {
	pool      *pgxpool.Pool
	activity  *cache.TTLCache[*AdminInsightsActivity]
	people    *cache.TTLCache[*AdminInsightsPeople]
	devices   *cache.TTLCache[*AdminInsightsDevices]
	bandwidth *cache.TTLCache[*AdminInsightsBandwidth]
	ttl       time.Duration
}

var _ AdminInsightsSource = (*AdminInsightsProvider)(nil)

// NewAdminInsightsProvider creates an event-bus-invalidated Insights cache provider.
func NewAdminInsightsProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminInsightsProvider, error) {
	p := &AdminInsightsProvider{
		pool:      pool,
		activity:  cache.NewTTLCache[*AdminInsightsActivity](),
		people:    cache.NewTTLCache[*AdminInsightsPeople](),
		devices:   cache.NewTTLCache[*AdminInsightsDevices](),
		bandwidth: cache.NewTTLCache[*AdminInsightsBandwidth](),
		ttl:       adminInsightsCacheTTL,
	}
	if bus == nil || ctx == nil {
		return p, nil
	}
	handler := func(cache.Event) {
		p.Invalidate()
	}
	for _, channel := range []string{cache.ChannelAdmin, cache.ChannelPlayback} {
		if err := bus.Subscribe(ctx, channel, handler); err != nil {
			p.Close()
			return nil, fmt.Errorf("subscribing admin insights provider to %s: %w", channel, err)
		}
	}
	return p, nil
}

// Invalidate drops all cached Insights entries across all four tabs.
func (p *AdminInsightsProvider) Invalidate() {
	if p == nil {
		return
	}
	if p.activity != nil {
		p.activity.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.people != nil {
		p.people.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.devices != nil {
		p.devices.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.bandwidth != nil {
		p.bandwidth.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
}

// Close stops all background TTL sweepers.
func (p *AdminInsightsProvider) Close() {
	if p == nil {
		return
	}
	if p.activity != nil {
		p.activity.Close()
	}
	if p.people != nil {
		p.people.Close()
	}
	if p.devices != nil {
		p.devices.Close()
	}
	if p.bandwidth != nil {
		p.bandwidth.Close()
	}
}

func insightsWindowCacheable(rangeVal string) bool {
	return normalizeInsightsRange(rangeVal) != InsightsRangeCustom
}

func (p *AdminInsightsProvider) Activity(ctx context.Context, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsActivity(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sactivity:r=%s:tz=%s:lib=%d:u=%d", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, q.UserID)
	if v, ok := p.activity.Get(key); ok {
		return v, nil
	}
	v, err := queryAdminInsightsActivity(ctx, p.pool, q)
	if err != nil {
		return nil, err
	}
	p.activity.Set(key, v, p.ttl)
	return v, nil
}

func (p *AdminInsightsProvider) People(ctx context.Context, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) || strings.TrimSpace(q.Search) != "" || q.Offset != 0 {
		return queryAdminInsightsPeople(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%speople:r=%s:tz=%s:lib=%d:s=%s:d=%s:l=%d", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, strings.TrimSpace(q.Sort), strings.TrimSpace(q.Dir), clampInsightsPeopleLimit(q.Limit))
	if v, ok := p.people.Get(key); ok {
		return v, nil
	}
	v, err := queryAdminInsightsPeople(ctx, p.pool, q)
	if err != nil {
		return nil, err
	}
	p.people.Set(key, v, p.ttl)
	return v, nil
}

func (p *AdminInsightsProvider) Devices(ctx context.Context, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsDevices(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sdevices:r=%s:tz=%s:lib=%d:u=%d:m=%d", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, q.UserID, clampInsightsMinPlays(q.MinPlays))
	if v, ok := p.devices.Get(key); ok {
		return v, nil
	}
	v, err := queryAdminInsightsDevices(ctx, p.pool, q)
	if err != nil {
		return nil, err
	}
	p.devices.Set(key, v, p.ttl)
	return v, nil
}

func (p *AdminInsightsProvider) Bandwidth(ctx context.Context, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsBandwidth(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sbandwidth:r=%s:tz=%s", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ))
	if v, ok := p.bandwidth.Get(key); ok {
		return v, nil
	}
	v, err := queryAdminInsightsBandwidth(ctx, p.pool, q)
	if err != nil {
		return nil, err
	}
	p.bandwidth.Set(key, v, p.ttl)
	return v, nil
}

// Service entrypoints on AdminHandler used by internal/apiv2.

func (h *AdminHandler) ReadAdminInsightsActivity(ctx context.Context, q AdminInsightsWindowQuery, refresh bool) (*AdminInsightsActivity, error) {
	if h.InsightsSource != nil {
		if refresh {
			h.InsightsSource.Invalidate()
		}
		return h.InsightsSource.Activity(ctx, q)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminInsightsActivity(ctx, h.pool, q)
}

func (h *AdminHandler) ReadAdminInsightsPeople(ctx context.Context, q AdminInsightsPeopleQuery, refresh bool) (*AdminInsightsPeople, error) {
	if h.InsightsSource != nil {
		if refresh {
			h.InsightsSource.Invalidate()
		}
		return h.InsightsSource.People(ctx, q)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminInsightsPeople(ctx, h.pool, q)
}

func (h *AdminHandler) ReadAdminInsightsDevices(ctx context.Context, q AdminInsightsDevicesQuery, refresh bool) (*AdminInsightsDevices, error) {
	if h.InsightsSource != nil {
		if refresh {
			h.InsightsSource.Invalidate()
		}
		return h.InsightsSource.Devices(ctx, q)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminInsightsDevices(ctx, h.pool, q)
}

func (h *AdminHandler) ReadAdminInsightsBandwidth(ctx context.Context, q AdminInsightsBandwidthQuery, refresh bool) (*AdminInsightsBandwidth, error) {
	if h.InsightsSource != nil {
		if refresh {
			h.InsightsSource.Invalidate()
		}
		return h.InsightsSource.Bandwidth(ctx, q)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminInsightsBandwidth(ctx, h.pool, q)
}

// Window validation & resolution helpers.

type resolvedInsightsWindow struct {
	Range      string
	TZ         string
	Loc        *time.Location
	From       time.Time // UTC inclusive lower bound (zero if AllTime until resolved from earliest row)
	To         time.Time // UTC exclusive upper bound (or now for presets)
	AllTime    bool
	BucketUnit string
}

func normalizeInsightsRange(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return InsightsRange30d
	}
	return v
}

func normalizeInsightsTZ(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "UTC"
	}
	return v
}

func clampInsightsPeopleLimit(limit int) int {
	if limit <= 0 {
		return insightsDefaultPeopleLim
	}
	if limit > insightsMaxPeopleLim {
		return insightsMaxPeopleLim
	}
	return limit
}

func clampInsightsMinPlays(minPlays int) int {
	if minPlays <= 0 {
		return insightsDefaultMinPlays
	}
	if minPlays > insightsMaxMinPlays {
		return insightsMaxMinPlays
	}
	return minPlays
}

func pickInsightsBucketUnit(span time.Duration) string {
	days := span.Hours() / 24
	switch {
	case days <= 62:
		return InsightsBucketDay
	case days <= 275:
		return InsightsBucketWeek
	default:
		return InsightsBucketMonth
	}
}

func resolveInsightsWindow(nowUTC time.Time, rangeRaw, fromDateRaw, toDateRaw, tzRaw string) (resolvedInsightsWindow, error) {
	tz := normalizeInsightsTZ(tzRaw)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return resolvedInsightsWindow{}, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInsightsQuery, tz)
	}
	rng := normalizeInsightsRange(rangeRaw)
	nowLocal := nowUTC.In(loc)
	todayStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	out := resolvedInsightsWindow{Range: rng, TZ: tz, Loc: loc, To: nowUTC.UTC()}

	switch rng {
	case InsightsRange7d:
		out.From = todayStart.AddDate(0, 0, -6).UTC()
		out.BucketUnit = InsightsBucketDay
	case InsightsRange30d:
		out.From = todayStart.AddDate(0, 0, -29).UTC()
		out.BucketUnit = InsightsBucketDay
	case InsightsRange90d:
		out.From = todayStart.AddDate(0, 0, -89).UTC()
		out.BucketUnit = InsightsBucketWeek
	case InsightsRange1y:
		out.From = todayStart.AddDate(0, 0, -364).UTC()
		out.BucketUnit = InsightsBucketMonth
	case InsightsRangeAll:
		out.AllTime = true
		out.BucketUnit = InsightsBucketMonth
	case InsightsRangeCustom:
		fromStr := strings.TrimSpace(fromDateRaw)
		toStr := strings.TrimSpace(toDateRaw)
		if fromStr == "" || toStr == "" {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: from_date and to_date are required for custom range", ErrInvalidInsightsQuery)
		}
		fromDay, err := time.ParseInLocation("2006-01-02", fromStr, loc)
		if err != nil {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: invalid from_date %q", ErrInvalidInsightsQuery, fromStr)
		}
		toDay, err := time.ParseInLocation("2006-01-02", toStr, loc)
		if err != nil {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: invalid to_date %q", ErrInvalidInsightsQuery, toStr)
		}
		if toDay.Before(fromDay) {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: to_date must be on or after from_date", ErrInvalidInsightsQuery)
		}
		endLocal := toDay.AddDate(0, 0, 1)
		span := endLocal.Sub(fromDay)
		if span > time.Duration(insightsCustomMaxSpanDays)*24*time.Hour {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: custom range exceeds %d days", ErrInvalidInsightsQuery, insightsCustomMaxSpanDays)
		}
		out.From = fromDay.UTC()
		out.To = endLocal.UTC()
		out.BucketUnit = pickInsightsBucketUnit(span)
	default:
		return resolvedInsightsWindow{}, fmt.Errorf("%w: unsupported range %q", ErrInvalidInsightsQuery, rng)
	}
	return out, nil
}

package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

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

	insightsCustomMaxSpanDays  = 3660
	insightsDefaultPeopleLim   = 50
	insightsMaxPeopleLim       = 100
	insightsDefaultMinSessions = 5
	insightsMaxMinSessions     = 1000
	insightsDefaultTitlesLimit = 10
	insightsMaxTitlesLimit     = 50

	InsightsTitlesSortPlays     = "plays"
	InsightsTitlesSortWatchTime = "watch_time"
	InsightsTitlesSortViewers   = "viewers"
)

// ErrInvalidInsightsQuery marks client-supplied filter or range validation errors.
var ErrInvalidInsightsQuery = errors.New("invalid insights query")

// ErrInsightsTitleNotFound marks a title detail read for a content id that is
// not in the catalog.
var ErrInsightsTitleNotFound = errors.New("insights title not found")

// AdminInsightsWindowQuery holds the shared Insights filter bar parameters.
type AdminInsightsWindowQuery struct {
	Range     string
	FromDate  string // YYYY-MM-DD (local date in TZ), used when Range == "custom"
	ToDate    string // YYYY-MM-DD (local date in TZ, inclusive), used when Range == "custom"
	TZ        string // IANA timezone name; empty defaults to UTC
	LibraryID int
	UserID    int
	// ProfileID narrows to one household profile of UserID. Profile ids repeat
	// across accounts (every account can have a "default" profile), so it is
	// rejected without UserID.
	ProfileID string
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
	MinSessions int
}

// AdminInsightsBandwidthQuery holds the time-range parameters for Bandwidth.
type AdminInsightsBandwidthQuery struct {
	Range    string
	FromDate string
	ToDate   string
	TZ       string
}

// AdminInsightsTitlesQuery extends the shared window query with the Titles
// ranking controls.
type AdminInsightsTitlesQuery struct {
	AdminInsightsWindowQuery
	Sort  string // plays | watch_time | viewers
	Limit int
}

// AdminInsightsTitleQuery reads one movie or series over the shared window.
type AdminInsightsTitleQuery struct {
	AdminInsightsWindowQuery
	ContentID string
}

// AdminInsightsCoverage reports honest pre-upgrade data boundaries so the UI
// never conflates missing legacy telemetry with zeroes. Its counts are
// sessions (history rows), not plays: measurement and route facts are
// recorded per session.
type AdminInsightsCoverage struct {
	OldestHistoryAt    *time.Time
	OldestMeasuredAt   *time.Time
	OldestRouteFactsAt *time.Time
	OldestMetricAt     *time.Time
	TotalSessions      int64
	MeasuredSessions   int64
	RouteFactsSessions int64
}

// AdminInsightsActivitySummary aggregates KPIs for the Activity tab. Play
// counts follow the viewing rule in admin_insights_plays.go; Sessions counts
// the history rows behind them.
type AdminInsightsActivitySummary struct {
	Plays                 int64
	Sessions              int64
	Movies                int64
	Episodes              int64
	OtherMedia            int64
	WatchSeconds          int64
	MeasuredWatchSeconds  int64
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
	ProfileID        string
	ProfileName      string
	IsLive           bool
	Plays            int64
	Sessions         int64
	WatchSeconds     int64
	MeasuredSessions int64
	DirectPlays      int64
	RemuxPlays       int64
	TranscodePlays   int64
	CompletedPlays   int64
	CompletionRate   float64
	LastSeenAt       *time.Time
}

// AdminInsightsPersonAccount is one account row on the People leaderboard.
type AdminInsightsPersonAccount struct {
	UserID           int
	Username         string
	Role             string
	IsLive           bool
	Plays            int64
	Sessions         int64
	Movies           int64
	Episodes         int64
	WatchSeconds     int64
	MeasuredSessions int64
	MeasuredShare    float64
	DirectPlays      int64
	RemuxPlays       int64
	TranscodePlays   int64
	DirectRate       float64
	CompletedPlays   int64
	CompletionRate   float64
	TopClient        *string
	LastSeenAt       *time.Time
	Profiles         []AdminInsightsPersonProfile
}

// AdminInsightsPeople is the payload for GET /api/v2/admin/insights/people.
type AdminInsightsPeople struct {
	From              time.Time
	To                time.Time
	TZ                string
	TotalAccounts     int64
	ActiveAccounts    int64
	ActiveProfiles    int64
	LiveAccounts      int64
	TotalWatchSeconds int64
	Coverage          AdminInsightsCoverage
	Accounts          []AdminInsightsPersonAccount
}

// AdminInsightsDevicesSummary aggregates KPIs for Devices & Quality. Devices
// counts sessions: each session has its own route.
type AdminInsightsDevicesSummary struct {
	TotalSessions          int64
	DirectSessions         int64
	RemuxSessions          int64
	TranscodeSessions      int64
	DirectRate             float64
	RouteFactsSessions     int64
	HWTranscodeSessions    int64
	SWTranscodeSessions    int64
	SubtitleBurnInSessions int64
	HDRToneMapSessions     int64
	HDRToneMapHWSessions   int64
	HDRToneMapSWSessions   int64
}

// AdminInsightsClientCodecCell is one cell of the Client × Source Video Codec matrix.
type AdminInsightsClientCodecCell struct {
	SourceVideoCodec  string
	Sessions          int64
	DirectSessions    int64
	RemuxSessions     int64
	TranscodeSessions int64
	DirectRate        float64
}

// AdminInsightsClientMatrixRow groups sessions by (client_name, device_platform).
type AdminInsightsClientMatrixRow struct {
	ClientName        string
	DevicePlatform    string
	IsJellyfinClient  bool
	TotalSessions     int64
	DirectSessions    int64
	RemuxSessions     int64
	TranscodeSessions int64
	DirectRate        float64
	Cells             []AdminInsightsClientCodecCell
}

// AdminInsightsAdaptationReason counts one canonical adaptation reason code.
type AdminInsightsAdaptationReason struct {
	Reason         string
	Sessions       int64
	ShareOfAdapted float64
}

// AdminInsightsLabeledCount is a generic key + session count pair for compact
// breakdowns (HW accel backends, HDR tone-map modes, burn-in subtitle codecs,
// resolution classes).
type AdminInsightsLabeledCount struct {
	Key      string
	Sessions int64
}

// AdminInsightsResolutionChange counts sessions delivered at a lower
// resolution class than their source.
type AdminInsightsResolutionChange struct {
	From     string
	To       string
	Sessions int64
}

// AdminInsightsDevices is the payload for GET /api/v2/admin/insights/devices.
type AdminInsightsDevices struct {
	From                 time.Time
	To                   time.Time
	TZ                   string
	Coverage             AdminInsightsCoverage
	Summary              AdminInsightsDevicesSummary
	Codecs               []string
	Matrix               []AdminInsightsClientMatrixRow
	AdaptationReasons    []AdminInsightsAdaptationReason
	HWAccelBreakdown     []AdminInsightsLabeledCount
	ToneMapBreakdown     []AdminInsightsLabeledCount
	SubtitleBurnInCodecs []AdminInsightsLabeledCount
	SourceResolutions    []AdminInsightsLabeledCount
	DeliveredResolutions []AdminInsightsLabeledCount
	ResolutionChanges    []AdminInsightsResolutionChange
}

// AdminInsightsBandwidthSummary aggregates KPIs for the Bandwidth tab.
type AdminInsightsBandwidthSummary struct {
	TotalEgressBytes        int64
	PlaybackEgressBytes     int64
	DownloadEgressBytes     int64
	APIEgressBytes          int64
	NodeEgressBytes         int64
	AvgDailyBytes           int64
	PeakEgressKbps          int64
	PeakEgressAt            *time.Time
	PeakDownloadKbps        int64
	ActiveDays              int64
	RemoteSessions          int64
	LocalSessions           int64
	UnknownLocationSessions int64
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

// AdminInsightsTitleRow is one movie or series on the Titles leaderboard.
// Viewers counts distinct profiles with a play in the window; a series rolls
// up the plays of all its episodes.
type AdminInsightsTitleRow struct {
	ContentID      string
	Kind           string // movie | series
	Title          string
	Year           *int
	Plays          int64
	Viewers        int64
	CompletedPlays int64
	Episodes       int64 // series: distinct episodes with a play
	WatchSeconds   int64
	LastPlayedAt   *time.Time
}

// AdminInsightsLibraryRow is one library on the Titles leaderboard.
type AdminInsightsLibraryRow struct {
	LibraryID    int
	Name         string
	Plays        int64
	Viewers      int64
	WatchSeconds int64
}

// AdminInsightsTitles is the payload for GET /api/v2/admin/insights/titles.
type AdminInsightsTitles struct {
	From      time.Time
	To        time.Time
	TZ        string
	Sort      string
	Movies    []AdminInsightsTitleRow
	Shows     []AdminInsightsTitleRow
	Libraries []AdminInsightsLibraryRow
}

// AdminInsightsEpisodeRef identifies one episode of a series.
type AdminInsightsEpisodeRef struct {
	ContentID     string
	SeasonNumber  int
	EpisodeNumber int
	Title         string
}

// AdminInsightsTitleSummary aggregates one title over the window.
type AdminInsightsTitleSummary struct {
	Plays          int64
	Sessions       int64
	Viewers        int64
	CompletedPlays int64
	WatchSeconds   int64
	DirectPlays    int64
	RemuxPlays     int64
	TranscodePlays int64
}

// AdminInsightsPlaysBucket is one day/week/month of plays and watch time.
type AdminInsightsPlaysBucket struct {
	BucketStart  time.Time
	LabelDate    string
	Plays        int64
	WatchSeconds int64
}

// AdminInsightsTitleViewer is one profile that played the title in the window.
type AdminInsightsTitleViewer struct {
	UserID                  int
	Username                string
	ProfileID               string
	ProfileName             string
	Plays                   int64
	CompletedPlays          int64
	WatchSeconds            int64
	LastPlayedAt            *time.Time
	FurthestPositionSeconds *float64                 // movies: furthest end position in the window
	LastEpisode             *AdminInsightsEpisodeRef // series: the episode of the latest play
}

// AdminInsightsTitleEpisode is one episode with a play in the window.
type AdminInsightsTitleEpisode struct {
	AdminInsightsEpisodeRef
	Plays        int64
	Viewers      int64
	WatchSeconds int64
}

// AdminInsightsTitle is the payload for GET /api/v2/admin/insights/titles/{content_id}.
type AdminInsightsTitle struct {
	From              time.Time
	To                time.Time
	TZ                string
	BucketUnit        string
	ContentID         string
	Kind              string // movie | series
	Title             string
	Year              *int
	DurationSeconds   *float64 // movies only
	Summary           AdminInsightsTitleSummary
	Buckets           []AdminInsightsPlaysBucket
	Viewers           []AdminInsightsTitleViewer
	ViewersTruncated  bool
	Episodes          []AdminInsightsTitleEpisode
	EpisodesTruncated bool
}

// AdminInsightsSource provides cached or freshly queried Insights payloads.
type AdminInsightsSource interface {
	Activity(ctx context.Context, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error)
	People(ctx context.Context, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error)
	Devices(ctx context.Context, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error)
	Bandwidth(ctx context.Context, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error)
	Titles(ctx context.Context, q AdminInsightsTitlesQuery) (*AdminInsightsTitles, error)
	Invalidate()
}

// AdminInsightsProvider caches standard preset Insights reads in memory for
// 60 seconds and clears local entries on admin/playback bus events (the same
// channels the sibling admin_stats_* providers subscribe to). Title detail is
// read on demand and not cached. Misses for one key fill once per generation:
// concurrent requests share the read, and a later fill starts only after the
// earlier one published, so fills publish in order. The generation counter,
// compared and published under publishMu, keeps a fill whose DB snapshot
// predates an invalidation from repopulating the cache with stale results.
type AdminInsightsProvider struct {
	pool      *pgxpool.Pool
	activity  *cache.TTLCache[*AdminInsightsActivity]
	people    *cache.TTLCache[*AdminInsightsPeople]
	devices   *cache.TTLCache[*AdminInsightsDevices]
	bandwidth *cache.TTLCache[*AdminInsightsBandwidth]
	titles    *cache.TTLCache[*AdminInsightsTitles]
	ttl       time.Duration
	fills     singleflight.Group
	publishMu sync.Mutex
	gen       uint64 // guarded by publishMu
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
		titles:    cache.NewTTLCache[*AdminInsightsTitles](),
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

// Invalidate drops all cached Insights entries across every tab and advances
// the generation counter so concurrent in-flight reads do not repopulate the
// cache with pre-invalidation snapshots.
func (p *AdminInsightsProvider) Invalidate() {
	if p == nil {
		return
	}
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	p.gen++
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
	if p.titles != nil {
		p.titles.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
}

func (p *AdminInsightsProvider) generation() uint64 {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	return p.gen
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
	if p.titles != nil {
		p.titles.Close()
	}
}

func insightsWindowCacheable(rangeVal string) bool {
	return normalizeInsightsRange(rangeVal) != InsightsRangeCustom
}

// cachedInsightsRead serves key from c or fills it. One fill runs per key and
// generation, on a context detached from the caller so one request leaving
// does not fail the others sharing it. The fill is published only when no
// invalidation ran while it was in flight; the comparison and the Set share
// publishMu with Invalidate, so an invalidation cannot slip in between.
func cachedInsightsRead[T comparable](ctx context.Context, p *AdminInsightsProvider, c *cache.TTLCache[T], key string, load func(context.Context) (T, error)) (T, error) {
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	gen := p.generation()
	shared, err, _ := p.fills.Do(fmt.Sprintf("%s#%d", key, gen), func() (any, error) {
		v, err := load(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		p.publishMu.Lock()
		if p.gen == gen {
			c.Set(key, v, p.ttl)
		}
		p.publishMu.Unlock()
		return insightsFill[T]{v}, nil
	})
	var zero T
	if err != nil {
		return zero, err
	}
	fill, ok := shared.(insightsFill[T])
	if !ok {
		return zero, fmt.Errorf("insights cache fill for %s returned %T", key, shared)
	}
	return fill.v, nil
}

// insightsFill carries a fill's value through singleflight's untyped result.
type insightsFill[T any] struct{ v T }

// insightsScopeKey is the cache-key fragment for the shared row filters.
func insightsScopeKey(q AdminInsightsWindowQuery) string {
	return fmt.Sprintf("r=%s:tz=%s:lib=%d:u=%d:p=%q", normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, q.UserID, q.ProfileID)
}

func (p *AdminInsightsProvider) Activity(ctx context.Context, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsActivity(ctx, p.pool, q)
	}
	key := adminInsightsCacheKeyPrefix + "activity:" + insightsScopeKey(q)
	return cachedInsightsRead(ctx, p, p.activity, key, func(ctx context.Context) (*AdminInsightsActivity, error) {
		return queryAdminInsightsActivity(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) People(ctx context.Context, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) || strings.TrimSpace(q.Search) != "" {
		return queryAdminInsightsPeople(ctx, p.pool, q)
	}
	// Cache the full sorted snapshot (independent of limit/offset) so paging
	// past the first page reads from the same snapshot without skipping or
	// duplicating accounts across concurrent plays.
	key := fmt.Sprintf("%speople:r=%s:tz=%s:lib=%d:s=%s:d=%s", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, strings.TrimSpace(q.Sort), strings.TrimSpace(q.Dir))
	full, err := cachedInsightsRead(ctx, p, p.people, key, func(ctx context.Context) (*AdminInsightsPeople, error) {
		return queryAdminInsightsPeopleSnapshot(ctx, p.pool, q)
	})
	if err != nil {
		return nil, err
	}
	return sliceAdminInsightsPeople(full, q.Limit, q.Offset), nil
}

func (p *AdminInsightsProvider) Devices(ctx context.Context, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsDevices(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sdevices:%s:m=%d", adminInsightsCacheKeyPrefix, insightsScopeKey(q.AdminInsightsWindowQuery), clampInsightsMinSessions(q.MinSessions))
	return cachedInsightsRead(ctx, p, p.devices, key, func(ctx context.Context) (*AdminInsightsDevices, error) {
		return queryAdminInsightsDevices(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) Bandwidth(ctx context.Context, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsBandwidth(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sbandwidth:r=%s:tz=%s", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ))
	return cachedInsightsRead(ctx, p, p.bandwidth, key, func(ctx context.Context) (*AdminInsightsBandwidth, error) {
		return queryAdminInsightsBandwidth(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) Titles(ctx context.Context, q AdminInsightsTitlesQuery) (*AdminInsightsTitles, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsTitles(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%stitles:%s:s=%s:n=%d", adminInsightsCacheKeyPrefix, insightsScopeKey(q.AdminInsightsWindowQuery), normalizeInsightsTitlesSort(q.Sort), clampInsightsTitlesLimit(q.Limit))
	return cachedInsightsRead(ctx, p, p.titles, key, func(ctx context.Context) (*AdminInsightsTitles, error) {
		return queryAdminInsightsTitles(ctx, p.pool, q)
	})
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

func (h *AdminHandler) ReadAdminInsightsTitles(ctx context.Context, q AdminInsightsTitlesQuery, refresh bool) (*AdminInsightsTitles, error) {
	if h.InsightsSource != nil {
		if refresh {
			h.InsightsSource.Invalidate()
		}
		return h.InsightsSource.Titles(ctx, q)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminInsightsTitles(ctx, h.pool, q)
}

// ReadAdminInsightsTitle reads one title's detail. It is small and keyed by an
// arbitrary content id, so it skips the cache.
func (h *AdminHandler) ReadAdminInsightsTitle(ctx context.Context, q AdminInsightsTitleQuery) (*AdminInsightsTitle, error) {
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminInsightsTitle(ctx, h.pool, q)
}

// Window validation & resolution helpers.

type resolvedInsightsWindow struct {
	Range         string
	TZ            string
	Loc           *time.Location
	From          time.Time // UTC inclusive lower bound (zero if AllTime until resolved from earliest row)
	To            time.Time // UTC exclusive upper bound (or now for presets)
	FromLocalDate string    // YYYY-MM-DD in Loc (set for presets & custom; resolved for AllTime)
	ToLocalDate   string    // YYYY-MM-DD in Loc inclusive
	AllTime       bool
	BucketUnit    string
}

// localDayStart returns the earliest valid instant on the calendar date
// (year, month, day) in loc. When DST springs forward right at 00:00 (such as
// America/Santiago or America/Havana), Go's time.Date(y, m, d, 0, 0, 0, 0, loc)
// resolves the nonexistent midnight backward into 23:00 on the previous day.
// Advancing across the gap and binary-searching back to the exact first second
// on (year, month, day) yields the true start of the day (e.g. 01:00).
func localDayStart(year int, month time.Month, day int, loc *time.Location) time.Time {
	t := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if !isBeforeCalendarDate(t, year, month, day) {
		return t
	}
	lo := t
	hi := t
	for isBeforeCalendarDate(hi, year, month, day) {
		hi = hi.Add(time.Hour).In(loc)
	}
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2).In(loc)
		if isBeforeCalendarDate(mid, year, month, day) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

func isBeforeCalendarDate(t time.Time, year int, month time.Month, day int) bool {
	ty, tm, td := t.Date()
	if ty != year {
		return ty < year
	}
	if tm != month {
		return tm < month
	}
	return td < day
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

func clampInsightsMinSessions(minSessions int) int {
	if minSessions <= 0 {
		return insightsDefaultMinSessions
	}
	if minSessions > insightsMaxMinSessions {
		return insightsMaxMinSessions
	}
	return minSessions
}

func clampInsightsTitlesLimit(limit int) int {
	if limit <= 0 {
		return insightsDefaultTitlesLimit
	}
	if limit > insightsMaxTitlesLimit {
		return insightsMaxTitlesLimit
	}
	return limit
}

func normalizeInsightsTitlesSort(raw string) string {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case InsightsTitlesSortWatchTime, InsightsTitlesSortViewers:
		return v
	default:
		return InsightsTitlesSortPlays
	}
}

// validateInsightsScope rejects a profile filter without its account: profile
// ids repeat across accounts.
func validateInsightsScope(q AdminInsightsWindowQuery) error {
	if strings.TrimSpace(q.ProfileID) != "" && q.UserID <= 0 {
		return fmt.Errorf("%w: profile_id requires user_id", ErrInvalidInsightsQuery)
	}
	return nil
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
	todayCal := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, time.UTC)
	presetStart := func(daysBack int) (time.Time, string) {
		cal := todayCal.AddDate(0, 0, -daysBack)
		return localDayStart(cal.Year(), cal.Month(), cal.Day(), loc).UTC(), cal.Format("2006-01-02")
	}
	out := resolvedInsightsWindow{
		Range:       rng,
		TZ:          tz,
		Loc:         loc,
		To:          nowUTC.UTC(),
		ToLocalDate: todayCal.Format("2006-01-02"),
	}

	switch rng {
	case InsightsRange7d:
		out.From, out.FromLocalDate = presetStart(6)
		out.BucketUnit = InsightsBucketDay
	case InsightsRange30d:
		out.From, out.FromLocalDate = presetStart(29)
		out.BucketUnit = InsightsBucketDay
	case InsightsRange90d:
		out.From, out.FromLocalDate = presetStart(89)
		out.BucketUnit = InsightsBucketWeek
	case InsightsRange1y:
		out.From, out.FromLocalDate = presetStart(364)
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
		fromCal, err := time.Parse("2006-01-02", fromStr)
		if err != nil {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: invalid from_date %q", ErrInvalidInsightsQuery, fromStr)
		}
		toCal, err := time.Parse("2006-01-02", toStr)
		if err != nil {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: invalid to_date %q", ErrInvalidInsightsQuery, toStr)
		}
		if toCal.Before(fromCal) {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: to_date must be on or after from_date", ErrInvalidInsightsQuery)
		}
		nextCal := toCal.AddDate(0, 0, 1)
		fromInstant := localDayStart(fromCal.Year(), fromCal.Month(), fromCal.Day(), loc)
		endInstant := localDayStart(nextCal.Year(), nextCal.Month(), nextCal.Day(), loc)
		span := endInstant.Sub(fromInstant)
		if span > time.Duration(insightsCustomMaxSpanDays)*24*time.Hour {
			return resolvedInsightsWindow{}, fmt.Errorf("%w: custom range exceeds %d days", ErrInvalidInsightsQuery, insightsCustomMaxSpanDays)
		}
		out.From = fromInstant.UTC()
		out.To = endInstant.UTC()
		out.FromLocalDate = fromCal.Format("2006-01-02")
		out.ToLocalDate = toCal.Format("2006-01-02")
		out.BucketUnit = pickInsightsBucketUnit(span)
	default:
		return resolvedInsightsWindow{}, fmt.Errorf("%w: unsupported range %q", ErrInvalidInsightsQuery, rng)
	}
	return out, nil
}

package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminInsightsService is the slice of *handlers.AdminHandler backing the
// unified Monitor -> Insights hub.
type AdminInsightsService interface {
	ReadAdminInsightsActivity(context.Context, handlers.AdminInsightsWindowQuery, bool) (*handlers.AdminInsightsActivity, error)
	ReadAdminInsightsPeople(context.Context, handlers.AdminInsightsPeopleQuery, bool) (*handlers.AdminInsightsPeople, error)
	ReadAdminInsightsDevices(context.Context, handlers.AdminInsightsDevicesQuery, bool) (*handlers.AdminInsightsDevices, error)
	ReadAdminInsightsBandwidth(context.Context, handlers.AdminInsightsBandwidthQuery, bool) (*handlers.AdminInsightsBandwidth, error)
	ReadAdminInsightsTitles(context.Context, handlers.AdminInsightsTitlesQuery, bool) (*handlers.AdminInsightsTitles, error)
	ReadAdminInsightsTitle(context.Context, handlers.AdminInsightsTitleQuery) (*handlers.AdminInsightsTitle, error)
	ReadAdminInsightsLibrary(context.Context, handlers.AdminInsightsWindowQuery, bool) (*handlers.AdminInsightsLibrary, error)
}

// AdminInsightsCapabilities reports whether Monitor -> Insights and its
// long-term per-node egress archive are available on this server build.
type AdminInsightsCapabilities struct {
	Capability
	Available       bool `json:"available"`
	NodeEgressDaily bool `json:"node_egress_daily" doc:"Long-term per-node daily egress archive survives past the 31-day minute-sample window"`
	Titles          bool `json:"titles" doc:"The Titles leaderboard (getAdminInsightsTitles) and title detail (getAdminInsightsTitle) are served"`
	ProfileFilter   bool `json:"profile_filter" doc:"Activity, Devices, Titles and title detail accept profile_id together with user_id"`
	Library         bool `json:"library" doc:"The Library read (getAdminInsightsLibrary) is served"`
	Breakdowns      bool `json:"breakdowns" doc:"Activity carries per-bucket active accounts and profiles and end_reasons; Titles carries least_finished; Devices carries client_versions, protocol_buckets, subtitles and audio; People carries top_profiles"`
}

type AdminInsightsCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminInsightsCapabilities
}

type AdminInsightsWindowInput struct {
	Range     string `query:"range" enum:"7d,30d,90d,1y,all,custom" default:"30d" doc:"Preset time window or custom"`
	FromDate  string `query:"from_date" format:"date" doc:"Inclusive local start date (YYYY-MM-DD in tz), required when range=custom"`
	ToDate    string `query:"to_date" format:"date" doc:"Inclusive local end date (YYYY-MM-DD in tz), required when range=custom"`
	TZ        string `query:"tz" maxLength:"128" default:"UTC" doc:"IANA timezone identifier for day/week/month bucketing and heatmap alignment"`
	LibraryID ID     `query:"library_id" doc:"Restrict playback analytics to media belonging to this library"`
	UserID    ID     `query:"user_id" doc:"Restrict playback analytics to this login account"`
	ProfileID string `query:"profile_id" maxLength:"1024" doc:"Restrict playback analytics to this household profile of user_id; requires user_id because profile ids repeat across accounts"`
	Refresh   bool   `query:"refresh" default:"false" doc:"Invalidate cached insights aggregates before reading; ignored for scoped API keys"`
}

type AdminInsightsPeopleInput struct {
	Range     string `query:"range" enum:"7d,30d,90d,1y,all,custom" default:"30d" doc:"Preset time window or custom"`
	FromDate  string `query:"from_date" format:"date" doc:"Inclusive local start date (YYYY-MM-DD in tz), required when range=custom"`
	ToDate    string `query:"to_date" format:"date" doc:"Inclusive local end date (YYYY-MM-DD in tz), required when range=custom"`
	TZ        string `query:"tz" maxLength:"128" default:"UTC" doc:"IANA timezone identifier"`
	LibraryID ID     `query:"library_id" doc:"Restrict people stats to media belonging to this library"`
	Search    string `query:"search" maxLength:"128" doc:"Case-insensitive search across account usernames and household profile names"`
	Sort      string `query:"sort" enum:"watch_time,plays,direct_pct,completion_pct,last_seen" default:"watch_time" doc:"Sort column for accounts"`
	Dir       string `query:"dir" enum:"asc,desc" default:"desc" doc:"Sort direction"`
	Limit     int    `query:"limit" minimum:"1" maximum:"100" default:"50" doc:"Maximum accounts to return"`
	Offset    int    `query:"offset" minimum:"0" maximum:"10000" default:"0" doc:"Account offset for pagination"`
	Refresh   bool   `query:"refresh" default:"false" doc:"Invalidate cached insights aggregates before reading; ignored for scoped API keys"`
}

type AdminInsightsDevicesInput struct {
	Range       string `query:"range" enum:"7d,30d,90d,1y,all,custom" default:"30d" doc:"Preset time window or custom"`
	FromDate    string `query:"from_date" format:"date" doc:"Inclusive local start date (YYYY-MM-DD in tz), required when range=custom"`
	ToDate      string `query:"to_date" format:"date" doc:"Inclusive local end date (YYYY-MM-DD in tz), required when range=custom"`
	TZ          string `query:"tz" maxLength:"128" default:"UTC" doc:"IANA timezone identifier"`
	LibraryID   ID     `query:"library_id" doc:"Restrict device analytics to media belonging to this library"`
	UserID      ID     `query:"user_id" doc:"Restrict device analytics to this login account"`
	ProfileID   string `query:"profile_id" maxLength:"1024" doc:"Restrict device analytics to this household profile of user_id; requires user_id"`
	MinSessions int    `query:"min_sessions" minimum:"1" maximum:"1000" default:"5" doc:"Minimum sessions required for a client/platform row to appear in the direct-play matrix"`
	Refresh     bool   `query:"refresh" default:"false" doc:"Invalidate cached insights aggregates before reading; ignored for scoped API keys"`
}

type AdminInsightsTitlesInput struct {
	Range     string `query:"range" enum:"7d,30d,90d,1y,all,custom" default:"30d" doc:"Preset time window or custom"`
	FromDate  string `query:"from_date" format:"date" doc:"Inclusive local start date (YYYY-MM-DD in tz), required when range=custom"`
	ToDate    string `query:"to_date" format:"date" doc:"Inclusive local end date (YYYY-MM-DD in tz), required when range=custom"`
	TZ        string `query:"tz" maxLength:"128" default:"UTC" doc:"IANA timezone identifier"`
	LibraryID ID     `query:"library_id" doc:"Restrict to plays of media belonging to this library"`
	UserID    ID     `query:"user_id" doc:"Restrict to plays by this login account"`
	ProfileID string `query:"profile_id" maxLength:"1024" doc:"Restrict to plays by this household profile of user_id; requires user_id"`
	Sort      string `query:"sort" enum:"plays,watch_time,viewers" default:"plays" doc:"Ranking: plays (most watched), watch_time, or viewers (most popular: distinct profiles with a play)"`
	Limit     int    `query:"limit" minimum:"1" maximum:"50" default:"10" doc:"Rows per list (movies, shows, libraries)"`
	Refresh   bool   `query:"refresh" default:"false" doc:"Invalidate cached insights aggregates before reading; ignored for scoped API keys"`
}

type AdminInsightsTitleInput struct {
	ContentID string `path:"content_id" maxLength:"1024" doc:"Movie or series content id; an episode id answers 422 (read its series)"`
	Range     string `query:"range" enum:"7d,30d,90d,1y,all,custom" default:"30d" doc:"Preset time window or custom"`
	FromDate  string `query:"from_date" format:"date" doc:"Inclusive local start date (YYYY-MM-DD in tz), required when range=custom"`
	ToDate    string `query:"to_date" format:"date" doc:"Inclusive local end date (YYYY-MM-DD in tz), required when range=custom"`
	TZ        string `query:"tz" maxLength:"128" default:"UTC" doc:"IANA timezone identifier for bucketing"`
	LibraryID ID     `query:"library_id" doc:"Restrict to plays of the title's files in this library"`
	UserID    ID     `query:"user_id" doc:"Restrict to plays by this login account"`
	ProfileID string `query:"profile_id" maxLength:"1024" doc:"Restrict to plays by this household profile of user_id; requires user_id"`
}

type AdminInsightsBandwidthInput struct {
	Range    string `query:"range" enum:"7d,30d,90d,1y,all,custom" default:"30d" doc:"Preset time window or custom"`
	FromDate string `query:"from_date" format:"date" doc:"Inclusive local start date (YYYY-MM-DD in tz), required when range=custom"`
	ToDate   string `query:"to_date" format:"date" doc:"Inclusive local end date (YYYY-MM-DD in tz), required when range=custom"`
	TZ       string `query:"tz" maxLength:"128" default:"UTC" doc:"IANA timezone identifier"`
	Refresh  bool   `query:"refresh" default:"false" doc:"Invalidate cached insights aggregates before reading; ignored for scoped API keys"`
}

// AdminInsightsCoverage counts sessions (playback history rows), the unit
// measurement and route facts are recorded on.
type AdminInsightsCoverage struct {
	OldestHistoryAt    NullableInstant `json:"oldest_history_at"`
	OldestMeasuredAt   NullableInstant `json:"oldest_measured_at"`
	OldestRouteFactsAt NullableInstant `json:"oldest_route_facts_at"`
	OldestMetricAt     NullableInstant `json:"oldest_metric_at"`
	TotalSessions      int64           `json:"total_sessions" doc:"Sessions in scope"`
	MeasuredSessions   int64           `json:"measured_sessions" doc:"Sessions with measured watch time"`
	RouteFactsSessions int64           `json:"route_facts_sessions" doc:"Sessions with recorded route facts"`
}

// AdminInsightsActivitySummary counts plays: a play groups the sessions of
// one profile watching one item in sittings and ignores unfinished attempts
// under 30 seconds (see docs/admin-api.md, Insights).
type AdminInsightsActivitySummary struct {
	Plays                 int64           `json:"plays" doc:"Plays that started in the window"`
	Sessions              int64           `json:"sessions" doc:"Playback sessions that started in the window"`
	Movies                int64           `json:"movies"`
	Episodes              int64           `json:"episodes"`
	OtherMedia            int64           `json:"other_media"`
	WatchSeconds          int64           `json:"watch_seconds"`
	MeasuredWatchSeconds  int64           `json:"measured_watch_seconds"`
	CompletedPlays        int64           `json:"completed_plays"`
	CompletionRate        float64         `json:"completion_rate"`
	DirectPlays           int64           `json:"direct_plays"`
	RemuxPlays            int64           `json:"remux_plays"`
	TranscodePlays        int64           `json:"transcode_plays"`
	UniqueAccounts        int64           `json:"unique_accounts"`
	UniqueProfiles        int64           `json:"unique_profiles"`
	PeakConcurrentStreams int64           `json:"peak_concurrent_streams"`
	PeakConcurrentAt      NullableInstant `json:"peak_concurrent_at"`
}

type AdminInsightsActivityBucket struct {
	BucketStart    Instant `json:"bucket_start"`
	LabelDate      string  `json:"label_date" format:"date" doc:"Local bucket start date (YYYY-MM-DD in requested tz)"`
	Plays          int64   `json:"plays"`
	WatchSeconds   int64   `json:"watch_seconds"`
	Direct         int64   `json:"direct"`
	Remux          int64   `json:"remux"`
	Transcode      int64   `json:"transcode"`
	Movies         int64   `json:"movies"`
	Episodes       int64   `json:"episodes"`
	OtherMedia     int64   `json:"other_media"`
	ActiveAccounts int64   `json:"active_accounts" doc:"Distinct accounts with a session that started in the bucket"`
	ActiveProfiles int64   `json:"active_profiles" doc:"Distinct household profiles with a session that started in the bucket"`
}

type AdminInsightsHeatmapCell struct {
	DayOfWeek    int   `json:"day_of_week" minimum:"0" maximum:"6" doc:"ISO day of week in requested tz: 0=Monday .. 6=Sunday"`
	Hour         int   `json:"hour" minimum:"0" maximum:"23" doc:"Local hour of day (0..23) in requested tz"`
	Plays        int64 `json:"plays"`
	WatchSeconds int64 `json:"watch_seconds"`
}

type AdminInsightsPeakStreamsPoint struct {
	Date                 string          `json:"date" format:"date" doc:"UTC calendar day (YYYY-MM-DD)"`
	PeakStreams          int64           `json:"peak_streams"`
	PeakDirect           int64           `json:"peak_direct"`
	PeakRemux            int64           `json:"peak_remux"`
	PeakTranscode        int64           `json:"peak_transcode"`
	PeakStreamsDirect    int64           `json:"peak_streams_direct"`
	PeakStreamsRemux     int64           `json:"peak_streams_remux"`
	PeakStreamsTranscode int64           `json:"peak_streams_transcode"`
	PeakStreamsAt        NullableInstant `json:"peak_streams_at"`
}

type AdminInsightsActivity struct {
	From        Instant                         `json:"from"`
	To          Instant                         `json:"to"`
	TZ          string                          `json:"tz"`
	BucketUnit  string                          `json:"bucket_unit" enum:"day,week,month"`
	Coverage    AdminInsightsCoverage           `json:"coverage"`
	Summary     AdminInsightsActivitySummary    `json:"summary"`
	Buckets     []AdminInsightsActivityBucket   `json:"buckets"`
	Heatmap     []AdminInsightsHeatmapCell      `json:"heatmap"`
	PeakStreams []AdminInsightsPeakStreamsPoint `json:"peak_streams"`
	EndReasons  []AdminInsightsLabeledCount     `json:"end_reasons" doc:"Sessions that started in the window by end reason (stopped, terminated, expired, aborted, replaced); unknown covers rows recorded before end reasons were"`
}

type AdminInsightsActivityOutput struct {
	Body AdminInsightsActivity
}

type AdminInsightsPersonProfile struct {
	ProfileID        ID              `json:"profile_id"`
	ProfileName      string          `json:"profile_name"`
	IsLive           bool            `json:"is_live"`
	Plays            int64           `json:"plays"`
	Sessions         int64           `json:"sessions"`
	WatchSeconds     int64           `json:"watch_seconds"`
	MeasuredSessions int64           `json:"measured_sessions"`
	DirectPlays      int64           `json:"direct_plays"`
	RemuxPlays       int64           `json:"remux_plays"`
	TranscodePlays   int64           `json:"transcode_plays"`
	CompletedPlays   int64           `json:"completed_plays"`
	CompletionRate   float64         `json:"completion_rate"`
	LastSeenAt       NullableInstant `json:"last_seen_at"`
}

type AdminInsightsPersonAccount struct {
	UserID           ID                           `json:"user_id"`
	Username         string                       `json:"username"`
	Role             string                       `json:"role"`
	IsLive           bool                         `json:"is_live"`
	Plays            int64                        `json:"plays"`
	Sessions         int64                        `json:"sessions"`
	Movies           int64                        `json:"movies"`
	Episodes         int64                        `json:"episodes"`
	WatchSeconds     int64                        `json:"watch_seconds"`
	MeasuredSessions int64                        `json:"measured_sessions"`
	MeasuredShare    float64                      `json:"measured_share" doc:"Share of the account's sessions with measured watch time"`
	DirectPlays      int64                        `json:"direct_plays"`
	RemuxPlays       int64                        `json:"remux_plays"`
	TranscodePlays   int64                        `json:"transcode_plays"`
	DirectRate       float64                      `json:"direct_rate"`
	CompletedPlays   int64                        `json:"completed_plays"`
	CompletionRate   float64                      `json:"completion_rate"`
	TopClient        *string                      `json:"top_client" nullable:"true" doc:"Client with the most sessions in the window"`
	LastSeenAt       NullableInstant              `json:"last_seen_at"`
	Profiles         []AdminInsightsPersonProfile `json:"profiles"`
}

type AdminInsightsPeople struct {
	From              Instant                      `json:"from"`
	To                Instant                      `json:"to"`
	TZ                string                       `json:"tz"`
	TotalAccounts     int64                        `json:"total_accounts"`
	ActiveAccounts    int64                        `json:"active_accounts"`
	ActiveProfiles    int64                        `json:"active_profiles"`
	LiveAccounts      int64                        `json:"live_accounts"`
	TotalWatchSeconds int64                        `json:"total_watch_seconds"`
	Coverage          AdminInsightsCoverage        `json:"coverage"`
	Accounts          []AdminInsightsPersonAccount `json:"accounts"`
	TopProfiles       []AdminInsightsTopProfile    `json:"top_profiles" doc:"Up to 10 household profiles across all accounts with the most plays, whatever the sort, search or page"`
}

type AdminInsightsTopProfile struct {
	UserID       ID     `json:"user_id"`
	Username     string `json:"username"`
	ProfileID    ID     `json:"profile_id"`
	ProfileName  string `json:"profile_name"`
	Plays        int64  `json:"plays"`
	WatchSeconds int64  `json:"watch_seconds"`
}

type AdminInsightsPeopleOutput struct {
	Body AdminInsightsPeople
}

// AdminInsightsDevicesSummary counts sessions: every session has its own
// route, so Devices reports per session rather than per play.
type AdminInsightsDevicesSummary struct {
	TotalSessions          int64   `json:"total_sessions"`
	DirectSessions         int64   `json:"direct_sessions"`
	RemuxSessions          int64   `json:"remux_sessions"`
	TranscodeSessions      int64   `json:"transcode_sessions"`
	DirectRate             float64 `json:"direct_rate"`
	RouteFactsSessions     int64   `json:"route_facts_sessions"`
	HWTranscodeSessions    int64   `json:"hw_transcode_sessions"`
	SWTranscodeSessions    int64   `json:"sw_transcode_sessions"`
	SubtitleBurnInSessions int64   `json:"subtitle_burn_in_sessions"`
	HDRToneMapSessions     int64   `json:"hdr_tonemap_sessions"`
	HDRToneMapHWSessions   int64   `json:"hdr_tonemap_hw_sessions"`
	HDRToneMapSWSessions   int64   `json:"hdr_tonemap_sw_sessions"`
}

type AdminInsightsClientCodecCell struct {
	SourceVideoCodec  string  `json:"source_video_codec"`
	Sessions          int64   `json:"sessions"`
	DirectSessions    int64   `json:"direct_sessions"`
	RemuxSessions     int64   `json:"remux_sessions"`
	TranscodeSessions int64   `json:"transcode_sessions"`
	DirectRate        float64 `json:"direct_rate"`
}

type AdminInsightsClientMatrixRow struct {
	ClientName        string                         `json:"client_name"`
	DevicePlatform    string                         `json:"device_platform"`
	IsJellyfinClient  bool                           `json:"is_jellyfin_client"`
	TotalSessions     int64                          `json:"total_sessions"`
	DirectSessions    int64                          `json:"direct_sessions"`
	RemuxSessions     int64                          `json:"remux_sessions"`
	TranscodeSessions int64                          `json:"transcode_sessions"`
	DirectRate        float64                        `json:"direct_rate"`
	Cells             []AdminInsightsClientCodecCell `json:"cells"`
}

type AdminInsightsAdaptationReason struct {
	Reason         string  `json:"reason"`
	Sessions       int64   `json:"sessions"`
	ShareOfAdapted float64 `json:"share_of_adapted"`
}

type AdminInsightsLabeledCount struct {
	Key      string `json:"key"`
	Sessions int64  `json:"sessions"`
}

type AdminInsightsResolutionChange struct {
	From     string `json:"from" doc:"Source resolution class"`
	To       string `json:"to" doc:"Lower delivered resolution class"`
	Sessions int64  `json:"sessions"`
}

type AdminInsightsDevices struct {
	From                 Instant                         `json:"from"`
	To                   Instant                         `json:"to"`
	TZ                   string                          `json:"tz"`
	Coverage             AdminInsightsCoverage           `json:"coverage"`
	Summary              AdminInsightsDevicesSummary     `json:"summary"`
	Codecs               []string                        `json:"codecs"`
	Matrix               []AdminInsightsClientMatrixRow  `json:"matrix" doc:"Client and platform pairs with at least min_sessions sessions, at most 200 (the ones with the most sessions)"`
	AdaptationReasons    []AdminInsightsAdaptationReason `json:"adaptation_reasons"`
	HWAccelBreakdown     []AdminInsightsLabeledCount     `json:"hw_accel_breakdown"`
	ToneMapBreakdown     []AdminInsightsLabeledCount     `json:"tone_map_breakdown"`
	SubtitleBurnInCodecs []AdminInsightsLabeledCount     `json:"subtitle_burn_in_codecs"`
	SourceResolutions    []AdminInsightsLabeledCount     `json:"source_resolutions" doc:"Sessions by source resolution class (480p, 720p, 1080p, 2160p, 4320p, unknown), on the scale media_files.resolution uses"`
	DeliveredResolutions []AdminInsightsLabeledCount     `json:"delivered_resolutions" doc:"Sessions by delivered resolution class; transcodes recorded before route facts are unknown"`
	ResolutionChanges    []AdminInsightsResolutionChange `json:"resolution_changes" doc:"Sessions delivered at a lower resolution class than their source, by source and delivered class"`
	TopTranscoders       []AdminInsightsTranscoderRow    `json:"top_transcoders" doc:"Up to 10 clients ranked by transcoded sessions, whatever min_sessions is"`
	BucketUnit           string                          `json:"bucket_unit" enum:"day,week,month" doc:"Bucket size of protocol_buckets"`
	ClientVersions       []AdminInsightsClientVersionRow `json:"client_versions" doc:"Up to 50 reported client versions with the most sessions; sessions without a version are left out"`
	ProtocolBuckets      []AdminInsightsProtocolBucket   `json:"protocol_buckets" doc:"Sessions per bucket from Silo's own clients and from Jellyfin-protocol clients"`
	Subtitles            AdminInsightsSubtitleUsage      `json:"subtitles"`
	Audio                AdminInsightsAudioUsage         `json:"audio"`
}

type AdminInsightsTranscoderRow struct {
	ClientName        string `json:"client_name"`
	DevicePlatform    string `json:"device_platform"`
	IsJellyfinClient  bool   `json:"is_jellyfin_client"`
	TotalSessions     int64  `json:"total_sessions"`
	TranscodeSessions int64  `json:"transcode_sessions"`
}

type AdminInsightsDevicesOutput struct {
	Body AdminInsightsDevices
}

type AdminInsightsBandwidthSummary struct {
	TotalEgressBytes        int64           `json:"total_egress_bytes" doc:"Total viewer-facing egress served across the window in bytes (API servers plus stream nodes, relays excluded)."`
	PlaybackEgressBytes     int64           `json:"playback_egress_bytes" doc:"Estimated playback egress in bytes (sum of daily max(egress_bytes - download_egress_bytes, 0)). Includes proxy-served downloads because proxy nodes do not split downloads out."`
	DownloadEgressBytes     int64           `json:"download_egress_bytes" doc:"API file-transfer egress in bytes (offline and direct downloads, ebook/audiobook fetches, theme audio, bandwidth probes)."`
	APIEgressBytes          int64           `json:"api_egress_bytes" doc:"Total viewer-facing egress served directly by API server processes in bytes."`
	NodeEgressBytes         int64           `json:"node_egress_bytes" doc:"Total viewer-facing egress served by remote stream nodes in bytes."`
	AvgDailyBytes           int64           `json:"avg_daily_bytes" doc:"Mean daily egress across active_days in bytes (0 when active_days is 0)."`
	PeakEgressKbps          int64           `json:"peak_egress_kbps" doc:"Highest sampled minute total egress rate in the window in kbps."`
	PeakEgressAt            NullableInstant `json:"peak_egress_at" doc:"Timestamp of the peak_egress_kbps minute, or null when no samples exist."`
	PeakDownloadKbps        int64           `json:"peak_download_kbps" doc:"Highest sampled minute API file-transfer egress rate in the window in kbps."`
	ActiveDays              int64           `json:"active_days" doc:"UTC calendar days in the window with at least one daily rollup or live metric sample."`
	RemoteSessions          int64           `json:"remote_sessions" doc:"Playback sessions started in the window classified as remote (count of sessions, not bytes)."`
	LocalSessions           int64           `json:"local_sessions" doc:"Playback sessions started in the window classified as local (count of sessions, not bytes)."`
	UnknownLocationSessions int64           `json:"unknown_location_sessions" doc:"Playback sessions started in the window without a recorded local/remote classification."`
}

type AdminInsightsEgressSource struct {
	Key            string  `json:"key" doc:"Source key used in bytes_by_source and peak_kbps_by_source: \"api\" or \"node:<node_id>\"."`
	Kind           string  `json:"kind" enum:"api,node" doc:"api covers all API server processes together; node is one stream node."`
	NodeID         *ID     `json:"node_id,omitempty" doc:"Stream node identifier when kind is node."`
	Name           string  `json:"name" doc:"Display name; deleted or archived nodes keep a fallback label."`
	NodeType       *string `json:"node_type" nullable:"true" doc:"Stream node type (proxy or transcode) when known; null for api or removed nodes without a recorded type."`
	EgressBytes    int64   `json:"egress_bytes" doc:"Total bytes this source served over the window."`
	PeakEgressKbps int64   `json:"peak_egress_kbps" doc:"Highest sampled minute egress rate for this source in the window in kbps."`
	ShareOfTotal   float64 `json:"share_of_total" doc:"Fraction of summary.total_egress_bytes served by this source (0 to 1, rounded to 4 decimal places)."`
}

type AdminInsightsBandwidthPoint struct {
	Date                string           `json:"date" format:"date" doc:"UTC calendar day (YYYY-MM-DD)"`
	EgressBytes         int64            `json:"egress_bytes" doc:"Total viewer-facing egress served on this UTC day in bytes."`
	PlaybackEgressBytes int64            `json:"playback_egress_bytes" doc:"Estimated playback egress on this UTC day in bytes (max(egress_bytes - download_egress_bytes, 0))."`
	DownloadEgressBytes int64            `json:"download_egress_bytes" doc:"API file-transfer egress on this UTC day in bytes."`
	APIEgressBytes      int64            `json:"api_egress_bytes" doc:"Viewer-facing egress served directly by API server processes on this UTC day in bytes."`
	PeakEgressKbps      int64            `json:"peak_egress_kbps" doc:"Highest sampled minute total egress rate on this UTC day in kbps."`
	PeakDownloadKbps    int64            `json:"peak_download_kbps" doc:"Highest sampled minute API file-transfer egress rate on this UTC day in kbps."`
	BytesBySource       map[string]int64 `json:"bytes_by_source" doc:"Daily egress in bytes keyed by sources[].key (\"api\" or \"node:<node_id>\")."`
	PeakKbpsBySource    map[string]int64 `json:"peak_kbps_by_source" doc:"Daily peak minute egress in kbps keyed by sources[].key."`
}

type AdminInsightsBandwidth struct {
	From               Instant                       `json:"from" doc:"Start of the resolved window in UTC."`
	To                 Instant                       `json:"to" doc:"End of the resolved window in UTC."`
	TZ                 string                        `json:"tz" doc:"Requested IANA timezone used to resolve window bounds and session location counts; daily points remain UTC calendar days."`
	OldestMetricAt     NullableInstant               `json:"oldest_metric_at" doc:"Oldest retained bandwidth metric timestamp across daily archives and live minute samples, or null."`
	OldestRouteFactsAt NullableInstant               `json:"oldest_route_facts_at" doc:"Oldest session timestamp with recorded route facts (local/remote location), or null."`
	HasRemoteNodes     bool                          `json:"has_remote_nodes" doc:"True when any stream node is registered or contributed bandwidth in the window."`
	Summary            AdminInsightsBandwidthSummary `json:"summary" doc:"Window-wide bandwidth totals, peak rates, and session location counts."`
	Sources            []AdminInsightsEgressSource   `json:"sources" doc:"Per-source totals across the window (API first, then stream nodes by egress_bytes descending)."`
	Points             []AdminInsightsBandwidthPoint `json:"points" doc:"Daily UTC bandwidth points in ascending date order; days with no samples are omitted."`
}

type AdminInsightsBandwidthOutput struct {
	Body AdminInsightsBandwidth
}

type AdminInsightsTitleRow struct {
	ContentID      string          `json:"content_id"`
	Kind           string          `json:"kind" enum:"movie,series"`
	Title          string          `json:"title"`
	Year           *int            `json:"year" nullable:"true"`
	Plays          int64           `json:"plays" doc:"Plays that started in the window; a series counts its episodes' plays"`
	Viewers        int64           `json:"viewers" doc:"Distinct household profiles with a play"`
	CompletedPlays int64           `json:"completed_plays"`
	Episodes       int64           `json:"episodes" doc:"Series: distinct episodes with a play; 0 for movies"`
	WatchSeconds   int64           `json:"watch_seconds"`
	LastPlayedAt   NullableInstant `json:"last_played_at"`
}

type AdminInsightsLibraryRow struct {
	LibraryID    ID     `json:"library_id"`
	Name         string `json:"name"`
	Plays        int64  `json:"plays"`
	Viewers      int64  `json:"viewers"`
	WatchSeconds int64  `json:"watch_seconds"`
}

type AdminInsightsTitles struct {
	From          Instant                   `json:"from"`
	To            Instant                   `json:"to"`
	TZ            string                    `json:"tz"`
	Sort          string                    `json:"sort" enum:"plays,watch_time,viewers"`
	Movies        []AdminInsightsTitleRow   `json:"movies"`
	Shows         []AdminInsightsTitleRow   `json:"shows"`
	Libraries     []AdminInsightsLibraryRow `json:"libraries"`
	LeastFinished []AdminInsightsTitleRow   `json:"least_finished" doc:"Movies and series with at least 3 plays and at least one left unfinished, lowest completion rate first, up to limit"`
}

type AdminInsightsTitlesOutput struct {
	Body AdminInsightsTitles
}

type AdminInsightsEpisodeRef struct {
	ContentID     string `json:"content_id"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	Title         string `json:"title"`
}

type AdminInsightsTitleSummary struct {
	Plays          int64 `json:"plays"`
	Sessions       int64 `json:"sessions"`
	Viewers        int64 `json:"viewers"`
	CompletedPlays int64 `json:"completed_plays"`
	WatchSeconds   int64 `json:"watch_seconds"`
	DirectPlays    int64 `json:"direct_plays"`
	RemuxPlays     int64 `json:"remux_plays"`
	TranscodePlays int64 `json:"transcode_plays"`
}

type AdminInsightsPlaysBucket struct {
	BucketStart  Instant `json:"bucket_start"`
	LabelDate    string  `json:"label_date" format:"date" doc:"Local bucket start date (YYYY-MM-DD in requested tz)"`
	Plays        int64   `json:"plays"`
	WatchSeconds int64   `json:"watch_seconds"`
}

type AdminInsightsTitleViewer struct {
	UserID                  ID                       `json:"user_id"`
	Username                string                   `json:"username"`
	ProfileID               string                   `json:"profile_id"`
	ProfileName             string                   `json:"profile_name"`
	Plays                   int64                    `json:"plays"`
	CompletedPlays          int64                    `json:"completed_plays"`
	WatchSeconds            int64                    `json:"watch_seconds"`
	LastPlayedAt            NullableInstant          `json:"last_played_at"`
	FurthestPositionSeconds *float64                 `json:"furthest_position_seconds" nullable:"true" doc:"Movies: furthest position a session in the window reached; null for series"`
	LastEpisode             *AdminInsightsEpisodeRef `json:"last_episode,omitempty" doc:"Series: the episode of the profile's latest play; absent for movies"`
}

type AdminInsightsTitleEpisode struct {
	ContentID     string `json:"content_id"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	Title         string `json:"title"`
	Plays         int64  `json:"plays"`
	Viewers       int64  `json:"viewers"`
	WatchSeconds  int64  `json:"watch_seconds"`
}

type AdminInsightsTitle struct {
	From              Instant                     `json:"from"`
	To                Instant                     `json:"to"`
	TZ                string                      `json:"tz"`
	BucketUnit        string                      `json:"bucket_unit" enum:"day,week,month"`
	ContentID         string                      `json:"content_id"`
	Kind              string                      `json:"kind" enum:"movie,series"`
	Title             string                      `json:"title"`
	Year              *int                        `json:"year" nullable:"true"`
	DurationSeconds   *float64                    `json:"duration_seconds" nullable:"true" doc:"Movie runtime in seconds; null for series or when unknown"`
	Summary           AdminInsightsTitleSummary   `json:"summary"`
	Buckets           []AdminInsightsPlaysBucket  `json:"buckets"`
	Viewers           []AdminInsightsTitleViewer  `json:"viewers" doc:"Profiles with a play in the window, most recent first"`
	ViewersTruncated  bool                        `json:"viewers_truncated" doc:"More than 100 profiles played the title; viewers holds the most recent 100"`
	Episodes          []AdminInsightsTitleEpisode `json:"episodes" doc:"Series: episodes with a play in the window, in season and episode order; empty for movies"`
	EpisodesTruncated bool                        `json:"episodes_truncated" doc:"More than 500 episodes had a play; episodes holds the first 500"`
}

type AdminInsightsTitleOutput struct {
	Body AdminInsightsTitle
}

func adminInsightsProblem(err error) error {
	if errors.Is(err, handlers.ErrInvalidInsightsQuery) {
		return NewProblem(TypeValidationFailed, err.Error())
	}
	if errors.Is(err, handlers.ErrInsightsTitleNotFound) {
		return NewProblem(TypeNotFound, "Title not found.")
	}
	if errors.Is(err, handlers.ErrAdminDashboardUnavailable) {
		return unavailable("insights")
	}
	return NewProblem(TypeInternalError, "Insights data could not be read.")
}

func parseOptionalPositiveID(raw ID, fieldName string) (int, *Problem) {
	if raw == "" {
		return 0, nil
	}
	n, err := intOfID(raw)
	if err != nil || n <= 0 {
		return 0, NewProblem(TypeValidationFailed, "Invalid "+fieldName+".")
	}
	return n, nil
}

func insightsOp(subpath, id, summary string) Operation {
	return Operation{
		Operation:     humaOp(http.MethodGet, Prefix+"/admin/insights/"+subpath, id, "admin-observability", summary),
		Class:         ClassActingAdmin,
		ServiceBacked: true,
	}
}

func registerAdminInsights(reg *Registry) {
	registerAdminInsightsCapabilities(reg)
	registerAdminInsightsActivity(reg)
	registerAdminInsightsPeople(reg)
	registerAdminInsightsDevices(reg)
	registerAdminInsightsBandwidth(reg)
	registerAdminInsightsTitles(reg)
	registerAdminInsightsTitle(reg)
	registerAdminInsightsLibrary(reg)
}

// insightsWindowQueryOf validates the shared row filters of a v2 Insights read.
func insightsWindowQueryOf(rng, fromDate, toDate, tz string, libraryID, userID ID, profileID string) (handlers.AdminInsightsWindowQuery, *Problem) {
	libID, p := parseOptionalPositiveID(libraryID, "library_id")
	if p != nil {
		return handlers.AdminInsightsWindowQuery{}, p
	}
	uid, p := parseOptionalPositiveID(userID, "user_id")
	if p != nil {
		return handlers.AdminInsightsWindowQuery{}, p
	}
	profileID = strings.TrimSpace(profileID)
	if profileID != "" && uid == 0 {
		return handlers.AdminInsightsWindowQuery{}, NewProblem(TypeValidationFailed, "profile_id requires user_id.")
	}
	return handlers.AdminInsightsWindowQuery{
		Range: rng, FromDate: fromDate, ToDate: toDate, TZ: tz, LibraryID: libID, UserID: uid, ProfileID: profileID,
	}, nil
}

func registerAdminInsightsCapabilities(reg *Registry) {
	capOp := Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/admin/insights/capabilities", "getAdminInsightsCapabilities", "admin-observability", "Discover Monitor -> Insights tabs and archival datasets supported by this server."),
		Class:     ClassActingAdmin,
	}
	Register(reg, capOp, func(_ context.Context, _ *CapabilityInput) (*AdminInsightsCapabilitiesOutput, error) {
		out := new(AdminInsightsCapabilitiesOutput)
		available := reg.deps.AdminInsights != nil
		out.Body.State = configuredCapabilityState(available)
		out.Body.Available = available
		out.Body.NodeEgressDaily = available
		out.Body.Titles = available
		out.Body.ProfileFilter = available
		out.Body.Library = available
		out.Body.Breakdowns = available
		return out, nil
	})
}

func registerAdminInsightsActivity(reg *Registry) {
	Register(reg, insightsOp("activity", "getAdminInsightsActivity", "Read multi-range playback activity, watch time, local-timezone viewing heatmap, and peak concurrent streams."), func(ctx context.Context, in *AdminInsightsWindowInput) (*AdminInsightsActivityOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		q, p := insightsWindowQueryOf(in.Range, in.FromDate, in.ToDate, in.TZ, in.LibraryID, in.UserID, in.ProfileID)
		if p != nil {
			return nil, p
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsActivity(ctx, q, cacheRefresh(ctx, in.Refresh))
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsActivityOutput{Body: adminInsightsActivityOf(res)}, nil
	})
}

func adminInsightsActivityOf(res *handlers.AdminInsightsActivity) AdminInsightsActivity {
	body := AdminInsightsActivity{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ, BucketUnit: res.BucketUnit,
		Coverage: adminInsightsCoverageOf(res.Coverage),
		Summary: AdminInsightsActivitySummary{
			Plays: res.Summary.Plays, Sessions: res.Summary.Sessions, Movies: res.Summary.Movies, Episodes: res.Summary.Episodes,
			OtherMedia: res.Summary.OtherMedia, WatchSeconds: res.Summary.WatchSeconds,
			MeasuredWatchSeconds: res.Summary.MeasuredWatchSeconds,
			CompletedPlays:       res.Summary.CompletedPlays, CompletionRate: res.Summary.CompletionRate,
			DirectPlays: res.Summary.DirectPlays, RemuxPlays: res.Summary.RemuxPlays, TranscodePlays: res.Summary.TranscodePlays,
			UniqueAccounts: res.Summary.UniqueAccounts, UniqueProfiles: res.Summary.UniqueProfiles,
			PeakConcurrentStreams: res.Summary.PeakConcurrentStreams, PeakConcurrentAt: destinationInstant(res.Summary.PeakConcurrentAt),
		},
		Buckets:     make([]AdminInsightsActivityBucket, 0, len(res.Buckets)),
		Heatmap:     make([]AdminInsightsHeatmapCell, 0, len(res.Heatmap)),
		PeakStreams: make([]AdminInsightsPeakStreamsPoint, 0, len(res.PeakStreams)),
		EndReasons:  adminInsightsLabeledCountsOf(res.EndReasons),
	}
	for _, b := range res.Buckets {
		body.Buckets = append(body.Buckets, AdminInsightsActivityBucket{
			BucketStart: NewInstant(b.BucketStart), LabelDate: b.LabelDate, Plays: b.Plays, WatchSeconds: b.WatchSeconds,
			Direct: b.Direct, Remux: b.Remux, Transcode: b.Transcode, Movies: b.Movies, Episodes: b.Episodes, OtherMedia: b.OtherMedia,
			ActiveAccounts: b.ActiveAccounts, ActiveProfiles: b.ActiveProfiles,
		})
	}
	for _, c := range res.Heatmap {
		body.Heatmap = append(body.Heatmap, AdminInsightsHeatmapCell{DayOfWeek: c.DayOfWeek, Hour: c.Hour, Plays: c.Plays, WatchSeconds: c.WatchSeconds})
	}
	for _, pt := range res.PeakStreams {
		body.PeakStreams = append(body.PeakStreams, AdminInsightsPeakStreamsPoint{
			Date: pt.Date, PeakStreams: pt.PeakStreams, PeakDirect: pt.PeakDirect, PeakRemux: pt.PeakRemux, PeakTranscode: pt.PeakTranscode,
			PeakStreamsDirect: pt.PeakStreamsDirect, PeakStreamsRemux: pt.PeakStreamsRemux, PeakStreamsTranscode: pt.PeakStreamsTranscode,
			PeakStreamsAt: destinationInstant(pt.PeakStreamsAt),
		})
	}
	return body
}

func registerAdminInsightsPeople(reg *Registry) {
	Register(reg, insightsOp("people", "getAdminInsightsPeople", "Read account and household profile viewing leaderboards over the requested range."), func(ctx context.Context, in *AdminInsightsPeopleInput) (*AdminInsightsPeopleOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		libID, p := parseOptionalPositiveID(in.LibraryID, "library_id")
		if p != nil {
			return nil, p
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsPeople(ctx, handlers.AdminInsightsPeopleQuery{
			AdminInsightsWindowQuery: handlers.AdminInsightsWindowQuery{
				Range: in.Range, FromDate: in.FromDate, ToDate: in.ToDate, TZ: in.TZ, LibraryID: libID,
			},
			Search: strings.TrimSpace(in.Search), Sort: in.Sort, Dir: in.Dir, Limit: in.Limit, Offset: in.Offset,
		}, cacheRefresh(ctx, in.Refresh))
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsPeopleOutput{Body: adminInsightsPeopleOf(res)}, nil
	})
}

func adminInsightsPeopleOf(res *handlers.AdminInsightsPeople) AdminInsightsPeople {
	body := AdminInsightsPeople{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ,
		TotalAccounts: res.TotalAccounts, ActiveAccounts: res.ActiveAccounts,
		ActiveProfiles: res.ActiveProfiles, LiveAccounts: res.LiveAccounts,
		TotalWatchSeconds: res.TotalWatchSeconds,
		Coverage:          adminInsightsCoverageOf(res.Coverage),
		Accounts:          make([]AdminInsightsPersonAccount, 0, len(res.Accounts)),
		TopProfiles:       make([]AdminInsightsTopProfile, 0, len(res.TopProfiles)),
	}
	for _, p := range res.TopProfiles {
		body.TopProfiles = append(body.TopProfiles, AdminInsightsTopProfile{
			UserID: IDFromInt(int64(p.UserID)), Username: p.Username, ProfileID: ID(p.ProfileID), ProfileName: p.ProfileName,
			Plays: p.Plays, WatchSeconds: p.WatchSeconds,
		})
	}
	for _, acc := range res.Accounts {
		profiles := make([]AdminInsightsPersonProfile, 0, len(acc.Profiles))
		for _, pr := range acc.Profiles {
			profiles = append(profiles, AdminInsightsPersonProfile{
				ProfileID: ID(pr.ProfileID), ProfileName: pr.ProfileName, IsLive: pr.IsLive,
				Plays: pr.Plays, Sessions: pr.Sessions, WatchSeconds: pr.WatchSeconds, MeasuredSessions: pr.MeasuredSessions,
				DirectPlays: pr.DirectPlays, RemuxPlays: pr.RemuxPlays, TranscodePlays: pr.TranscodePlays,
				CompletedPlays: pr.CompletedPlays, CompletionRate: pr.CompletionRate, LastSeenAt: destinationInstant(pr.LastSeenAt),
			})
		}
		body.Accounts = append(body.Accounts, AdminInsightsPersonAccount{
			UserID: IDFromInt(int64(acc.UserID)), Username: acc.Username, Role: acc.Role, IsLive: acc.IsLive,
			Plays: acc.Plays, Sessions: acc.Sessions, Movies: acc.Movies, Episodes: acc.Episodes, WatchSeconds: acc.WatchSeconds,
			MeasuredSessions: acc.MeasuredSessions, MeasuredShare: acc.MeasuredShare,
			DirectPlays: acc.DirectPlays, RemuxPlays: acc.RemuxPlays, TranscodePlays: acc.TranscodePlays,
			DirectRate: acc.DirectRate, CompletedPlays: acc.CompletedPlays, CompletionRate: acc.CompletionRate,
			TopClient: acc.TopClient, LastSeenAt: destinationInstant(acc.LastSeenAt), Profiles: profiles,
		})
	}
	return body
}

func registerAdminInsightsDevices(reg *Registry) {
	Register(reg, insightsOp("devices", "getAdminInsightsDevices", "Read client x source video codec direct-play matrix, adaptation reasons, hardware acceleration, subtitle burn-in, and HDR tone-mapping diagnostics."), func(ctx context.Context, in *AdminInsightsDevicesInput) (*AdminInsightsDevicesOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		q, p := insightsWindowQueryOf(in.Range, in.FromDate, in.ToDate, in.TZ, in.LibraryID, in.UserID, in.ProfileID)
		if p != nil {
			return nil, p
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsDevices(ctx, handlers.AdminInsightsDevicesQuery{
			AdminInsightsWindowQuery: q, MinSessions: in.MinSessions,
		}, cacheRefresh(ctx, in.Refresh))
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsDevicesOutput{Body: adminInsightsDevicesOf(res)}, nil
	})
}

func adminInsightsDevicesOf(res *handlers.AdminInsightsDevices) AdminInsightsDevices {
	body := AdminInsightsDevices{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ,
		Coverage:             adminInsightsCoverageOf(res.Coverage),
		Summary:              AdminInsightsDevicesSummary(res.Summary),
		Codecs:               append([]string{}, res.Codecs...),
		Matrix:               make([]AdminInsightsClientMatrixRow, 0, len(res.Matrix)),
		AdaptationReasons:    make([]AdminInsightsAdaptationReason, 0, len(res.AdaptationReasons)),
		HWAccelBreakdown:     make([]AdminInsightsLabeledCount, 0, len(res.HWAccelBreakdown)),
		ToneMapBreakdown:     make([]AdminInsightsLabeledCount, 0, len(res.ToneMapBreakdown)),
		SubtitleBurnInCodecs: make([]AdminInsightsLabeledCount, 0, len(res.SubtitleBurnInCodecs)),
		SourceResolutions:    adminInsightsLabeledCountsOf(res.SourceResolutions),
		DeliveredResolutions: adminInsightsLabeledCountsOf(res.DeliveredResolutions),
		ResolutionChanges:    make([]AdminInsightsResolutionChange, 0, len(res.ResolutionChanges)),
		TopTranscoders:       make([]AdminInsightsTranscoderRow, 0, len(res.TopTranscoders)),
		BucketUnit:           res.BucketUnit,
		ClientVersions:       make([]AdminInsightsClientVersionRow, 0, len(res.ClientVersions)),
		ProtocolBuckets:      make([]AdminInsightsProtocolBucket, 0, len(res.ProtocolBuckets)),
		Subtitles: AdminInsightsSubtitleUsage{
			Sessions: res.Subtitles.Sessions, SubtitledSessions: res.Subtitles.SubtitledSessions,
			ForcedSessions: res.Subtitles.ForcedSessions, HearingImpairedSessions: res.Subtitles.HearingImpairedSessions,
			Languages:  adminInsightsLabeledCountsOf(res.Subtitles.Languages),
			Deliveries: adminInsightsLabeledCountsOf(res.Subtitles.Deliveries),
		},
		Audio: AdminInsightsAudioUsage{
			Sessions:          res.Audio.Sessions,
			Languages:         adminInsightsLabeledCountsOf(res.Audio.Languages),
			Decisions:         adminInsightsLabeledCountsOf(res.Audio.Decisions),
			DeliveredChannels: adminInsightsLabeledCountsOf(res.Audio.DeliveredChannels),
		},
	}
	for _, v := range res.ClientVersions {
		body.ClientVersions = append(body.ClientVersions, AdminInsightsClientVersionRow{
			ClientName: v.ClientName, DevicePlatform: v.DevicePlatform, ClientVersion: v.ClientVersion,
			IsJellyfinClient: v.IsJellyfinClient, Sessions: v.Sessions, LastSeenAt: destinationInstant(v.LastSeenAt),
		})
	}
	for _, b := range res.ProtocolBuckets {
		body.ProtocolBuckets = append(body.ProtocolBuckets, AdminInsightsProtocolBucket{
			BucketStart: NewInstant(b.BucketStart), LabelDate: b.LabelDate,
			NativeSessions: b.NativeSessions, JellyfinSessions: b.JellyfinSessions,
		})
	}
	for _, r := range res.TopTranscoders {
		body.TopTranscoders = append(body.TopTranscoders, AdminInsightsTranscoderRow(r))
	}
	for _, c := range res.ResolutionChanges {
		body.ResolutionChanges = append(body.ResolutionChanges, AdminInsightsResolutionChange(c))
	}
	for _, row := range res.Matrix {
		cells := make([]AdminInsightsClientCodecCell, 0, len(row.Cells))
		for _, cell := range row.Cells {
			cells = append(cells, AdminInsightsClientCodecCell(cell))
		}
		body.Matrix = append(body.Matrix, AdminInsightsClientMatrixRow{
			ClientName: row.ClientName, DevicePlatform: row.DevicePlatform, IsJellyfinClient: row.IsJellyfinClient,
			TotalSessions: row.TotalSessions, DirectSessions: row.DirectSessions, RemuxSessions: row.RemuxSessions,
			TranscodeSessions: row.TranscodeSessions, DirectRate: row.DirectRate, Cells: cells,
		})
	}
	for _, r := range res.AdaptationReasons {
		body.AdaptationReasons = append(body.AdaptationReasons, AdminInsightsAdaptationReason(r))
	}
	for _, item := range res.HWAccelBreakdown {
		body.HWAccelBreakdown = append(body.HWAccelBreakdown, AdminInsightsLabeledCount(item))
	}
	for _, item := range res.ToneMapBreakdown {
		body.ToneMapBreakdown = append(body.ToneMapBreakdown, AdminInsightsLabeledCount(item))
	}
	for _, item := range res.SubtitleBurnInCodecs {
		body.SubtitleBurnInCodecs = append(body.SubtitleBurnInCodecs, AdminInsightsLabeledCount(item))
	}
	return body
}

func registerAdminInsightsBandwidth(reg *Registry) {
	Register(reg, insightsOp("bandwidth", "getAdminInsightsBandwidth", "Read multi-range server and per-node daily egress volume, peak throughput, and remote vs local session share."), func(ctx context.Context, in *AdminInsightsBandwidthInput) (*AdminInsightsBandwidthOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsBandwidth(ctx, handlers.AdminInsightsBandwidthQuery{
			Range: in.Range, FromDate: in.FromDate, ToDate: in.ToDate, TZ: in.TZ,
		}, cacheRefresh(ctx, in.Refresh))
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsBandwidthOutput{Body: adminInsightsBandwidthOf(res)}, nil
	})
}

func adminInsightsBandwidthOf(res *handlers.AdminInsightsBandwidth) AdminInsightsBandwidth {
	body := AdminInsightsBandwidth{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ,
		OldestMetricAt: destinationInstant(res.OldestMetricAt), OldestRouteFactsAt: destinationInstant(res.OldestRouteFactsAt),
		HasRemoteNodes: res.HasRemoteNodes,
		Summary: AdminInsightsBandwidthSummary{
			TotalEgressBytes: res.Summary.TotalEgressBytes, PlaybackEgressBytes: res.Summary.PlaybackEgressBytes,
			DownloadEgressBytes: res.Summary.DownloadEgressBytes, APIEgressBytes: res.Summary.APIEgressBytes,
			NodeEgressBytes: res.Summary.NodeEgressBytes, AvgDailyBytes: res.Summary.AvgDailyBytes,
			PeakEgressKbps: res.Summary.PeakEgressKbps, PeakEgressAt: destinationInstant(res.Summary.PeakEgressAt),
			PeakDownloadKbps: res.Summary.PeakDownloadKbps, ActiveDays: res.Summary.ActiveDays,
			RemoteSessions: res.Summary.RemoteSessions, LocalSessions: res.Summary.LocalSessions, UnknownLocationSessions: res.Summary.UnknownLocationSessions,
		},
		Sources: make([]AdminInsightsEgressSource, 0, len(res.Sources)),
		Points:  make([]AdminInsightsBandwidthPoint, 0, len(res.Points)),
	}
	for _, src := range res.Sources {
		wire := AdminInsightsEgressSource{
			Key: src.Key, Kind: src.Kind, Name: src.Name, NodeType: src.NodeType,
			EgressBytes: src.EgressBytes, PeakEgressKbps: src.PeakEgressKbps, ShareOfTotal: src.ShareOfTotal,
		}
		if src.Kind == handlers.EgressSourceKindNode && src.NodeID > 0 {
			wire.NodeID = new(IDFromInt(int64(src.NodeID)))
		}
		body.Sources = append(body.Sources, wire)
	}
	for _, pt := range res.Points {
		body.Points = append(body.Points, AdminInsightsBandwidthPoint{
			Date: pt.Date, EgressBytes: pt.EgressBytes, PlaybackEgressBytes: pt.PlaybackEgressBytes,
			DownloadEgressBytes: pt.DownloadEgressBytes, APIEgressBytes: pt.APIEgressBytes,
			PeakEgressKbps: pt.PeakEgressKbps, PeakDownloadKbps: pt.PeakDownloadKbps,
			BytesBySource: egressBySourceOf(pt.BytesBySource), PeakKbpsBySource: egressBySourceOf(pt.PeakKbpsBySource),
		})
	}
	return body
}

func adminInsightsCoverageOf(c handlers.AdminInsightsCoverage) AdminInsightsCoverage {
	return AdminInsightsCoverage{
		OldestHistoryAt:    destinationInstant(c.OldestHistoryAt),
		OldestMeasuredAt:   destinationInstant(c.OldestMeasuredAt),
		OldestRouteFactsAt: destinationInstant(c.OldestRouteFactsAt),
		OldestMetricAt:     destinationInstant(c.OldestMetricAt),
		TotalSessions:      c.TotalSessions,
		MeasuredSessions:   c.MeasuredSessions,
		RouteFactsSessions: c.RouteFactsSessions,
	}
}

func adminInsightsLabeledCountsOf(in []handlers.AdminInsightsLabeledCount) []AdminInsightsLabeledCount {
	out := make([]AdminInsightsLabeledCount, 0, len(in))
	for _, item := range in {
		out = append(out, AdminInsightsLabeledCount(item))
	}
	return out
}

func registerAdminInsightsTitles(reg *Registry) {
	Register(reg, insightsOp("titles", "getAdminInsightsTitles", "Rank the most watched and most popular movies, series and libraries still in the catalog over the requested range."), func(ctx context.Context, in *AdminInsightsTitlesInput) (*AdminInsightsTitlesOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		q, p := insightsWindowQueryOf(in.Range, in.FromDate, in.ToDate, in.TZ, in.LibraryID, in.UserID, in.ProfileID)
		if p != nil {
			return nil, p
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsTitles(ctx, handlers.AdminInsightsTitlesQuery{
			AdminInsightsWindowQuery: q, Sort: in.Sort, Limit: in.Limit,
		}, cacheRefresh(ctx, in.Refresh))
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsTitlesOutput{Body: adminInsightsTitlesOf(res)}, nil
	})
}

func adminInsightsTitleRowsOf(in []handlers.AdminInsightsTitleRow) []AdminInsightsTitleRow {
	out := make([]AdminInsightsTitleRow, 0, len(in))
	for _, r := range in {
		out = append(out, AdminInsightsTitleRow{
			ContentID: r.ContentID, Kind: r.Kind, Title: r.Title, Year: r.Year, Plays: r.Plays, Viewers: r.Viewers,
			CompletedPlays: r.CompletedPlays, Episodes: r.Episodes, WatchSeconds: r.WatchSeconds,
			LastPlayedAt: destinationInstant(r.LastPlayedAt),
		})
	}
	return out
}

func adminInsightsTitlesOf(res *handlers.AdminInsightsTitles) AdminInsightsTitles {
	body := AdminInsightsTitles{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ, Sort: res.Sort,
		Movies: adminInsightsTitleRowsOf(res.Movies), Shows: adminInsightsTitleRowsOf(res.Shows),
		Libraries:     make([]AdminInsightsLibraryRow, 0, len(res.Libraries)),
		LeastFinished: adminInsightsTitleRowsOf(res.LeastFinished),
	}
	for _, lib := range res.Libraries {
		body.Libraries = append(body.Libraries, AdminInsightsLibraryRow{
			LibraryID: IDFromInt(int64(lib.LibraryID)), Name: lib.Name, Plays: lib.Plays, Viewers: lib.Viewers, WatchSeconds: lib.WatchSeconds,
		})
	}
	return body
}

func registerAdminInsightsTitle(reg *Registry) {
	Register(reg, insightsOp("titles/{content_id}", "getAdminInsightsTitle", "Read one movie or series: plays over time, the profiles that played it, and a series' episodes."), func(ctx context.Context, in *AdminInsightsTitleInput) (*AdminInsightsTitleOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		q, p := insightsWindowQueryOf(in.Range, in.FromDate, in.ToDate, in.TZ, in.LibraryID, in.UserID, in.ProfileID)
		if p != nil {
			return nil, p
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsTitle(ctx, handlers.AdminInsightsTitleQuery{
			AdminInsightsWindowQuery: q, ContentID: in.ContentID,
		})
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsTitleOutput{Body: adminInsightsTitleOf(res)}, nil
	})
}

func adminInsightsTitleOf(res *handlers.AdminInsightsTitle) AdminInsightsTitle {
	body := AdminInsightsTitle{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ, BucketUnit: res.BucketUnit,
		ContentID: res.ContentID, Kind: res.Kind, Title: res.Title, Year: res.Year, DurationSeconds: res.DurationSeconds,
		Summary:          AdminInsightsTitleSummary(res.Summary),
		Buckets:          make([]AdminInsightsPlaysBucket, 0, len(res.Buckets)),
		Viewers:          make([]AdminInsightsTitleViewer, 0, len(res.Viewers)),
		Episodes:         make([]AdminInsightsTitleEpisode, 0, len(res.Episodes)),
		ViewersTruncated: res.ViewersTruncated, EpisodesTruncated: res.EpisodesTruncated,
	}
	for _, b := range res.Buckets {
		body.Buckets = append(body.Buckets, AdminInsightsPlaysBucket{BucketStart: NewInstant(b.BucketStart), LabelDate: b.LabelDate, Plays: b.Plays, WatchSeconds: b.WatchSeconds})
	}
	for _, v := range res.Viewers {
		viewer := AdminInsightsTitleViewer{
			UserID: IDFromInt(int64(v.UserID)), Username: v.Username, ProfileID: v.ProfileID, ProfileName: v.ProfileName,
			Plays: v.Plays, CompletedPlays: v.CompletedPlays, WatchSeconds: v.WatchSeconds,
			LastPlayedAt: destinationInstant(v.LastPlayedAt), FurthestPositionSeconds: v.FurthestPositionSeconds,
		}
		if v.LastEpisode != nil {
			viewer.LastEpisode = &AdminInsightsEpisodeRef{
				ContentID: v.LastEpisode.ContentID, SeasonNumber: v.LastEpisode.SeasonNumber,
				EpisodeNumber: v.LastEpisode.EpisodeNumber, Title: v.LastEpisode.Title,
			}
		}
		body.Viewers = append(body.Viewers, viewer)
	}
	for _, e := range res.Episodes {
		body.Episodes = append(body.Episodes, AdminInsightsTitleEpisode{
			ContentID: e.ContentID, SeasonNumber: e.SeasonNumber, EpisodeNumber: e.EpisodeNumber, Title: e.Title,
			Plays: e.Plays, Viewers: e.Viewers, WatchSeconds: e.WatchSeconds,
		})
	}
	return body
}

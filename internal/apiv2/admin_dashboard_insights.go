package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminDashboardInsightsService interface {
	ReadAdminTimeseries(context.Context, int, bool) (*handlers.AdminTimeseries, error)
	ReadAdminPlaybackActivity(context.Context, int, bool) (*handlers.AdminPlaybackActivity, error)
	ReadAdminTopActivity(context.Context, int, int, bool) (*handlers.AdminTopActivity, error)
	ReadAdminDownloadsStats(context.Context, int, bool) (*handlers.AdminDownloadsStats, error)
}
type AdminDashboardWindowInput struct {
	Hours   int  `query:"hours" minimum:"1" maximum:"744" default:"24"`
	Refresh bool `query:"refresh" default:"false" doc:"Invalidate the cached aggregate before reading."`
}
type AdminDashboardTopInput struct {
	Days    int  `query:"days" minimum:"1" maximum:"30" default:"7"`
	Limit   int  `query:"limit" minimum:"1" maximum:"25" default:"10"`
	Refresh bool `query:"refresh" default:"false"`
}
type AdminDashboardDownloadsInput struct {
	Limit   int  `query:"limit" minimum:"1" maximum:"25" default:"10"`
	Refresh bool `query:"refresh" default:"false"`
}
type AdminDashboardTimeseriesPoint struct {
	T                  Instant `json:"t"`
	Streams            int64   `json:"streams"`
	Direct             int64   `json:"direct"`
	Remux              int64   `json:"remux"`
	Transcode          int64   `json:"transcode"`
	EgressKbps         int64   `json:"egress_kbps"`
	DownloadEgressKbps int64   `json:"download_egress_kbps"`
}
type AdminDashboardTimeseries struct {
	ResolutionSeconds int                             `json:"resolution_seconds"`
	From              Instant                         `json:"from"`
	To                Instant                         `json:"to"`
	OldestSampleAt    NullableInstant                 `json:"oldest_sample_at"`
	Points            []AdminDashboardTimeseriesPoint `json:"points"`
}
type AdminDashboardTimeseriesOutput struct{ Body AdminDashboardTimeseries }
type AdminDashboardPlaybackBucket struct {
	Hour      Instant `json:"hour"`
	Direct    int64   `json:"direct"`
	Remux     int64   `json:"remux"`
	Transcode int64   `json:"transcode"`
}
type AdminDashboardPlaybackActivity struct {
	Hours             int                            `json:"hours"`
	BucketSeconds     int                            `json:"bucket_seconds"`
	From              Instant                        `json:"from"`
	To                Instant                        `json:"to"`
	Buckets           []AdminDashboardPlaybackBucket `json:"buckets"`
	Reliability       AdminPlaybackReliability       `json:"reliability"`
	ProfilesActive24h int64                          `json:"profiles_active_24h"`
}
type AdminDashboardPlaybackActivityOutput struct {
	Body AdminDashboardPlaybackActivity
}
type AdminDashboardTopProfile struct {
	UserID       ID     `json:"user_id"`
	Username     string `json:"username"`
	ProfileID    ID     `json:"profile_id"`
	ProfileName  string `json:"profile_name"`
	Plays        int64  `json:"plays"`
	TotalSeconds int64  `json:"total_seconds"`
}
type AdminDashboardTopActivity struct {
	Days     int                        `json:"days"`
	Limit    int                        `json:"limit"`
	Titles   []AdminTopTitle            `json:"titles"`
	Profiles []AdminDashboardTopProfile `json:"profiles"`
}
type AdminDashboardTopActivityOutput struct{ Body AdminDashboardTopActivity }
type AdminDashboardDownloadsUser struct {
	UserID     ID     `json:"user_id"`
	Username   string `json:"username"`
	Downloads  int64  `json:"downloads"`
	TotalBytes int64  `json:"total_bytes"`
}
type AdminDashboardDownloadsStats struct {
	UsersWithDownloads    int64                         `json:"users_with_downloads"`
	ActiveDownloads       int64                         `json:"active_downloads"`
	TotalBytes            int64                         `json:"total_bytes"`
	DownloadsStarted24h   int64                         `json:"downloads_started_24h"`
	DownloadsCompleted24h int64                         `json:"downloads_completed_24h"`
	Limit                 int                           `json:"limit"`
	TopUsers              []AdminDashboardDownloadsUser `json:"top_users"`
}
type AdminDashboardDownloadsOutput struct{ Body AdminDashboardDownloadsStats }

func adminDashboardInsightProblem(err error) error {
	if errors.Is(err, handlers.ErrAdminDashboardUnavailable) {
		return unavailable("dashboard statistics")
	}
	return NewProblem(TypeInternalError, "Dashboard statistics could not be read")
}
func registerAdminDashboardInsights(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/stats/"+path, id, "admin-observability", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	Register(reg, op("timeseries", "getAdminDashboardTimeseries", "Read sampled stream counts and egress with the existing database window and bucket resolution."), func(ctx context.Context, in *AdminDashboardWindowInput) (*AdminDashboardTimeseriesOutput, error) {
		if reg.deps.AdminDashboardInsights == nil {
			return nil, unavailable("dashboard statistics")
		}
		s, err := reg.deps.AdminDashboardInsights.ReadAdminTimeseries(ctx, in.Hours, in.Refresh)
		if err != nil || s == nil {
			return nil, adminDashboardInsightProblem(err)
		}
		body := AdminDashboardTimeseries{ResolutionSeconds: s.ResolutionSeconds, From: NewInstant(s.From), To: NewInstant(s.To), Points: make([]AdminDashboardTimeseriesPoint, 0, len(s.Points))}
		if s.OldestSampleAt != nil {
			body.OldestSampleAt = NullableInstant{Valid: true, Time: NewInstant(*s.OldestSampleAt)}
		}
		for _, p := range s.Points {
			body.Points = append(body.Points, AdminDashboardTimeseriesPoint{T: NewInstant(p.T), Streams: p.Streams, Direct: p.Direct, Remux: p.Remux, Transcode: p.Transcode, EgressKbps: p.EgressKbps, DownloadEgressKbps: p.DownloadEgressKbps})
		}
		return &AdminDashboardTimeseriesOutput{Body: body}, nil
	})
	Register(reg, op("playback-activity", "getAdminDashboardPlaybackActivity", "Read playback method buckets, finalized-session reliability, and rolling active profiles."), func(ctx context.Context, in *AdminDashboardWindowInput) (*AdminDashboardPlaybackActivityOutput, error) {
		if reg.deps.AdminDashboardInsights == nil {
			return nil, unavailable("dashboard statistics")
		}
		s, err := reg.deps.AdminDashboardInsights.ReadAdminPlaybackActivity(ctx, in.Hours, in.Refresh)
		if err != nil || s == nil {
			return nil, adminDashboardInsightProblem(err)
		}
		body := AdminDashboardPlaybackActivity{Hours: s.Hours, BucketSeconds: s.BucketSeconds, From: NewInstant(s.From), To: NewInstant(s.To), Reliability: AdminPlaybackReliability(s.Reliability), ProfilesActive24h: s.ProfilesActive24h, Buckets: make([]AdminDashboardPlaybackBucket, 0, len(s.Buckets))}
		for _, b := range s.Buckets {
			body.Buckets = append(body.Buckets, AdminDashboardPlaybackBucket{Hour: NewInstant(b.Hour), Direct: b.Direct, Remux: b.Remux, Transcode: b.Transcode})
		}
		return &AdminDashboardPlaybackActivityOutput{Body: body}, nil
	})
	Register(reg, op("top-activity", "getAdminDashboardTopActivity", "Read bounded title and household-profile leaderboards from existing watch history aggregates."), func(ctx context.Context, in *AdminDashboardTopInput) (*AdminDashboardTopActivityOutput, error) {
		if reg.deps.AdminDashboardInsights == nil {
			return nil, unavailable("dashboard statistics")
		}
		s, err := reg.deps.AdminDashboardInsights.ReadAdminTopActivity(ctx, in.Days, in.Limit, in.Refresh)
		if err != nil || s == nil {
			return nil, adminDashboardInsightProblem(err)
		}
		body := AdminDashboardTopActivity{Days: s.Days, Limit: s.Limit, Titles: make([]AdminTopTitle, 0, len(s.Titles)), Profiles: make([]AdminDashboardTopProfile, 0, len(s.Profiles))}
		for _, title := range s.Titles {
			body.Titles = append(body.Titles, AdminTopTitle(title))
		}
		for _, p := range s.Profiles {
			body.Profiles = append(body.Profiles, AdminDashboardTopProfile{UserID: IDFromInt(int64(p.UserID)), Username: p.Username, ProfileID: ID(p.ProfileID), ProfileName: p.ProfileName, Plays: p.Plays, TotalSeconds: p.TotalSeconds})
		}
		return &AdminDashboardTopActivityOutput{Body: body}, nil
	})
	Register(reg, op("downloads", "getAdminDashboardDownloadsStats", "Read account-level managed-download counts and bounded top users, retaining existing lifecycle definitions."), func(ctx context.Context, in *AdminDashboardDownloadsInput) (*AdminDashboardDownloadsOutput, error) {
		if reg.deps.AdminDashboardInsights == nil {
			return nil, unavailable("dashboard statistics")
		}
		s, err := reg.deps.AdminDashboardInsights.ReadAdminDownloadsStats(ctx, in.Limit, in.Refresh)
		if err != nil || s == nil {
			return nil, adminDashboardInsightProblem(err)
		}
		body := AdminDashboardDownloadsStats{UsersWithDownloads: s.UsersWithDownloads, ActiveDownloads: s.ActiveDownloads, TotalBytes: s.TotalBytes, DownloadsStarted24h: s.DownloadsStarted24h, DownloadsCompleted24h: s.DownloadsCompleted24h, Limit: s.Limit, TopUsers: make([]AdminDashboardDownloadsUser, 0, len(s.TopUsers))}
		for _, u := range s.TopUsers {
			body.TopUsers = append(body.TopUsers, AdminDashboardDownloadsUser{UserID: IDFromInt(int64(u.UserID)), Username: u.Username, Downloads: u.Downloads, TotalBytes: u.TotalBytes})
		}
		return &AdminDashboardDownloadsOutput{Body: body}, nil
	})
}

// AdminPlaybackReliability is the native transport projection, independent of handler views.
type AdminPlaybackReliability struct {
	SessionsStarted   int64   `json:"sessions_started"`
	TranscodeStarts   int64   `json:"transcode_starts"`
	FinalizedSessions int64   `json:"finalized_sessions"`
	CompletedSessions int64   `json:"completed_sessions"`
	CompletionRate    float64 `json:"completion_rate"`
	UniqueProfiles    int64   `json:"unique_profiles"`
}

// AdminTopTitle is the native transport projection, independent of handler views.
type AdminTopTitle struct {
	MediaItemID string `json:"media_item_id"`
	Title       string `json:"title"`
	MediaType   string `json:"media_type"`
	Plays       int64  `json:"plays"`
	// TotalSeconds is watched time summed from finalized playback sessions, not
	// the runtime of the titles played. A title that was only ever marked
	// watched has no sessions and reports 0.
	TotalSeconds int64 `json:"total_seconds"`
}

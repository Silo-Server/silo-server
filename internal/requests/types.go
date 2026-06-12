package requests

import (
	"time"
)

type MediaType string

const (
	MediaTypeMovie  MediaType = "movie"
	MediaTypeSeries MediaType = "series"
	MediaTypeAll    MediaType = "all"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusApproved    Status = "approved"
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusCompleted   Status = "completed"
)

const StatusFailed Status = "failed" // target-only status; requests use outcome=failed

type Outcome string

const (
	OutcomeActive    Outcome = "active"
	OutcomeDeclined  Outcome = "declined"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeFailed    Outcome = "failed"
)

type Quality string

const (
	Quality1080p Quality = "1080p"
	Quality2160p Quality = "2160p"
)

// Target is one fulfillment of a request against a single instance at a single
// quality. A request fans out to one Target per resolved quality.
type Target struct {
	ID              int64     `json:"id"`
	RequestID       string    `json:"request_id"`
	IntegrationID   string    `json:"integration_id,omitempty"`
	IntegrationKind string    `json:"integration_kind,omitempty"`
	InstanceName    string    `json:"instance_name,omitempty"`
	Quality         Quality   `json:"quality"`
	IsAnime         bool      `json:"is_anime"`
	ExternalID      string    `json:"external_id,omitempty"`
	ExternalStatus  string    `json:"external_status,omitempty"`
	Status          Status    `json:"status"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Availability string

const (
	AvailabilityMissing   Availability = "missing"
	AvailabilityAvailable Availability = "available"
)

type LimitMode string

const (
	LimitModeInherit   LimitMode = "inherit"
	LimitModeCustom    LimitMode = "custom"
	LimitModeUnlimited LimitMode = "unlimited"
	LimitModeBlocked   LimitMode = "blocked"
)

type ApprovalMode string

const (
	ApprovalModeInherit ApprovalMode = "inherit"
	ApprovalModeManual  ApprovalMode = "manual"
	ApprovalModeAuto    ApprovalMode = "auto"
	ApprovalModeBlocked ApprovalMode = "blocked"
)

type Viewer struct {
	UserID    int
	ProfileID string
	IsAdmin   bool
}

type Settings struct {
	RequestsEnabled           bool      `json:"requests_enabled"`
	GlobalMaxRequests         int       `json:"global_max_requests"`
	GlobalWindowDays          int       `json:"global_window_days"`
	GlobalAutoApprovalEnabled bool      `json:"global_auto_approval_enabled"`
	ForceDualQuality          bool      `json:"force_dual_quality"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type FeatureStatus struct {
	RequestsEnabled bool `json:"requests_enabled"`
}

type UserLimit struct {
	UserID       int          `json:"user_id"`
	LimitMode    LimitMode    `json:"limit_mode"`
	MaxRequests  *int         `json:"max_requests,omitempty"`
	WindowDays   *int         `json:"window_days,omitempty"`
	ApprovalMode ApprovalMode `json:"approval_mode"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type EffectivePolicy struct {
	RequestsEnabled bool
	MaxRequests     int
	WindowDays      int
	Unlimited       bool
	Blocked         bool
	AutoApprove     bool
	Used            int
	Remaining       int
	WindowStart     time.Time
}

type Request struct {
	ID                   string    `json:"id"`
	Provider             string    `json:"provider"`
	MediaType            MediaType `json:"media_type"`
	TMDBID               int       `json:"tmdb_id"`
	TVDBID               *int      `json:"tvdb_id,omitempty"`
	IMDbID               string    `json:"imdb_id,omitempty"`
	Title                string    `json:"title"`
	Year                 *int      `json:"year,omitempty"`
	Overview             string    `json:"overview,omitempty"`
	PosterPath           string    `json:"poster_path,omitempty"`
	BackdropPath         string    `json:"backdrop_path,omitempty"`
	Status               Status    `json:"status"`
	Outcome              Outcome   `json:"outcome"`
	RequestedByUserID    int       `json:"requested_by_user_id,omitempty"`
	RequestedByProfileID string    `json:"requested_by_profile_id,omitempty"`
	RequesterEmail       string    `json:"-"`
	RequesterUsername    string    `json:"-"`
	// DeclineReason is the admin's decline message, populated transiently for
	// the lifecycle notifier (the durable copy lives in the request event
	// record, not on this row).
	DeclineReason    string     `json:"-"`
	IntegrationKind  string     `json:"integration_kind,omitempty"`
	IsAnime          bool       `json:"is_anime"`
	Targets          []Target   `json:"targets,omitempty"`
	ExternalID       string     `json:"external_id,omitempty"`
	ExternalStatus   string     `json:"external_status,omitempty"`
	LibraryContentID string     `json:"library_content_id,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}

type RequestEvent struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	EventType      string    `json:"event_type"`
	ActorUserID    *int      `json:"actor_user_id,omitempty"`
	ActorProfileID string    `json:"actor_profile_id,omitempty"`
	Message        string    `json:"message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type RequestState struct {
	Status      Status `json:"status,omitempty"`
	Requestable bool   `json:"requestable"`
	Reason      string `json:"reason,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}

type MediaResult struct {
	MediaType        MediaType    `json:"media_type"`
	TMDBID           int          `json:"tmdb_id"`
	Title            string       `json:"title"`
	Year             int          `json:"year,omitempty"`
	Overview         string       `json:"overview,omitempty"`
	PosterPath       string       `json:"poster_path,omitempty"`
	BackdropPath     string       `json:"backdrop_path,omitempty"`
	ReleaseDate      string       `json:"release_date,omitempty"`
	Popularity       float64      `json:"popularity,omitempty"`
	VoteAverage      float64      `json:"vote_average,omitempty"`
	Availability     Availability `json:"availability"`
	LibraryContentID string       `json:"library_content_id,omitempty"`
	Request          RequestState `json:"request"`
}

type MediaPage struct {
	Page         int           `json:"page"`
	TotalPages   int           `json:"total_pages"`
	TotalResults int           `json:"total_results"`
	Results      []MediaResult `json:"results"`
}

type MediaCastMember struct {
	Name        string `json:"name"`
	Character   string `json:"character,omitempty"`
	ProfilePath string `json:"profile_path,omitempty"`
	Order       int    `json:"order"`
}

type MediaDetail struct {
	MediaType           MediaType         `json:"media_type"`
	TMDBID              int               `json:"tmdb_id"`
	IMDbID              string            `json:"imdb_id,omitempty"`
	TVDBID              *int              `json:"tvdb_id,omitempty"`
	Title               string            `json:"title"`
	OriginalTitle       string            `json:"original_title,omitempty"`
	Tagline             string            `json:"tagline,omitempty"`
	Overview            string            `json:"overview,omitempty"`
	PosterPath          string            `json:"poster_path,omitempty"`
	BackdropPath        string            `json:"backdrop_path,omitempty"`
	ReleaseDate         string            `json:"release_date,omitempty"`
	Year                int               `json:"year,omitempty"`
	Runtime             int               `json:"runtime,omitempty"`
	Genres              []string          `json:"genres,omitempty"`
	VoteAverage         float64           `json:"vote_average,omitempty"`
	VoteCount           int               `json:"vote_count,omitempty"`
	Status              string            `json:"status,omitempty"`
	Homepage            string            `json:"homepage,omitempty"`
	ContentRating       string            `json:"content_rating,omitempty"`
	ProductionCompanies []string          `json:"production_companies,omitempty"`
	NumberOfSeasons     int               `json:"number_of_seasons,omitempty"`
	NumberOfEpisodes    int               `json:"number_of_episodes,omitempty"`
	FirstAirDate        string            `json:"first_air_date,omitempty"`
	LastAirDate         string            `json:"last_air_date,omitempty"`
	Networks            []string          `json:"networks,omitempty"`
	Cast                []MediaCastMember `json:"cast,omitempty"`
	Director            string            `json:"director,omitempty"`
	Creators            []string          `json:"creators,omitempty"`
	Recommendations     []MediaResult     `json:"recommendations,omitempty"`
	Availability        Availability      `json:"availability"`
	LibraryContentID    string            `json:"library_content_id,omitempty"`
	Request             RequestState      `json:"request"`
}

type CreateRequestInput struct {
	MediaType    MediaType `json:"media_type"`
	TMDBID       int       `json:"tmdb_id"`
	TVDBID       *int      `json:"tvdb_id,omitempty"`
	IMDbID       string    `json:"imdb_id,omitempty"`
	Title        string    `json:"title"`
	Year         *int      `json:"year,omitempty"`
	Overview     string    `json:"overview,omitempty"`
	PosterPath   string    `json:"poster_path,omitempty"`
	BackdropPath string    `json:"backdrop_path,omitempty"`
}

type ListFilter struct {
	Status  Status
	Outcome Outcome
	Limit   int
	Offset  int
}

type Integration struct {
	ID                  string         `json:"id"`
	Name                string         `json:"name"`
	Enabled             bool           `json:"enabled"`
	BaseURL             string         `json:"base_url"`
	APIKeyRef           string         `json:"api_key_ref,omitempty"`
	LastCheckAt         *time.Time     `json:"last_check_at,omitempty"`
	LastCheckStatus     string         `json:"last_check_status,omitempty"`
	LastCheckError      string         `json:"last_check_error,omitempty"`
	UpdatedAt           time.Time      `json:"updated_at"`
	CapabilityID        string         `json:"capability_id"`
	InstallationID      *int           `json:"installation_id,omitempty"`
	SupportedMediaTypes []string       `json:"supported_media_types"`
	PluginConfig        map[string]any `json:"plugin_config"`
}

type FulfillmentResult struct {
	IntegrationKind string
	ExternalID      string
	ExternalStatus  string
}

type FulfillmentStatus struct {
	Status          Status
	Outcome         Outcome
	IntegrationKind string
	ExternalID      string
	ExternalStatus  string
	Message         string
}

type ReconcileResult struct {
	Checked     int `json:"checked"`
	Submitted   int `json:"submitted"`
	Downloading int `json:"downloading"`
	Completed   int `json:"completed"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
	Errors      int `json:"errors"`
}

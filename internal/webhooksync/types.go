package webhooksync

import "time"

const (
	ProviderPlex     = "plex"
	ProviderEmby     = "emby"
	ProviderJellyfin = "jellyfin"
)

const webhookSyncPathPrefix = "/api/v1/webhook-sync/webhooks/"

type Connection struct {
	ID                        string     `json:"id"`
	UserID                    int        `json:"-"`
	Provider                  string     `json:"provider"`
	ServerID                  string     `json:"server_id"`
	ServerName                string     `json:"server_name"`
	DefaultProfileID          string     `json:"default_profile_id"`
	WebhookURL                string     `json:"webhook_url,omitempty"`
	BaseURL                   string     `json:"-"`
	AccessToken               string     `json:"-"`
	WebhookSecret             string     `json:"-"`
	AccountDiscoveryAvailable bool       `json:"account_discovery_available"`
	UserCount                 int        `json:"user_count"`
	LastWebhookReceivedAt     *time.Time `json:"last_webhook_received_at,omitempty"`
	LastWebhookErrorAt        *time.Time `json:"last_webhook_error_at,omitempty"`
	LastWebhookErrorMessage   string     `json:"last_webhook_error_message,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

type ProfileMapping struct {
	ID               int       `json:"id"`
	ConnectionID     string    `json:"connection_id"`
	ExternalUserID   string    `json:"external_user_id"`
	ExternalUserName string    `json:"external_user_name"`
	SiloProfileID    *string   `json:"silo_profile_id,omitempty"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type DiscoveredUser struct {
	ExternalUserID   string `json:"external_user_id"`
	ExternalUserName string `json:"external_user_name"`
}

type ItemState struct {
	ConnectionID       string
	ExternalUserID     string
	ExternalItemID     string
	MediaItemID        string
	LastEventAt        time.Time
	LastCompleted      bool
	LastPositionSecond float64
	UpdatedAt          time.Time
}

type CreateConnectionInput struct {
	Provider         string `json:"provider"`
	ServerID         string `json:"server_id"`
	ServerName       string `json:"server_name"`
	BaseURL          string `json:"base_url,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	DefaultProfileID string `json:"default_profile_id"`
}

type UpdateConnectionInput struct {
	ServerName       *string `json:"server_name,omitempty"`
	DefaultProfileID *string `json:"default_profile_id,omitempty"`
}

type CreateConnectionResult struct {
	Connection Connection `json:"connection"`
	WebhookURL string     `json:"webhook_url"`
}

type RotateWebhookResult struct {
	WebhookURL string `json:"webhook_url"`
}

type ProfileMappingsResponse struct {
	Mappings                  []ProfileMapping `json:"mappings"`
	DiscoveredUsers           []DiscoveredUser `json:"discovered_users"`
	AccountDiscoveryAvailable bool             `json:"account_discovery_available"`
}

type UpdateProfileMappingsInput struct {
	Mappings []UpdateProfileMapping `json:"mappings"`
}

type UpdateProfileMapping struct {
	ExternalUserID   string  `json:"external_user_id"`
	ExternalUserName string  `json:"external_user_name"`
	SiloProfileID    *string `json:"silo_profile_id"`
}

type CanonicalEvent struct {
	Provider        string
	ServerName      string
	OccurredAt      time.Time
	Action          string
	EventKind       string
	UserID          string
	UserName        string
	ExternalItemID  string
	MediaKind       string
	Completed       bool
	PositionSeconds float64
	DurationSeconds float64
	Record          CanonicalRecord
	Summary         string
	Apply           bool
}

const (
	ActionImportProgress = "import_progress"
	ActionMarkUnplayed   = "mark_unplayed"
	ActionAddFavorite    = "add_favorite"
	ActionRemoveFavorite = "remove_favorite"
	ActionToggleFavorite = "toggle_favorite"
)

const (
	OutcomeApplied   = "applied"
	OutcomeIgnored   = "ignored"
	OutcomeUnmatched = "unmatched"
	OutcomeSkipped   = "skipped"
	OutcomeRejected  = "rejected"
	OutcomeError     = "error"
)

type CanonicalRecord struct {
	ExternalID      string
	Kind            string
	Title           string
	Year            int
	IMDbID          string
	TMDBID          string
	TVDBID          string
	SeriesTitle     string
	SeriesYear      int
	SeriesIMDbID    string
	SeriesTMDBID    string
	SeriesTVDBID    string
	SeasonNumber    int
	EpisodeNumber   int
	Played          bool
	LastPlayedAt    *time.Time
	PositionSeconds float64
	DurationSeconds float64
	UpdatedAt       time.Time
}

type ProcessWebhookResult struct {
	ConnectionID          string
	Provider              string
	EventKind             string
	Action                string
	UserID                string
	UserName              string
	ExternalItemID        string
	MediaKind             string
	MatchedMediaItemID    string
	MatchedMediaItemTitle string
	ProfileID             string
	Outcome               string
	Summary               string
	ErrorMessage          string
}

type WebhookRequestLogContext struct {
	RequestID   string
	ClientIP    string
	ContentType string
	UserAgent   string
	PathPattern string
	BodyExcerpt string
}

type WebhookEventLog struct {
	ID           int64          `json:"id"`
	ConnectionID string         `json:"connection_id,omitempty"`
	ReceivedAt   time.Time      `json:"received_at"`
	RequestID    string         `json:"request_id,omitempty"`
	HTTPStatus   int            `json:"http_status"`
	Outcome      string         `json:"outcome"`
	Summary      string         `json:"summary"`
	ErrorMessage string         `json:"error_message,omitempty"`
	BodyExcerpt  string         `json:"body_excerpt,omitempty"`
	Attrs        map[string]any `json:"attrs,omitempty"`
}

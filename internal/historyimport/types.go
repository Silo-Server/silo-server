package historyimport

import "time"

const (
	SourceTypeEmby     = "emby"
	SourceTypeJellyfin = "jellyfin"
	SourceTypePlex     = "plex"

	ConnectionModeConnect    = "connect"
	ConnectionModeCustom     = "custom"
	ConnectionModePredefined = "predefined"
	ConnectionModePlexOAuth  = "plex_oauth"
	ConnectionModeAdminToken = "admin_token"

	RunStatusQueued    = "queued"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"

	KindMovie   = "movie"
	KindSeries  = "series"
	KindEpisode = "episode"
)

const (
	connectSessionTTL      = 30 * time.Minute
	maxStoredWarnings      = 20
	maxUnmatchedSamples    = 10
	maxUnmatchedLogSamples = 40
	connectApplicationName = "Silo/1.0.0"
)

// Source is an admin-configured external media server used as an import source.
type Source struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	SourceType    string    `json:"source_type"`
	BaseURL       string    `json:"base_url,omitempty"`
	SystemID      string    `json:"system_id,omitempty"`
	Enabled       bool      `json:"enabled"`
	SortOrder     int       `json:"sort_order"`
	HasAdminToken bool      `json:"has_admin_token"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ConnectServer struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SystemID     string `json:"system_id,omitempty"`
	URL          string `json:"url,omitempty"`
	LocalAddress string `json:"local_address,omitempty"`
	AccessKey    string `json:"access_key,omitempty"`
	HasRemoteURL bool   `json:"has_remote_url"`
	HasLocalURL  bool   `json:"has_local_address"`
}

type ConnectServerResponse struct {
	ServerID        string `json:"server_id"`
	Name            string `json:"name"`
	SystemID        string `json:"system_id,omitempty"`
	HasRemoteURL    bool   `json:"has_remote_url"`
	HasLocalAddress bool   `json:"has_local_address"`
}

type ConnectSession struct {
	ID                 string          `json:"connect_session_id"`
	UserID             int             `json:"-"`
	ConnectUserID      string          `json:"-"`
	ConnectAccessToken string          `json:"-"`
	Servers            []ConnectServer `json:"servers"`
	ExpiresAt          time.Time       `json:"expires_at"`
	ConsumedAt         *time.Time      `json:"consumed_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type ConnectSessionLoginResult struct {
	ConnectSessionID string                  `json:"connect_session_id"`
	Servers          []ConnectServerResponse `json:"servers"`
	ExpiresAt        time.Time               `json:"expires_at"`
}

type PlexServer struct {
	Name             string `json:"name"`
	ClientIdentifier string `json:"client_identifier"`
	AccessToken      string `json:"access_token"`
	RemoteURL        string `json:"remote_url"`
	LocalURL         string `json:"local_url"`
	Owned            bool   `json:"owned"`
	HasRemoteURL     bool   `json:"has_remote_url"`
	HasLocalURL      bool   `json:"has_local_url"`
}

type PlexSession struct {
	ID         string       `json:"plex_session_id"`
	UserID     int          `json:"-"`
	PinID      string       `json:"-"`
	PinCode    string       `json:"-"`
	AuthToken  string       `json:"-"`
	Servers    []PlexServer `json:"servers,omitempty"`
	ExpiresAt  time.Time    `json:"expires_at"`
	ConsumedAt *time.Time   `json:"consumed_at,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

type PlexPinResponse struct {
	SessionID string    `json:"session_id"`
	PinCode   string    `json:"pin_code"`
	AuthURL   string    `json:"auth_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PlexCheckRequest struct {
	SessionID string `json:"session_id"`
}

type PlexCheckResponse struct {
	Authenticated bool               `json:"authenticated"`
	Servers       []PlexServerPublic `json:"servers,omitempty"`
}

type PlexServerPublic struct {
	Name             string `json:"name"`
	ClientIdentifier string `json:"client_identifier"`
	Owned            bool   `json:"owned"`
	HasRemoteURL     bool   `json:"has_remote_url"`
	HasLocalURL      bool   `json:"has_local_url"`
}

// Run represents one history import execution.
type Run struct {
	ID               string            `json:"id"`
	UserID           int               `json:"user_id"`
	ProfileID        string            `json:"profile_id"`
	SourceType       string            `json:"source_type"`
	ConnectionMode   string            `json:"connection_mode"`
	Status           string            `json:"status"`
	MappingID        *int              `json:"mapping_id,omitempty"`
	Fetched          int               `json:"fetched"`
	Matched          int               `json:"matched"`
	Unmatched        int               `json:"unmatched"`
	ProgressUpdated  int               `json:"progress_updated"`
	HistoryCreated   int               `json:"history_created"`
	WatchlistAdded   int               `json:"watchlist_added"`
	Skipped          int               `json:"skipped"`
	Warnings         []string          `json:"warnings"`
	UnmatchedSamples []UnmatchedSample `json:"unmatched_samples"`
	ErrorMessage     string            `json:"error_message,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	CompletedAt      *time.Time        `json:"completed_at,omitempty"`
}

type UnmatchedSample struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
	Reason string `json:"reason"`
}

type Record struct {
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
	PlayCount       int
	LastPlayedAt    *time.Time
	PositionSeconds float64
	DurationSeconds float64
	// Watchlisted marks an entry from the source account's watchlist: the
	// matched item is added to the importing profile's watchlist instead of
	// receiving watch state.
	Watchlisted bool
	UpdatedAt   time.Time
}

type Match struct {
	MediaItemID string
	Kind        string
	Title       string
	Year        int
}

type CreateRunInput struct {
	ProfileID        string `json:"profile_id"`
	Source           string `json:"source"`
	ConnectSessionID string `json:"connect_session_id,omitempty"`
	ServerID         string `json:"server_id,omitempty"`
	SourceID         int    `json:"source_id,omitempty"`
	ServerURL        string `json:"server_url,omitempty"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"`
	JellyfinBaseURL  string `json:"jellyfin_base_url,omitempty"`
	JellyfinUsername string `json:"jellyfin_username,omitempty"`
	JellyfinPassword string `json:"jellyfin_password,omitempty"`
	PlexSessionID    string `json:"plex_session_id,omitempty"`
	PlexServerID     string `json:"plex_server_id,omitempty"`
	PlexBaseURL      string `json:"plex_base_url,omitempty"`
	PlexToken        string `json:"plex_token,omitempty"`
}

type LoginConnectInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type CreateSourceInput struct {
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
	BaseURL    string `json:"base_url"`
	SystemID   string `json:"system_id,omitempty"`
	Enabled    bool   `json:"enabled"`
	SortOrder  int    `json:"sort_order"`
	AdminToken string `json:"admin_token,omitempty"`
}

type UpdateSourceInput struct {
	Name       *string `json:"name,omitempty"`
	BaseURL    *string `json:"base_url,omitempty"`
	SystemID   *string `json:"system_id,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
	SortOrder  *int    `json:"sort_order,omitempty"`
	AdminToken *string `json:"admin_token,omitempty"`
}

type ExecutionSummary struct {
	Fetched               int
	Matched               int
	Unmatched             int
	ProgressUpdated       int
	HistoryCreated        int
	WatchlistAdded        int
	Skipped               int
	Warnings              []string
	UnmatchedSamples      []UnmatchedSample
	UnmatchedReasonCounts map[string]int
}

type localProgressRow struct {
	UpdatedAt time.Time
}

type plexAuth struct {
	BaseURL string
	Token   string
	// AccountToken is the plex.tv account token (PIN/OAuth session token or
	// the user-supplied token), used for account-level fetches such as the
	// watchlist. May equal Token for manual-token imports.
	AccountToken string
}

// --- Admin types ---

// ExternalUser is a user account on an external media server.
type ExternalUser struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Home       bool   `json:"home,omitempty"`
	Guest      bool   `json:"guest,omitempty"`
	Restricted bool   `json:"restricted,omitempty"`
}

// UserMapping persists the link from one external server user to a Silo user + profile.
type UserMapping struct {
	ID               int        `json:"id"`
	SourceID         int        `json:"source_id"`
	ExternalUserID   string     `json:"external_user_id"`
	ExternalUserName string     `json:"external_user_name"`
	SiloUserID       int        `json:"silo_user_id"`
	SiloProfileID    string     `json:"silo_profile_id"`
	SiloUsername     string     `json:"silo_username,omitempty"`
	SiloProfileName  string     `json:"silo_profile_name,omitempty"`
	LastImportedAt   *time.Time `json:"last_imported_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type CreateMappingInput struct {
	SourceID         int    `json:"source_id"`
	ExternalUserID   string `json:"external_user_id"`
	ExternalUserName string `json:"external_user_name"`
	SiloUserID       int    `json:"silo_user_id"`
	SiloProfileID    string `json:"silo_profile_id"`
}

type UpdateMappingInput struct {
	SiloUserID    *int    `json:"silo_user_id,omitempty"`
	SiloProfileID *string `json:"silo_profile_id,omitempty"`
}

type SetAdminTokenInput struct {
	Token string `json:"token"`
}

// BulkRunResult is the response from a bulk admin run request.
type BulkRunResult struct {
	Runs    []*Run `json:"runs"`
	Skipped int    `json:"skipped"`
	Errors  int    `json:"errors"`
}

type PlexLoginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type PlexLoginResult struct {
	Token string `json:"token"`
}

package watchsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type Capabilities struct {
	ImportWatched   bool `json:"import_watched"`
	ImportProgress  bool `json:"import_progress"`
	ExportWatched   bool `json:"export_watched"`
	ExportUnwatched bool `json:"export_unwatched"`
	ImportFavorites bool `json:"import_favorites"`
	ExportFavorites bool `json:"export_favorites"`
	RemoveFavorites bool `json:"remove_favorites"`
	ImportWatchlist bool `json:"import_watchlist"`
	ExportWatchlist bool `json:"export_watchlist"`
	RemoveWatchlist bool `json:"remove_watchlist"`
	// ProvidesWatchlistOrder is true when the provider returns its watchlist in a
	// user-configurable order that Silo can mirror locally.
	ProvidesWatchlistOrder bool `json:"provides_watchlist_order"`
	ScrobblePlayback       bool `json:"scrobble_playback"`
}

// ListKind identifies which personal list a sync operates on. The favorites and
// watchlist pipelines share the same machinery, parameterized by this kind.
type ListKind string

const (
	ListKindFavorites ListKind = "favorites"
	ListKindWatchlist ListKind = "watchlist"
)

const (
	AuthMethodDeviceCode = "device_code"
	AuthMethodAPIKey     = "api_key"
)

type Provider interface {
	Key() string
	DisplayName() string
	Capabilities() Capabilities
}

type HistorySourceProvider interface {
	HistorySource() userstore.WatchHistorySource
}

type AuthProvider interface {
	StartDeviceAuth(ctx context.Context, cfg ServerConfig) (DeviceAuthSession, error)
	PollDeviceAuth(ctx context.Context, cfg ServerConfig, session DeviceAuthSession) (TokenSet, error)
	RefreshToken(ctx context.Context, cfg ServerConfig, conn Connection) (TokenSet, error)
	LookupAccount(ctx context.Context, cfg ServerConfig, conn Connection) (ProviderAccount, error)
}

// APIKeyAuthProvider is implemented by providers that authenticate via a
// user-supplied API key rather than an OAuth device flow. The key itself is
// stored in Connection.AccessToken; LookupAccount and RefreshToken (no-op)
// from AuthProvider are still expected so the rest of the sync pipeline keeps
// working unchanged.
type APIKeyAuthProvider interface {
	ConnectWithAPIKey(ctx context.Context, apiKey string) (TokenSet, ProviderAccount, error)
}

type WatchedImporter interface {
	FetchWatched(ctx context.Context, cfg ServerConfig, conn Connection) ([]RemoteWatch, error)
}

type WatchedImportBatch struct {
	Rows           []RemoteWatch
	UpdatedCursors map[string]string
	Warnings       []string
}

type WatchedBatchImporter interface {
	FetchWatchedBatch(ctx context.Context, cfg ServerConfig, conn Connection) (WatchedImportBatch, error)
}

type ProgressImporter interface {
	FetchProgress(ctx context.Context, cfg ServerConfig, conn Connection) ([]RemoteProgress, error)
}

type ProgressImportBatch struct {
	Rows           []RemoteProgress
	UpdatedCursors map[string]string
	Warnings       []string
}

type ProgressBatchImporter interface {
	FetchProgressBatch(ctx context.Context, cfg ServerConfig, conn Connection) (ProgressImportBatch, error)
}

type WatchedExporter interface {
	FetchHistory(ctx context.Context, cfg ServerConfig, conn Connection) ([]RemotePlay, error)
	ExportHistory(ctx context.Context, cfg ServerConfig, conn Connection, plays []LocalPlay) (ExportResult, error)
}

type UnwatchedExporter interface {
	RemoveHistory(ctx context.Context, cfg ServerConfig, conn Connection, plays []LocalPlay) (ExportResult, error)
}

type FavoriteImporter interface {
	FetchFavorites(ctx context.Context, cfg ServerConfig, conn Connection) ([]RemoteFavorite, error)
}

type FavoriteImportBatch struct {
	Rows           []RemoteFavorite
	UpdatedCursors map[string]string
	Warnings       []string
}

type FavoriteBatchImporter interface {
	FetchFavoritesBatch(ctx context.Context, cfg ServerConfig, conn Connection) (FavoriteImportBatch, error)
}

type FavoriteExporter interface {
	ExportFavorites(ctx context.Context, cfg ServerConfig, conn Connection, favorites []LocalFavorite) (ExportResult, error)
}

type FavoriteRemover interface {
	RemoveFavorites(ctx context.Context, cfg ServerConfig, conn Connection, favorites []LocalFavorite) (ExportResult, error)
}

// Watchlist provider interfaces mirror the favorite ones but target the
// provider's watchlist ("want to watch") list rather than its favorites. They
// reuse the RemoteFavorite/LocalFavorite/FavoriteImportBatch carriers, which
// hold only generic item identity (external ids, title, year, kind).
type WatchlistImporter interface {
	FetchWatchlist(ctx context.Context, cfg ServerConfig, conn Connection) ([]RemoteFavorite, error)
}

type WatchlistBatchImporter interface {
	FetchWatchlistBatch(ctx context.Context, cfg ServerConfig, conn Connection) (FavoriteImportBatch, error)
}

type WatchlistExporter interface {
	ExportWatchlist(ctx context.Context, cfg ServerConfig, conn Connection, items []LocalFavorite) (ExportResult, error)
}

type WatchlistRemover interface {
	RemoveWatchlist(ctx context.Context, cfg ServerConfig, conn Connection, items []LocalFavorite) (ExportResult, error)
}

type Scrobbler interface {
	Start(ctx context.Context, cfg ServerConfig, conn Connection, event ScrobbleEvent) error
	Pause(ctx context.Context, cfg ServerConfig, conn Connection, event ScrobbleEvent) error
	Stop(ctx context.Context, cfg ServerConfig, conn Connection, event ScrobbleEvent) error
}

type OrderedScrobbler interface {
	ScrobbleOrderingKey(conn Connection, event ScrobbleEvent) string
}

type ServerConfig struct {
	ClientID     string
	ClientSecret string
}

func (c ServerConfig) Configured() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

type Connection struct {
	ID                           string
	Provider                     string
	UserID                       int
	ProfileID                    string
	ProviderAccountID            string
	ProviderUsername             string
	AccessToken                  string
	RefreshToken                 string
	TokenExpiresAt               *time.Time
	ImportWatchedEnabled         bool
	ImportProgressEnabled        bool
	ExportWatchedEnabled         bool
	ExportUnwatchedEnabled       bool
	ImportFavoritesEnabled       bool
	ExportFavoritesEnabled       bool
	SyncFavoriteRemovalsEnabled  bool
	ImportWatchlistEnabled       bool
	ExportWatchlistEnabled       bool
	SyncWatchlistRemovalsEnabled bool
	SyncWatchlistOrderEnabled    bool
	ScrobbleEnabled              bool
	LastInboundSyncAt            *time.Time
	LastProgressSyncAt           *time.Time
	LastOutboundSyncAt           *time.Time
	LastFavoritesSyncAt          *time.Time
	LastWatchlistSyncAt          *time.Time
	LastScrobbleErrorAt          *time.Time
	LastError                    string
	// RateLimitedUntil defers scheduled syncs after a provider 429 so retries
	// don't burn more of the provider's request quota while still throttled.
	RateLimitedUntil *time.Time
	SyncCursors      map[string]string `json:"-"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type SyncRun struct {
	ID                       string     `json:"id"`
	ConnectionID             string     `json:"connection_id"`
	Trigger                  string     `json:"trigger"`
	Status                   string     `json:"status"`
	Provider                 string     `json:"provider"`
	InboundWatchedFound      int        `json:"inbound_watched_found"`
	InboundWatchedImported   int        `json:"inbound_watched_imported"`
	InboundProgressFound     int        `json:"inbound_progress_found"`
	InboundProgressImported  int        `json:"inbound_progress_imported"`
	OutboundFound            int        `json:"outbound_found"`
	OutboundSent             int        `json:"outbound_sent"`
	InboundFavoritesFound    int        `json:"inbound_favorites_found"`
	InboundFavoritesImported int        `json:"inbound_favorites_imported"`
	OutboundFavoritesFound   int        `json:"outbound_favorites_found"`
	OutboundFavoritesSent    int        `json:"outbound_favorites_sent"`
	FavoriteRemovalsSent     int        `json:"favorite_removals_sent"`
	InboundWatchlistFound    int        `json:"inbound_watchlist_found"`
	InboundWatchlistImported int        `json:"inbound_watchlist_imported"`
	OutboundWatchlistFound   int        `json:"outbound_watchlist_found"`
	OutboundWatchlistSent    int        `json:"outbound_watchlist_sent"`
	WatchlistRemovalsSent    int        `json:"watchlist_removals_sent"`
	Warning                  string     `json:"warning,omitempty"`
	Error                    string     `json:"error,omitempty"`
	StartedAt                time.Time  `json:"started_at"`
	CompletedAt              *time.Time `json:"completed_at,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
}

type SyncRunStatus string

const (
	SyncRunStatusQueued  SyncRunStatus = "queued"
	SyncRunStatusRunning SyncRunStatus = "running"
	SyncRunStatusSuccess SyncRunStatus = "success"
	SyncRunStatusWarning SyncRunStatus = "warning"
	SyncRunStatusFailed  SyncRunStatus = "failed"
)

type ManualSyncResult struct {
	Run               SyncRun `json:"run"`
	RetryAfterSeconds int     `json:"retry_after_seconds"`
}

type SyncCooldownError struct {
	RetryAfterSeconds int
}

func (e SyncCooldownError) Error() string {
	return "watch provider sync is cooling down"
}

// RateLimitedError reports that a provider rejected a request with HTTP 429
// (or an equivalent quota signal). RetryAfter carries the provider-supplied
// Retry-After when available, otherwise a provider-chosen fallback; zero means
// unknown. Callers should stop issuing requests to the provider and defer the
// remaining work instead of treating affected items as failed.
type RateLimitedError struct {
	Provider   string
	RetryAfter time.Duration
}

func (e RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s rate limit reached; retry after %s", e.Provider, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("%s rate limit reached", e.Provider)
}

// AsRateLimited unwraps err looking for a RateLimitedError.
func AsRateLimited(err error) (RateLimitedError, bool) {
	var rle RateLimitedError
	ok := errors.As(err, &rle)
	return rle, ok
}

type DeviceAuthSession struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	UserID          int        `json:"user_id"`
	ProfileID       string     `json:"profile_id"`
	DeviceCode      string     `json:"device_code,omitempty"`
	UserCode        string     `json:"user_code"`
	VerificationURL string     `json:"verification_url"`
	IntervalSeconds int        `json:"interval_seconds"`
	ExpiresAt       time.Time  `json:"expires_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type TokenSet struct {
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt *time.Time
}

type ProviderAccount struct {
	ID       string
	Username string
}

type RemoteWatch struct {
	Provider        string
	ProviderItemKey string
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
	PlayCount       int
	LastWatchedAt   *time.Time
}

type RemoteProgress struct {
	Provider        string
	ProviderItemKey string
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
	ProgressPercent float64
	PausedAt        time.Time
}

type RemoteFavorite struct {
	Provider        string
	ProviderItemKey string
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
	FavoritedAt     time.Time
}

type RemotePlay struct {
	Provider        string
	ProviderItemKey string
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
	WatchedAt       time.Time
}

type LocalPlay struct {
	HistoryID       string
	MediaItemID     string
	ProviderItemKey string
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
	WatchedAt       time.Time
	DurationSeconds float64
	Source          userstore.WatchHistorySource
}

type LocalFavorite struct {
	MediaItemID     string
	ProviderItemKey string
	Kind            string
	Title           string
	Year            int
	IMDbID          string
	TMDBID          string
	TVDBID          string
	SeriesIMDbID    string
	SeriesTMDBID    string
	SeriesTVDBID    string
	FavoritedAt     time.Time
}

type LocalWatchEventKind string

const (
	LocalWatchEventMarkedWatched   LocalWatchEventKind = "marked_watched"
	LocalWatchEventMarkedUnwatched LocalWatchEventKind = "marked_unwatched"
)

type LocalWatchEvent struct {
	Kind      LocalWatchEventKind
	UserID    int
	ProfileID string
	Plays     []LocalPlay
}

type ListChange string

const (
	ListChangeAdded   ListChange = "added"
	ListChangeRemoved ListChange = "removed"
)

// LocalListEvent reports that a profile's local list membership changed (an item
// added or removed), so the watch providers bound to that list kind can mirror
// the change. It serves both the favorites and watchlist lists via List.
type LocalListEvent struct {
	List      ListKind
	Change    ListChange
	UserID    int
	ProfileID string
	Items     []LocalFavorite
}

type HistoryExport struct {
	ID              string
	ConnectionID    string
	HistoryID       string
	MediaItemID     string
	WatchedAt       time.Time
	ProviderItemKey string
	Status          string
	AttemptCount    int
	LastAttemptAt   *time.Time
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ListItemState is the per-connection, per-list shadow record tracking whether
// an item is present locally and/or remotely, used to drive incremental
// export/removal reconciliation. It serves both the favorites and watchlist
// lists, discriminated by ListKind.
type ListItemState struct {
	ID                  string
	ConnectionID        string
	ListKind            ListKind
	MediaItemID         string
	ProviderItemKey     string
	Kind                string
	Title               string
	Year                int
	RemotePresent       bool
	LocalPresent        bool
	LastSeenRemoteAt    *time.Time
	LastSeenLocalAt     *time.Time
	LastExportedAt      *time.Time
	LastRemovedRemoteAt *time.Time
	LastRemovedLocalAt  *time.Time
	LastError           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ScrobbleSession struct {
	PlaybackSessionID string
	ConnectionID      string
	MediaItemID       string
	ProviderItemKey   string
	Kind              string
	IMDbID            string
	TMDBID            string
	TVDBID            string
	SeriesIMDbID      string
	SeriesTMDBID      string
	SeriesTVDBID      string
	SeasonNumber      int
	EpisodeNumber     int
	HistoryID         string
	StartedAt         time.Time
	LastProgress      float64
	DurationSeconds   float64
	Completed         bool
	LastAction        string
	StopSentAt        *time.Time
	LastError         string
}

type ExportResult struct {
	Sent     []string
	NotFound []string
	Failed   map[string]string
}

type ScrobbleEvent struct {
	PlaybackSessionID string
	UserID            int
	ProfileID         string
	MediaItemID       string
	ProviderItemKey   string
	Kind              string
	IMDbID            string
	TMDBID            string
	TVDBID            string
	SeriesIMDbID      string
	SeriesTMDBID      string
	SeriesTVDBID      string
	SeasonNumber      int
	EpisodeNumber     int
	HistoryID         string
	PositionSeconds   float64
	DurationSeconds   float64
	Completed         bool
	OccurredAt        time.Time
}

func (w RemoteWatch) HistoryRecord() historyimport.Record {
	return historyimport.Record{
		Kind:          w.Kind,
		Title:         w.Title,
		Year:          w.Year,
		IMDbID:        w.IMDbID,
		TMDBID:        w.TMDBID,
		TVDBID:        w.TVDBID,
		SeriesTitle:   w.SeriesTitle,
		SeriesYear:    w.SeriesYear,
		SeriesIMDbID:  w.SeriesIMDbID,
		SeriesTMDBID:  w.SeriesTMDBID,
		SeriesTVDBID:  w.SeriesTVDBID,
		SeasonNumber:  w.SeasonNumber,
		EpisodeNumber: w.EpisodeNumber,
		Played:        true,
		PlayCount:     w.PlayCount,
		LastPlayedAt:  w.LastWatchedAt,
	}
}

func (p RemoteProgress) HistoryRecord() historyimport.Record {
	return historyimport.Record{
		Kind:          p.Kind,
		Title:         p.Title,
		Year:          p.Year,
		IMDbID:        p.IMDbID,
		TMDBID:        p.TMDBID,
		TVDBID:        p.TVDBID,
		SeriesTitle:   p.SeriesTitle,
		SeriesYear:    p.SeriesYear,
		SeriesIMDbID:  p.SeriesIMDbID,
		SeriesTMDBID:  p.SeriesTMDBID,
		SeriesTVDBID:  p.SeriesTVDBID,
		SeasonNumber:  p.SeasonNumber,
		EpisodeNumber: p.EpisodeNumber,
		UpdatedAt:     p.PausedAt,
	}
}

func (f RemoteFavorite) HistoryRecord() historyimport.Record {
	return historyimport.Record{
		Kind:          f.Kind,
		Title:         f.Title,
		Year:          f.Year,
		IMDbID:        f.IMDbID,
		TMDBID:        f.TMDBID,
		TVDBID:        f.TVDBID,
		SeriesTitle:   f.SeriesTitle,
		SeriesYear:    f.SeriesYear,
		SeriesIMDbID:  f.SeriesIMDbID,
		SeriesTMDBID:  f.SeriesTMDBID,
		SeriesTVDBID:  f.SeriesTVDBID,
		SeasonNumber:  f.SeasonNumber,
		EpisodeNumber: f.EpisodeNumber,
		UpdatedAt:     f.FavoritedAt,
	}
}

type ProviderSummary struct {
	Key          string       `json:"key"`
	DisplayName  string       `json:"display_name"`
	Capabilities Capabilities `json:"capabilities"`
}

type ConnectionStatus struct {
	Provider                     string       `json:"provider"`
	DisplayName                  string       `json:"display_name"`
	Capabilities                 Capabilities `json:"capabilities"`
	AuthMethod                   string       `json:"auth_method"`
	Connected                    bool         `json:"connected"`
	ProviderUsername             string       `json:"provider_username,omitempty"`
	ImportWatchedEnabled         bool         `json:"import_watched_enabled"`
	ImportProgressEnabled        bool         `json:"import_progress_enabled"`
	ExportWatchedEnabled         bool         `json:"export_watched_enabled"`
	ExportUnwatchedEnabled       bool         `json:"export_unwatched_enabled"`
	ImportFavoritesEnabled       bool         `json:"import_favorites_enabled"`
	ExportFavoritesEnabled       bool         `json:"export_favorites_enabled"`
	SyncFavoriteRemovalsEnabled  bool         `json:"sync_favorite_removals_enabled"`
	ImportWatchlistEnabled       bool         `json:"import_watchlist_enabled"`
	ExportWatchlistEnabled       bool         `json:"export_watchlist_enabled"`
	SyncWatchlistRemovalsEnabled bool         `json:"sync_watchlist_removals_enabled"`
	SyncWatchlistOrderEnabled    bool         `json:"sync_watchlist_order_enabled"`
	ScrobbleEnabled              bool         `json:"scrobble_enabled"`
	CredentialsConfigured        bool         `json:"credentials_configured"`
	LastInboundSyncAt            *time.Time   `json:"last_inbound_sync_at,omitempty"`
	LastProgressSyncAt           *time.Time   `json:"last_progress_sync_at,omitempty"`
	LastOutboundSyncAt           *time.Time   `json:"last_outbound_sync_at,omitempty"`
	LastFavoritesSyncAt          *time.Time   `json:"last_favorites_sync_at,omitempty"`
	LastWatchlistSyncAt          *time.Time   `json:"last_watchlist_sync_at,omitempty"`
	LastScrobbleErrorAt          *time.Time   `json:"last_scrobble_error_at,omitempty"`
	LastError                    string       `json:"last_error,omitempty"`
}

type ConnectionUpdate struct {
	ImportWatchedEnabled         *bool `json:"import_watched_enabled,omitempty"`
	ImportProgressEnabled        *bool `json:"import_progress_enabled,omitempty"`
	ExportWatchedEnabled         *bool `json:"export_watched_enabled,omitempty"`
	ExportUnwatchedEnabled       *bool `json:"export_unwatched_enabled,omitempty"`
	ImportFavoritesEnabled       *bool `json:"import_favorites_enabled,omitempty"`
	ExportFavoritesEnabled       *bool `json:"export_favorites_enabled,omitempty"`
	SyncFavoriteRemovalsEnabled  *bool `json:"sync_favorite_removals_enabled,omitempty"`
	ImportWatchlistEnabled       *bool `json:"import_watchlist_enabled,omitempty"`
	ExportWatchlistEnabled       *bool `json:"export_watchlist_enabled,omitempty"`
	SyncWatchlistRemovalsEnabled *bool `json:"sync_watchlist_removals_enabled,omitempty"`
	SyncWatchlistOrderEnabled    *bool `json:"sync_watchlist_order_enabled,omitempty"`
	ScrobbleEnabled              *bool `json:"scrobble_enabled,omitempty"`
}

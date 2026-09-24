package apiv2

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchsync"
	"github.com/jackc/pgx/v5"
)

const opGetWatchProviderConnection = "getWatchProviderConnection"
const opGetWatchProviderSettings = "getWatchProviderSettings"
const opUpdateWatchProviderConnection = "updateWatchProviderConnection"

// RequestLifecycleService preserves the domain's viewer checks for cancellation.
type RequestLifecycleService interface {
	Cancel(context.Context, mediarequests.Viewer, string, string) (*mediarequests.Request, error)
	GetFeatureStatus(context.Context, mediarequests.Viewer) (mediarequests.FeatureStatus, error)
	RequestCapabilityAllowed(context.Context, mediarequests.Viewer) (bool, error)
}

type WatchProviderService = handlers.WatchProviderService

type RequestCancelInput struct {
	ID   ID `path:"id"`
	Body struct {
		Reason string `json:"reason,omitempty" maxLength:"2000"`
	}
}
type FeatureStatus struct {
	Capability
	RequestsEnabled            bool `json:"requests_enabled"`
	RatingRestrictionsEnforced bool `json:"rating_restrictions_enforced"`
}
type RequestFeatureStatusOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         FeatureStatus
}
type WatchProviderInput struct {
	Provider string `path:"provider" minLength:"1" maxLength:"255"`
}
type WatchProviderUpdateInput struct {
	WatchProviderInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        watchsync.ConnectionUpdate
}
type WatchProviderPollInput struct {
	WatchProviderInput
	Body struct {
		AuthSessionID string `json:"auth_session_id" format:"uuid"`
	}
}
type WatchProviderAPIKeyInput struct {
	WatchProviderInput
	Body struct {
		APIKey           string                           `json:"api_key" minLength:"1" maxLength:"8192"`
		ConnectionConfig watchsync.ConnectionConfigValues `json:"connection_config,omitempty"`
	}
}
type WatchProviderRunsInput struct {
	WatchProviderInput
	Limit int `query:"limit" default:"10" minimum:"1" maximum:"50" doc:"Maximum recent runs; this bounded activity window does not expose continuation."`
}
type WatchProviderCollectionOutput struct {
	Body Collection[WatchProviderSummary]
}
type WatchProviderConnectionOutput struct {
	Body WatchProviderConnection
}
type WatchProviderSettingsOutput struct {
	ETag string `header:"ETag"`
	Body WatchProviderSettings
}

// WatchProviderSettings contains only persisted connection preferences.
// Provider metadata and runtime capability/configuration state have no place
// in this representation: its strong validator is the connection row version.
type WatchProviderSettings struct {
	ImportWatchedEnabled         bool `json:"import_watched_enabled"`
	ImportProgressEnabled        bool `json:"import_progress_enabled"`
	ExportWatchedEnabled         bool `json:"export_watched_enabled"`
	ExportUnwatchedEnabled       bool `json:"export_unwatched_enabled"`
	ImportFavoritesEnabled       bool `json:"import_favorites_enabled"`
	ExportFavoritesEnabled       bool `json:"export_favorites_enabled"`
	SyncFavoriteRemovalsEnabled  bool `json:"sync_favorite_removals_enabled"`
	ImportWatchlistEnabled       bool `json:"import_watchlist_enabled"`
	ExportWatchlistEnabled       bool `json:"export_watchlist_enabled"`
	SyncWatchlistRemovalsEnabled bool `json:"sync_watchlist_removals_enabled"`
	SyncWatchlistOrderEnabled    bool `json:"sync_watchlist_order_enabled"`
	ScrobbleEnabled              bool `json:"scrobble_enabled"`
	ImportRatingsEnabled         bool `json:"import_ratings_enabled" doc:"Import the provider's movie and series ratings as stars (1-2 is 1 star, 9-10 is 5 stars)."`
	ExportRatingsEnabled         bool `json:"export_ratings_enabled" doc:"Send the profile's star ratings to the provider (stars times two) and clear removed ones."`
}

func watchProviderSettingsOf(status watchsync.ConnectionStatus) WatchProviderSettings {
	return WatchProviderSettings{
		ImportWatchedEnabled:         status.ImportWatchedEnabled,
		ImportProgressEnabled:        status.ImportProgressEnabled,
		ExportWatchedEnabled:         status.ExportWatchedEnabled,
		ExportUnwatchedEnabled:       status.ExportUnwatchedEnabled,
		ImportFavoritesEnabled:       status.ImportFavoritesEnabled,
		ExportFavoritesEnabled:       status.ExportFavoritesEnabled,
		SyncFavoriteRemovalsEnabled:  status.SyncFavoriteRemovalsEnabled,
		ImportWatchlistEnabled:       status.ImportWatchlistEnabled,
		ExportWatchlistEnabled:       status.ExportWatchlistEnabled,
		SyncWatchlistRemovalsEnabled: status.SyncWatchlistRemovalsEnabled,
		SyncWatchlistOrderEnabled:    status.SyncWatchlistOrderEnabled,
		ScrobbleEnabled:              status.ScrobbleEnabled,
		ImportRatingsEnabled:         status.ImportRatingsEnabled,
		ExportRatingsEnabled:         status.ExportRatingsEnabled,
	}
}

type guardedWatchProviderService interface {
	UpdateConnectionConditional(context.Context, int, string, string, watchsync.ConnectionVersion, watchsync.ConnectionUpdate) (watchsync.ConnectionStatus, error)
}

func watchConnectionTag(userID int, profileID, provider string, status watchsync.ConnectionStatus) EntityTag {
	return RenderETag("watch-provider-settings/"+strconv.Itoa(userID)+"/"+profileID+"/"+provider, status.Version.ID, status.Version.UpdatedAt.UnixMicro())
}

type WatchProviderRunsOutput struct {
	Body Collection[WatchProviderSyncRun]
}
type WatchProviderSyncOutput struct {
	Body struct {
		Run               WatchProviderSyncRun `json:"run"`
		RetryAfterSeconds int                  `json:"retry_after_seconds"`
	}
}

// Device codes and access tokens are deliberately excluded from this projection.
type WatchProviderDeviceAuth struct {
	ID              ID      `json:"id"`
	Provider        string  `json:"provider"`
	UserCode        string  `json:"user_code"`
	VerificationURL string  `json:"verification_url"`
	IntervalSeconds int     `json:"interval_seconds"`
	ExpiresAt       Instant `json:"expires_at"`
}
type WatchProviderDeviceAuthOutput struct{ Body WatchProviderDeviceAuth }

func registerRequestLifecycle(reg *Registry, requests RequestLifecycleService, providers WatchProviderService) {
	op := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, requestsTag, summary), Class: ClassProfileScoped, ServiceBacked: true}
		o.Errors = []int{http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests}
		if method != http.MethodGet {
			o.DemoRestricted = true
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/requests/status", "getRequestStatus", "Get request capabilities for the current viewer."), func(ctx context.Context, _ *CapabilityInput) (*RequestFeatureStatusOutput, error) {
		if requests == nil {
			return &RequestFeatureStatusOutput{Body: FeatureStatus{Capability: Capability{State: StateNotConfigured}}}, nil
		}
		status, err := requests.GetFeatureStatus(ctx, lifecycleViewer(ctx))
		if err != nil {
			return nil, requestProblem(err)
		}
		allowed := false
		if status.RequestsEnabled {
			allowed, err = requests.RequestCapabilityAllowed(ctx, lifecycleViewer(ctx))
			if err != nil {
				return nil, requestProblem(err)
			}
		}
		return &RequestFeatureStatusOutput{Body: FeatureStatus{Capability: Capability{State: enabledCapabilityState(status.RequestsEnabled), Allowed: &allowed}, RequestsEnabled: status.RequestsEnabled, RatingRestrictionsEnforced: status.RatingRestrictionsEnforced}}, nil
	})
	Register(reg, op(http.MethodPost, "/requests/{id}/cancel", "cancelRequest", "Cancel an accessible request."), func(ctx context.Context, in *RequestCancelInput) (*MediaRequestOutput, error) {
		if requests == nil {
			return nil, unavailable("requests")
		}
		result, err := requests.Cancel(ctx, lifecycleViewer(ctx), string(in.ID), in.Body.Reason)
		if err != nil {
			return nil, requestProblem(err)
		}
		return &MediaRequestOutput{Body: mediaRequestOf(result)}, nil
	})
	scope := func(ctx context.Context) (int, string, error) {
		if providers == nil {
			return 0, "", unavailable("watch providers")
		}
		return claimsFrom(ctx).UserID, profileFrom(ctx), nil
	}
	Register(reg, op(http.MethodGet, "/watch-providers", "listWatchProviders", "List watch providers and their capabilities."), func(ctx context.Context, _ *struct{}) (*WatchProviderCollectionOutput, error) {
		if _, _, err := scope(ctx); err != nil {
			return nil, err
		}
		out := &WatchProviderCollectionOutput{}
		out.Body.Items = []WatchProviderSummary{}
		for _, row := range providers.ListProviders() {
			schemas, err := adminPluginConfigSchemasOf(row.ConnectionConfigSchema)
			if err != nil {
				return nil, serviceProblem(err)
			}
			out.Body.Items = append(out.Body.Items, WatchProviderSummary{Key: row.Key, DisplayName: row.DisplayName, Capabilities: watchProviderCapabilitiesOf(row.Capabilities), ConnectionConfigSchema: schemas})
		}
		return out, nil
	})
	connection := func(ctx context.Context, u int, p, key string) (*WatchProviderConnectionOutput, error) {
		s, err := providers.GetConnectionStatus(ctx, u, p, key)
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		body, err := watchProviderConnectionOf(s)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &WatchProviderConnectionOutput{Body: body}, nil
	}
	Register(reg, op(http.MethodGet, "/watch-providers/{provider}/connection", opGetWatchProviderConnection, "Get the active profile's connection."), func(ctx context.Context, in *WatchProviderInput) (*WatchProviderConnectionOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		return connection(ctx, u, p, in.Provider)
	})
	Register(reg, op(http.MethodGet, "/watch-providers/{provider}/connection/settings", opGetWatchProviderSettings, "Read persisted watch-provider preferences and their update validator."), func(ctx context.Context, in *WatchProviderInput) (*WatchProviderSettingsOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		current, err := providers.GetConnectionStatus(ctx, u, p, in.Provider)
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		if current.Version.ID == "" {
			return nil, watchProviderProblem(watchsync.ErrConnectionNotFound)
		}
		return &WatchProviderSettingsOutput{ETag: watchConnectionTag(u, p, in.Provider, current).String(), Body: watchProviderSettingsOf(current)}, nil
	})
	update := op(http.MethodPatch, "/watch-providers/{provider}/connection", opUpdateWatchProviderConnection, "Update watch-provider synchronization preferences.")
	update.Guarded = true
	Register(reg, update, func(ctx context.Context, in *WatchProviderUpdateInput) (*WatchProviderSettingsOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		current, err := providers.GetConnectionStatus(ctx, u, p, in.Provider)
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		if current.Version.ID == "" {
			return nil, watchProviderProblem(watchsync.ErrConnectionNotFound)
		}
		if problem := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, watchConnectionTag(u, p, in.Provider, current)); problem != nil {
			return nil, problem
		}
		writer, ok := providers.(guardedWatchProviderService)
		if !ok {
			return nil, unavailable("guarded watch-provider settings")
		}
		saved, err := writer.UpdateConnectionConditional(ctx, u, p, in.Provider, current.Version, in.Body)
		if errors.Is(err, watchsync.ErrStaleConnection) {
			latest, readErr := providers.GetConnectionStatus(ctx, u, p, in.Provider)
			if readErr != nil {
				return nil, watchProviderProblem(readErr)
			}
			if latest.Version.ID == "" {
				return nil, watchProviderProblem(watchsync.ErrConnectionNotFound)
			}
			return nil, StaleVersionProblem(watchConnectionTag(u, p, in.Provider, latest))
		}
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		return &WatchProviderSettingsOutput{ETag: watchConnectionTag(u, p, in.Provider, saved).String(), Body: watchProviderSettingsOf(saved)}, nil
	})

	del := op(http.MethodDelete, "/watch-providers/{provider}/connection", "deleteWatchProviderConnection", "Disconnect the active profile from a watch provider.")
	del.DefaultStatus = http.StatusNoContent
	Register(reg, del, func(ctx context.Context, in *WatchProviderInput) (*struct{}, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		if err = providers.DeleteConnection(ctx, u, p, in.Provider); err != nil {
			return nil, watchProviderProblem(err)
		}
		return nil, nil
	})
	Register(reg, op(http.MethodPost, "/watch-providers/{provider}/auth/device-code", "startWatchProviderDeviceAuth", "Start device authorization with a watch provider."), func(ctx context.Context, in *WatchProviderInput) (*WatchProviderDeviceAuthOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		s, err := providers.StartDeviceAuth(ctx, u, p, in.Provider)
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		return &WatchProviderDeviceAuthOutput{Body: WatchProviderDeviceAuth{ID(s.ID), s.Provider, s.UserCode, s.VerificationURL, s.IntervalSeconds, NewInstant(s.ExpiresAt)}}, nil
	})
	Register(reg, op(http.MethodPost, "/watch-providers/{provider}/auth/poll", "pollWatchProviderDeviceAuth", "Poll device authorization. Concurrent polls are not durably coalesced."), func(ctx context.Context, in *WatchProviderPollInput) (*WatchProviderConnectionOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		if _, err = providers.PollDeviceAuth(ctx, u, p, in.Provider, in.Body.AuthSessionID); err != nil {
			return nil, watchProviderProblem(err)
		}
		return connection(ctx, u, p, in.Provider)
	})
	Register(reg, op(http.MethodPost, "/watch-providers/{provider}/auth/api-key", "connectWatchProviderAPIKey", "Connect a watch provider with a supplied API key."), func(ctx context.Context, in *WatchProviderAPIKeyInput) (*WatchProviderConnectionOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		if _, err = providers.ConnectAPIKeyWithConfig(ctx, u, p, in.Provider, in.Body.APIKey, in.Body.ConnectionConfig); err != nil {
			return nil, watchProviderProblem(err)
		}
		return connection(ctx, u, p, in.Provider)
	})
	sync := op(http.MethodPost, "/watch-providers/{provider}/sync", "triggerWatchProviderSync", "Request a manual sync, subject to the provider cooldown.")
	sync.DefaultStatus = http.StatusAccepted
	Register(reg, sync, func(ctx context.Context, in *WatchProviderInput) (*WatchProviderSyncOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		result, err := providers.RequestManualSync(ctx, u, p, in.Provider)
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		out := &WatchProviderSyncOutput{}
		out.Body.Run = watchProviderSyncRunOf(result.Run)
		out.Body.RetryAfterSeconds = result.RetryAfterSeconds
		return out, nil
	})
	Register(reg, op(http.MethodGet, "/watch-providers/{provider}/sync-runs", "listWatchProviderSyncRuns", "List a bounded window of the profile's recent sync runs."), func(ctx context.Context, in *WatchProviderRunsInput) (*WatchProviderRunsOutput, error) {
		u, p, err := scope(ctx)
		if err != nil {
			return nil, err
		}
		runs, err := providers.ListSyncRuns(ctx, u, p, in.Provider, in.Limit)
		if err != nil {
			return nil, watchProviderProblem(err)
		}
		out := &WatchProviderRunsOutput{}
		out.Body.Items = make([]WatchProviderSyncRun, 0, len(runs))
		for _, run := range runs {
			out.Body.Items = append(out.Body.Items, watchProviderSyncRunOf(run))
		}
		return out, nil
	})
}
func lifecycleViewer(ctx context.Context) mediarequests.Viewer {
	c := claimsFrom(ctx)
	return mediarequests.Viewer{UserID: c.UserID, ProfileID: profileFrom(ctx), IsAdmin: c.Role == models.RoleAdmin}
}
func watchProviderProblem(err error) *Problem {
	if errors.Is(err, watchsync.ErrSettingsCleanupUnavailable) {
		return NewProblem(TypeDependencyUnavailable, "Watchlist ordering could not be cleared; settings were not changed.").WithRetryAfter(5)
	}

	if _, ok := errors.AsType[watchsync.UnknownProviderError](err); ok {
		return NewProblem(TypeNotFound, "Watch provider not found.")
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, watchsync.ErrConnectionNotFound) || errors.Is(err, watchsync.ErrAuthSessionMismatch) {
		return NewProblem(TypeNotFound, "Watch-provider connection or authorization session not found.")
	}
	if errors.Is(err, watchsync.ErrAuthSessionCompleted) || errors.Is(err, watchsync.ErrAuthSessionExpired) || watchsync.IsDeviceAuthPending(err) {
		return NewProblem(TypeConflict, err.Error())
	}
	if _, ok := errors.AsType[watchsync.ProviderCapabilityError](err); ok {
		return NewProblem(TypeCapabilityUnsupported, "The watch provider does not support this operation.")
	}
	if cooldown, ok := errors.AsType[watchsync.SyncCooldownError](err); ok {
		p := NewProblem(TypeRateLimited, "Watch provider sync recently ran. Try again later.")
		return p.WithHeader("Retry-After", strconv.Itoa(max(1, cooldown.RetryAfterSeconds)))
	}
	if limited, ok := watchsync.AsRateLimited(err); ok {
		// The provider throttled this call (for example Trakt's slow_down answer
		// to a device-code poll); pass its wait on so the client backs off.
		p := NewProblem(TypeRateLimited, "The watch provider is rate limiting requests. Try again later.")
		return p.WithHeader("Retry-After", strconv.Itoa(max(1, int(math.Ceil(limited.RetryAfter.Seconds())))))
	}
	if watchsync.IsInvalidCredentialError(err) {
		return NewProblem(TypeValidationFailed, "The watch provider rejected the supplied credential.")
	}
	if watchsync.IsRetryableProviderError(err) {
		return NewProblem(TypeDependencyUnavailable, "The watch provider is temporarily unavailable.")
	}
	return NewProblem(TypeInternalError, "Watch-provider operation failed.")
}

// WatchProviderCapabilities is the v2 projection of a provider's sync
// capabilities. It is separate from watchsync.Capabilities so the frozen v1
// responses, which serialize that type, keep their original fields.
type WatchProviderCapabilities struct {
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
	ImportRatings          bool `json:"import_ratings"`
	ExportRatings          bool `json:"export_ratings"`
}

func watchProviderCapabilitiesOf(c watchsync.Capabilities) WatchProviderCapabilities {
	return WatchProviderCapabilities{
		ImportWatched:          c.ImportWatched,
		ImportProgress:         c.ImportProgress,
		ExportWatched:          c.ExportWatched,
		ExportUnwatched:        c.ExportUnwatched,
		ImportFavorites:        c.ImportFavorites,
		ExportFavorites:        c.ExportFavorites,
		RemoveFavorites:        c.RemoveFavorites,
		ImportWatchlist:        c.ImportWatchlist,
		ExportWatchlist:        c.ExportWatchlist,
		RemoveWatchlist:        c.RemoveWatchlist,
		ProvidesWatchlistOrder: c.ProvidesWatchlistOrder,
		ScrobblePlayback:       c.ScrobblePlayback,
		ImportRatings:          c.ImportRatings,
		ExportRatings:          c.ExportRatings,
	}
}

type WatchProviderConnection struct {
	Provider                     string                    `json:"provider"`
	DisplayName                  string                    `json:"display_name"`
	Capabilities                 WatchProviderCapabilities `json:"capabilities"`
	AuthMethod                   string                    `json:"auth_method"`
	Connected                    bool                      `json:"connected"`
	ProviderUsername             string                    `json:"provider_username,omitempty"`
	ImportWatchedEnabled         bool                      `json:"import_watched_enabled"`
	ImportProgressEnabled        bool                      `json:"import_progress_enabled"`
	ExportWatchedEnabled         bool                      `json:"export_watched_enabled"`
	ExportUnwatchedEnabled       bool                      `json:"export_unwatched_enabled"`
	ImportFavoritesEnabled       bool                      `json:"import_favorites_enabled"`
	ExportFavoritesEnabled       bool                      `json:"export_favorites_enabled"`
	SyncFavoriteRemovalsEnabled  bool                      `json:"sync_favorite_removals_enabled"`
	ImportWatchlistEnabled       bool                      `json:"import_watchlist_enabled"`
	ExportWatchlistEnabled       bool                      `json:"export_watchlist_enabled"`
	SyncWatchlistRemovalsEnabled bool                      `json:"sync_watchlist_removals_enabled"`
	SyncWatchlistOrderEnabled    bool                      `json:"sync_watchlist_order_enabled"`
	ScrobbleEnabled              bool                      `json:"scrobble_enabled"`
	ImportRatingsEnabled         bool                      `json:"import_ratings_enabled"`
	ExportRatingsEnabled         bool                      `json:"export_ratings_enabled"`
	CredentialsConfigured        bool                      `json:"credentials_configured"`
	ConnectionConfigSchema       []AdminPluginConfigSchema `json:"connection_config_schema,omitempty"`
	LastInboundSyncAt            *Instant                  `json:"last_inbound_sync_at,omitempty"`
	LastProgressSyncAt           *Instant                  `json:"last_progress_sync_at,omitempty"`
	LastOutboundSyncAt           *Instant                  `json:"last_outbound_sync_at,omitempty"`
	LastFavoritesSyncAt          *Instant                  `json:"last_favorites_sync_at,omitempty"`
	LastWatchlistSyncAt          *Instant                  `json:"last_watchlist_sync_at,omitempty"`
	LastScrobbleErrorAt          *Instant                  `json:"last_scrobble_error_at,omitempty"`
	LastError                    string                    `json:"last_error,omitempty"`
}

func watchProviderConnectionOf(s watchsync.ConnectionStatus) (WatchProviderConnection, error) {
	schemas, err := adminPluginConfigSchemasOf(s.ConnectionConfigSchema)
	if err != nil {
		return WatchProviderConnection{}, err
	}
	return WatchProviderConnection{
		Provider:                     s.Provider,
		DisplayName:                  s.DisplayName,
		Capabilities:                 watchProviderCapabilitiesOf(s.Capabilities),
		AuthMethod:                   s.AuthMethod,
		Connected:                    s.Connected,
		ProviderUsername:             s.ProviderUsername,
		ImportWatchedEnabled:         s.ImportWatchedEnabled,
		ImportProgressEnabled:        s.ImportProgressEnabled,
		ExportWatchedEnabled:         s.ExportWatchedEnabled,
		ExportUnwatchedEnabled:       s.ExportUnwatchedEnabled,
		ImportFavoritesEnabled:       s.ImportFavoritesEnabled,
		ExportFavoritesEnabled:       s.ExportFavoritesEnabled,
		SyncFavoriteRemovalsEnabled:  s.SyncFavoriteRemovalsEnabled,
		ImportWatchlistEnabled:       s.ImportWatchlistEnabled,
		ExportWatchlistEnabled:       s.ExportWatchlistEnabled,
		SyncWatchlistRemovalsEnabled: s.SyncWatchlistRemovalsEnabled,
		SyncWatchlistOrderEnabled:    s.SyncWatchlistOrderEnabled,
		ScrobbleEnabled:              s.ScrobbleEnabled,
		ImportRatingsEnabled:         s.ImportRatingsEnabled,
		ExportRatingsEnabled:         s.ExportRatingsEnabled,
		CredentialsConfigured:        s.CredentialsConfigured,
		ConnectionConfigSchema:       schemas,
		LastInboundSyncAt:            instantPtr(s.LastInboundSyncAt),
		LastProgressSyncAt:           instantPtr(s.LastProgressSyncAt),
		LastOutboundSyncAt:           instantPtr(s.LastOutboundSyncAt),
		LastFavoritesSyncAt:          instantPtr(s.LastFavoritesSyncAt),
		LastWatchlistSyncAt:          instantPtr(s.LastWatchlistSyncAt),
		LastScrobbleErrorAt:          instantPtr(s.LastScrobbleErrorAt),
		LastError:                    s.LastError,
	}, nil
}

type WatchProviderSyncRun struct {
	ID                       ID       `json:"id"`
	ConnectionID             ID       `json:"connection_id"`
	Trigger                  string   `json:"trigger"`
	Status                   string   `json:"status"`
	Provider                 string   `json:"provider"`
	InboundWatchedFound      int      `json:"inbound_watched_found"`
	InboundWatchedImported   int      `json:"inbound_watched_imported"`
	InboundProgressFound     int      `json:"inbound_progress_found"`
	InboundProgressImported  int      `json:"inbound_progress_imported"`
	OutboundFound            int      `json:"outbound_found"`
	OutboundSent             int      `json:"outbound_sent"`
	InboundFavoritesFound    int      `json:"inbound_favorites_found"`
	InboundFavoritesImported int      `json:"inbound_favorites_imported"`
	OutboundFavoritesFound   int      `json:"outbound_favorites_found"`
	OutboundFavoritesSent    int      `json:"outbound_favorites_sent"`
	FavoriteRemovalsSent     int      `json:"favorite_removals_sent"`
	InboundWatchlistFound    int      `json:"inbound_watchlist_found"`
	InboundWatchlistImported int      `json:"inbound_watchlist_imported"`
	OutboundWatchlistFound   int      `json:"outbound_watchlist_found"`
	OutboundWatchlistSent    int      `json:"outbound_watchlist_sent"`
	WatchlistRemovalsSent    int      `json:"watchlist_removals_sent"`
	InboundRatingsFound      int      `json:"inbound_ratings_found"`
	InboundRatingsImported   int      `json:"inbound_ratings_imported"`
	OutboundRatingsFound     int      `json:"outbound_ratings_found" doc:"Movie and series ratings the profile holds."`
	OutboundRatingsSent      int      `json:"outbound_ratings_sent" doc:"Ratings set or cleared on the provider."`
	Warning                  string   `json:"warning,omitempty"`
	Error                    string   `json:"error,omitempty"`
	StartedAt                Instant  `json:"started_at"`
	CompletedAt              *Instant `json:"completed_at,omitempty"`
	CreatedAt                Instant  `json:"created_at"`
}

func watchProviderSyncRunOf(s watchsync.SyncRun) WatchProviderSyncRun {
	return WatchProviderSyncRun{
		ID:                       ID(s.ID),
		ConnectionID:             ID(s.ConnectionID),
		Trigger:                  s.Trigger,
		Status:                   s.Status,
		Provider:                 s.Provider,
		InboundWatchedFound:      s.InboundWatchedFound,
		InboundWatchedImported:   s.InboundWatchedImported,
		InboundProgressFound:     s.InboundProgressFound,
		InboundProgressImported:  s.InboundProgressImported,
		OutboundFound:            s.OutboundFound,
		OutboundSent:             s.OutboundSent,
		InboundFavoritesFound:    s.InboundFavoritesFound,
		InboundFavoritesImported: s.InboundFavoritesImported,
		OutboundFavoritesFound:   s.OutboundFavoritesFound,
		OutboundFavoritesSent:    s.OutboundFavoritesSent,
		FavoriteRemovalsSent:     s.FavoriteRemovalsSent,
		InboundWatchlistFound:    s.InboundWatchlistFound,
		InboundWatchlistImported: s.InboundWatchlistImported,
		OutboundWatchlistFound:   s.OutboundWatchlistFound,
		OutboundWatchlistSent:    s.OutboundWatchlistSent,
		WatchlistRemovalsSent:    s.WatchlistRemovalsSent,
		InboundRatingsFound:      s.InboundRatingsFound,
		InboundRatingsImported:   s.InboundRatingsImported,
		OutboundRatingsFound:     s.OutboundRatingsFound,
		OutboundRatingsSent:      s.OutboundRatingsSent,
		Warning:                  s.Warning,
		Error:                    s.Error,
		StartedAt:                NewInstant(s.StartedAt),
		CompletedAt:              instantPtr(s.CompletedAt),
		CreatedAt:                NewInstant(s.CreatedAt),
	}
}

var requestLifecycleOperationIDs = []string{"getRequestStatus", "cancelRequest", "listWatchProviders", opGetWatchProviderConnection, opGetWatchProviderSettings, opUpdateWatchProviderConnection, "deleteWatchProviderConnection", "startWatchProviderDeviceAuth", "pollWatchProviderDeviceAuth", "connectWatchProviderAPIKey", "triggerWatchProviderSync", "listWatchProviderSyncRuns"}

// WatchProviderSummary shares configuration forms with plugin administration.
type WatchProviderSummary struct {
	Key                    string                    `json:"key"`
	DisplayName            string                    `json:"display_name"`
	Capabilities           WatchProviderCapabilities `json:"capabilities"`
	ConnectionConfigSchema []AdminPluginConfigSchema `json:"connection_config_schema,omitempty"`
}

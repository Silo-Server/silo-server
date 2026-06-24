package watchsync

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type Service struct {
	repo           Repository
	registry       *Registry
	now            func() time.Time
	matcher        mediaMatcher
	watchState     watchStateImporter
	storeProvider  userstore.UserStoreProvider
	locks          sync.Map
	scrobbleQueues sync.Map
}

type scrobbleQueue struct {
	mu   sync.Mutex
	tail chan struct{}
}

type mediaMatcher interface {
	Match(ctx context.Context, record historyimport.Record) (*historyimport.Match, string, error)
}

type watchStateImporter interface {
	RecordImportedWatchIfNewerWithSource(ctx context.Context, userID int, profileID, targetID string, duration, position float64, completed bool, updatedAt time.Time, watchedAt *time.Time, source userstore.WatchHistorySource) (bool, error)
}

const (
	manualSyncCooldown = time.Hour
	manualSyncTimeout  = 10 * time.Minute
)

func NewService(repo Repository, registry *Registry) *Service {
	return &Service{
		repo:     repo,
		registry: registry,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) WithMatcher(matcher mediaMatcher) *Service {
	if s != nil {
		s.matcher = matcher
	}
	return s
}

func (s *Service) WithWatchState(watchState watchStateImporter) *Service {
	if s != nil {
		s.watchState = watchState
	}
	return s
}

func (s *Service) WithUserStoreProvider(provider userstore.UserStoreProvider) *Service {
	if s != nil {
		s.storeProvider = provider
	}
	return s
}

func (s *Service) WithDefaultWatchState(provider userstore.UserStoreProvider) *Service {
	return s.WithUserStoreProvider(provider).WithWatchState(watchstate.NewService(provider))
}

func (s *Service) ListProviders() []ProviderSummary {
	return s.registry.List()
}

func (s *Service) GetConnectionStatus(ctx context.Context, userID int, profileID string, providerKey string) (ConnectionStatus, error) {
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return ConnectionStatus{}, fmt.Errorf("unknown provider %q", providerKey)
	}
	authMethod := authMethodOf(provider)
	credentialsConfigured := authMethod == AuthMethodAPIKey
	if !credentialsConfigured {
		cfg, _ := s.serverConfig(ctx, providerKey)
		credentialsConfigured = cfg.Configured()
	}
	conn, connected, err := s.repo.GetConnection(ctx, providerKey, userID, profileID)
	if err != nil {
		return ConnectionStatus{}, err
	}
	missingAccessToken := connected && strings.TrimSpace(conn.AccessToken) == ""
	status := ConnectionStatus{
		Provider:                    providerKey,
		DisplayName:                 provider.DisplayName(),
		Capabilities:                provider.Capabilities(),
		AuthMethod:                  authMethod,
		Connected:                   connected && !missingAccessToken,
		CredentialsConfigured:       credentialsConfigured,
		ImportWatchedEnabled:        true,
		ImportProgressEnabled:       true,
		ExportWatchedEnabled:        true,
		ExportUnwatchedEnabled:      false,
		ImportFavoritesEnabled:      true,
		ExportFavoritesEnabled:      true,
		SyncFavoriteRemovalsEnabled: false,
		ScrobbleEnabled:             true,
	}
	if connected {
		status.ProviderUsername = conn.ProviderUsername
		status.ImportWatchedEnabled = conn.ImportWatchedEnabled
		status.ImportProgressEnabled = conn.ImportProgressEnabled
		status.ExportWatchedEnabled = conn.ExportWatchedEnabled
		status.ExportUnwatchedEnabled = conn.ExportUnwatchedEnabled
		status.ImportFavoritesEnabled = conn.ImportFavoritesEnabled
		status.ExportFavoritesEnabled = conn.ExportFavoritesEnabled
		status.SyncFavoriteRemovalsEnabled = conn.SyncFavoriteRemovalsEnabled
		status.ScrobbleEnabled = conn.ScrobbleEnabled
		status.LastInboundSyncAt = conn.LastInboundSyncAt
		status.LastProgressSyncAt = conn.LastProgressSyncAt
		status.LastOutboundSyncAt = conn.LastOutboundSyncAt
		status.LastFavoritesSyncAt = conn.LastFavoritesSyncAt
		status.LastScrobbleErrorAt = conn.LastScrobbleErrorAt
		status.LastError = conn.LastError
	}
	if missingAccessToken {
		status.LastError = fmt.Sprintf("%s connection is missing an access token; reconnect the provider", providerKey)
	}
	return status, nil
}

func (s *Service) UpdateConnection(ctx context.Context, userID int, profileID string, providerKey string, update ConnectionUpdate) (ConnectionStatus, error) {
	conn, ok, err := s.repo.GetConnection(ctx, providerKey, userID, profileID)
	if err != nil {
		return ConnectionStatus{}, err
	}
	if !ok {
		return ConnectionStatus{}, fmt.Errorf("watch provider connection not found")
	}
	if update.ImportWatchedEnabled != nil {
		conn.ImportWatchedEnabled = *update.ImportWatchedEnabled
	}
	if update.ImportProgressEnabled != nil {
		conn.ImportProgressEnabled = *update.ImportProgressEnabled
	}
	if update.ExportWatchedEnabled != nil {
		conn.ExportWatchedEnabled = *update.ExportWatchedEnabled
	}
	if update.ExportUnwatchedEnabled != nil {
		conn.ExportUnwatchedEnabled = *update.ExportUnwatchedEnabled
	}
	if update.ImportFavoritesEnabled != nil {
		conn.ImportFavoritesEnabled = *update.ImportFavoritesEnabled
	}
	if update.ExportFavoritesEnabled != nil {
		conn.ExportFavoritesEnabled = *update.ExportFavoritesEnabled
	}
	if update.SyncFavoriteRemovalsEnabled != nil {
		conn.SyncFavoriteRemovalsEnabled = *update.SyncFavoriteRemovalsEnabled
	}
	if update.ScrobbleEnabled != nil {
		conn.ScrobbleEnabled = *update.ScrobbleEnabled
	}
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return ConnectionStatus{}, err
	}
	return s.GetConnectionStatus(ctx, userID, profileID, providerKey)
}

func (s *Service) DeleteConnection(ctx context.Context, userID int, profileID string, providerKey string) error {
	return s.repo.DeleteConnection(ctx, providerKey, userID, profileID)
}

func (s *Service) RequestManualSync(ctx context.Context, userID int, profileID string, providerKey string) (ManualSyncResult, error) {
	conn, ok, err := s.repo.GetConnection(ctx, providerKey, userID, profileID)
	if err != nil {
		return ManualSyncResult{}, err
	}
	if !ok {
		return ManualSyncResult{}, fmt.Errorf("watch provider connection not found")
	}

	for {
		active, ok, err := s.repo.GetActiveSyncRun(ctx, conn.ID)
		if err != nil {
			return ManualSyncResult{}, err
		}
		if !ok {
			break
		}
		if !active.StartedAt.IsZero() && s.now().Sub(active.StartedAt) <= manualSyncTimeout {
			return ManualSyncResult{Run: active}, nil
		}
		active.Status = string(SyncRunStatusFailed)
		active.Error = "watch provider sync timed out before completion"
		if _, err := s.completeSyncRun(ctx, active); err != nil {
			return ManualSyncResult{}, err
		}
	}

	if latest, ok, err := s.repo.GetLatestSyncRun(ctx, conn.ID); err != nil {
		return ManualSyncResult{}, err
	} else if ok {
		reference := latest.StartedAt
		if latest.CompletedAt != nil {
			reference = *latest.CompletedAt
		}
		if retryAfter := retryAfterSeconds(s.now(), reference, manualSyncCooldown); retryAfter > 0 {
			return ManualSyncResult{}, SyncCooldownError{RetryAfterSeconds: retryAfter}
		}
	}

	run, err := s.repo.CreateSyncRun(ctx, SyncRun{
		ConnectionID: conn.ID,
		Trigger:      "manual",
		Status:       string(SyncRunStatusRunning),
		Provider:     conn.Provider,
		StartedAt:    s.now(),
	})
	if err != nil {
		return ManualSyncResult{}, err
	}

	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), manualSyncTimeout)
		defer cancel()
		if _, err := s.syncConnectionWithRun(runCtx, conn, run); err != nil {
			slog.Warn("manual watch provider sync failed", "provider", conn.Provider, "user_id", conn.UserID, "profile_id", conn.ProfileID, "error", err)
		}
	}()

	return ManualSyncResult{Run: run}, nil
}

func (s *Service) ListSyncRuns(ctx context.Context, userID int, profileID string, providerKey string, limit int) ([]SyncRun, error) {
	conn, ok, err := s.repo.GetConnection(ctx, providerKey, userID, profileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("watch provider connection not found")
	}
	return s.repo.ListSyncRuns(ctx, conn.ID, limit)
}

func (s *Service) HandleLocalWatchEvent(ctx context.Context, event LocalWatchEvent) error {
	if event.UserID == 0 || event.ProfileID == "" || len(event.Plays) == 0 {
		return nil
	}
	plays := make([]LocalPlay, 0, len(event.Plays))
	for _, play := range event.Plays {
		if play.ProviderItemKey == "" {
			play.ProviderItemKey = providerItemKeyForLocalPlay(play)
		}
		if play.ProviderItemKey == "" {
			continue
		}
		plays = append(plays, play)
	}
	if len(plays) == 0 {
		return nil
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.processLocalWatchEvent(bg, LocalWatchEvent{
			Kind:      event.Kind,
			UserID:    event.UserID,
			ProfileID: event.ProfileID,
			Plays:     plays,
		}); err != nil {
			slog.Warn("failed to dispatch local watch provider event", "kind", event.Kind, "user_id", event.UserID, "profile_id", event.ProfileID, "error", err)
		}
	}()
	return nil
}

func (s *Service) processLocalWatchEvent(ctx context.Context, event LocalWatchEvent) error {
	conns, err := s.repo.ListLocalWatchEventConnections(ctx, event.UserID, event.ProfileID, event.Kind)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		provider, ok := s.registry.Get(conn.Provider)
		if !ok {
			continue
		}
		cfg, err := s.serverConfig(ctx, conn.Provider)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		switch event.Kind {
		case LocalWatchEventMarkedWatched:
			if !provider.Capabilities().ExportWatched {
				continue
			}
			exporter, ok := provider.(WatchedExporter)
			if !ok {
				continue
			}
			if err := s.exportLocalPlays(ctx, conn, cfg, exporter, event.Plays); err != nil {
				s.recordLocalWatchEventError(ctx, conn, err)
			}
		case LocalWatchEventMarkedUnwatched:
			if !provider.Capabilities().ExportUnwatched {
				continue
			}
			remover, ok := provider.(UnwatchedExporter)
			if !ok {
				continue
			}
			if _, err := remover.RemoveHistory(ctx, cfg, conn, event.Plays); err != nil {
				s.recordLocalWatchEventError(ctx, conn, err)
				continue
			}
			now := s.now()
			conn.LastOutboundSyncAt = &now
			conn.LastError = ""
			if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) HandleLocalFavoriteEvent(ctx context.Context, event LocalFavoriteEvent) error {
	if event.UserID == 0 || event.ProfileID == "" || len(event.Favorites) == 0 {
		return nil
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.processLocalFavoriteEvent(bg, event); err != nil {
			slog.Warn("failed to dispatch local favorite provider event", "kind", event.Kind, "user_id", event.UserID, "profile_id", event.ProfileID, "error", err)
		}
	}()
	return nil
}

func (s *Service) processLocalFavoriteEvent(ctx context.Context, event LocalFavoriteEvent) error {
	conns, err := s.repo.ListFavoriteEventConnections(ctx, event.UserID, event.ProfileID, event.Kind)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		provider, ok := s.registry.Get(conn.Provider)
		if !ok {
			continue
		}
		cfg, err := s.serverConfig(ctx, conn.Provider)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		conn, err = s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		switch event.Kind {
		case LocalFavoriteEventAdded:
			exporter, ok := provider.(FavoriteExporter)
			if !ok || !provider.Capabilities().ExportFavorites {
				continue
			}
			if err := s.exportLocalFavorites(ctx, conn, cfg, exporter, event.Favorites); err != nil {
				s.recordLocalWatchEventError(ctx, conn, err)
			}
		case LocalFavoriteEventRemoved:
			now := s.now()
			for _, favorite := range event.Favorites {
				if err := s.repo.MarkFavoriteLocalRemoved(ctx, conn.ID, favorite.MediaItemID, now); err != nil {
					return err
				}
			}
			if !conn.SyncFavoriteRemovalsEnabled || !provider.Capabilities().RemoveFavorites {
				continue
			}
			remover, ok := provider.(FavoriteRemover)
			if !ok {
				continue
			}
			if _, err := remover.RemoveFavorites(ctx, cfg, conn, event.Favorites); err != nil {
				s.recordLocalWatchEventError(ctx, conn, err)
				continue
			}
			for _, favorite := range event.Favorites {
				if err := s.repo.MarkFavoriteRemoteRemoved(ctx, conn.ID, favorite.MediaItemID, now); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) exportLocalFavorites(ctx context.Context, conn Connection, cfg ServerConfig, exporter FavoriteExporter, favorites []LocalFavorite) error {
	states := make([]FavoriteState, 0, len(favorites))
	for _, favorite := range favorites {
		if favorite.ProviderItemKey == "" {
			favorite.ProviderItemKey = providerItemKeyForLocalFavorite(favorite)
		}
		if favorite.ProviderItemKey == "" {
			continue
		}
		favoritedAt := favorite.FavoritedAt
		if favoritedAt.IsZero() {
			favoritedAt = s.now()
		}
		states = append(states, FavoriteState{
			ConnectionID:    conn.ID,
			MediaItemID:     favorite.MediaItemID,
			ProviderItemKey: favorite.ProviderItemKey,
			Kind:            favorite.Kind,
			Title:           favorite.Title,
			Year:            favorite.Year,
			RemotePresent:   false,
			LocalPresent:    true,
			LastSeenLocalAt: &favoritedAt,
		})
	}
	if err := s.repo.UpsertFavoriteStates(ctx, states); err != nil {
		return err
	}
	result, err := exporter.ExportFavorites(ctx, cfg, conn, favorites)
	if err != nil {
		return err
	}
	now := s.now()
	sent := exportResultSentSet(result)
	for _, favorite := range favorites {
		if sent[favorite.MediaItemID] || sent[favorite.ProviderItemKey] {
			if err := s.repo.MarkFavoriteExported(ctx, conn.ID, favorite.MediaItemID, now); err != nil {
				return err
			}
		}
	}
	conn.LastFavoritesSyncAt = &now
	conn.LastError = ""
	_, err = s.repo.UpsertConnection(ctx, conn)
	return err
}

func (s *Service) recordLocalWatchEventError(ctx context.Context, conn Connection, err error) {
	if err == nil {
		return
	}
	conn.LastError = err.Error()
	if _, updateErr := s.repo.UpsertConnection(ctx, conn); updateErr != nil {
		slog.Warn("failed to record local watch provider event error", "provider", conn.Provider, "connection_id", conn.ID, "error", updateErr)
	}
}

func (s *Service) StartDeviceAuth(
	ctx context.Context,
	userID int,
	profileID string,
	providerKey string,
) (DeviceAuthSession, error) {
	if userID <= 0 {
		return DeviceAuthSession{}, fmt.Errorf("user id is required")
	}
	if profileID == "" {
		return DeviceAuthSession{}, fmt.Errorf("profile id is required")
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return DeviceAuthSession{}, fmt.Errorf("unknown provider %q", providerKey)
	}
	authProvider, ok := provider.(AuthProvider)
	if !ok {
		return DeviceAuthSession{}, fmt.Errorf("provider %q does not support auth", providerKey)
	}

	cfg, err := s.serverConfig(ctx, providerKey)
	if err != nil {
		return DeviceAuthSession{}, err
	}
	session, err := authProvider.StartDeviceAuth(ctx, cfg)
	if err != nil {
		return DeviceAuthSession{}, err
	}

	session.Provider = providerKey
	session.UserID = userID
	session.ProfileID = profileID
	return s.repo.UpsertAuthSession(ctx, session)
}

func (s *Service) PollDeviceAuth(
	ctx context.Context,
	userID int,
	profileID string,
	providerKey string,
	sessionID string,
) (Connection, error) {
	if userID <= 0 {
		return Connection{}, fmt.Errorf("user id is required")
	}
	if profileID == "" {
		return Connection{}, fmt.Errorf("profile id is required")
	}
	if sessionID == "" {
		return Connection{}, fmt.Errorf("auth session id is required")
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return Connection{}, fmt.Errorf("unknown provider %q", providerKey)
	}
	authProvider, ok := provider.(AuthProvider)
	if !ok {
		return Connection{}, fmt.Errorf("provider %q does not support auth", providerKey)
	}

	session, err := s.repo.GetAuthSession(ctx, sessionID)
	if err != nil {
		return Connection{}, err
	}
	if session.UserID != userID || session.ProfileID != profileID || session.Provider != providerKey {
		return Connection{}, fmt.Errorf("auth session does not match active profile")
	}
	if session.CompletedAt != nil {
		return Connection{}, fmt.Errorf("auth session is already completed")
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
		return Connection{}, fmt.Errorf("auth session has expired")
	}

	cfg, err := s.serverConfig(ctx, providerKey)
	if err != nil {
		return Connection{}, err
	}
	tokens, err := authProvider.PollDeviceAuth(ctx, cfg, session)
	if err != nil {
		return Connection{}, err
	}

	account, err := authProvider.LookupAccount(ctx, cfg, Connection{AccessToken: tokens.AccessToken})
	if err != nil {
		return Connection{}, err
	}
	conn, err := s.persistConnection(ctx, providerKey, userID, profileID, tokens, account)
	if err != nil {
		return Connection{}, err
	}

	completedAt := s.now()
	session.CompletedAt = &completedAt
	if _, err := s.repo.UpsertAuthSession(ctx, session); err != nil {
		return Connection{}, err
	}

	return conn, nil
}

func (s *Service) ConnectAPIKey(
	ctx context.Context,
	userID int,
	profileID string,
	providerKey string,
	apiKey string,
) (Connection, error) {
	if userID <= 0 {
		return Connection{}, fmt.Errorf("user id is required")
	}
	if profileID == "" {
		return Connection{}, fmt.Errorf("profile id is required")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return Connection{}, fmt.Errorf("api key is required")
	}
	provider, ok := s.registry.Get(providerKey)
	if !ok {
		return Connection{}, fmt.Errorf("unknown provider %q", providerKey)
	}
	authProvider, ok := provider.(APIKeyAuthProvider)
	if !ok {
		return Connection{}, fmt.Errorf("provider %q does not support api-key auth", providerKey)
	}

	tokens, account, err := authProvider.ConnectWithAPIKey(ctx, apiKey)
	if err != nil {
		return Connection{}, err
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		tokens.AccessToken = apiKey
	}
	return s.persistConnection(ctx, providerKey, userID, profileID, tokens, account)
}

func (s *Service) persistConnection(
	ctx context.Context,
	providerKey string,
	userID int,
	profileID string,
	tokens TokenSet,
	account ProviderAccount,
) (Connection, error) {
	conn, ok, err := s.repo.GetConnection(ctx, providerKey, userID, profileID)
	if err != nil {
		return Connection{}, err
	}
	if !ok {
		conn = Connection{
			ImportWatchedEnabled:   true,
			ImportProgressEnabled:  true,
			ExportWatchedEnabled:   true,
			ImportFavoritesEnabled: true,
			ExportFavoritesEnabled: true,
			ScrobbleEnabled:        true,
		}
	}
	conn.Provider = providerKey
	conn.UserID = userID
	conn.ProfileID = profileID
	conn.AccessToken = tokens.AccessToken
	conn.RefreshToken = tokens.RefreshToken
	conn.TokenExpiresAt = tokens.TokenExpiresAt
	conn.ProviderAccountID = account.ID
	conn.ProviderUsername = account.Username

	return s.repo.UpsertConnection(ctx, conn)
}

func (s *Service) SyncDueConnections(ctx context.Context) error {
	conns, err := s.repo.ListConnectionsDueForSync(ctx, s.now())
	if err != nil {
		return fmt.Errorf("list due watch provider connections: %w", err)
	}
	for _, conn := range conns {
		if err := s.SyncConnection(ctx, conn, "scheduled"); err != nil {
			slog.Warn("watch provider connection sync failed", "provider", conn.Provider, "user_id", conn.UserID, "profile_id", conn.ProfileID, "error", err)
		}
	}
	return nil
}

func (s *Service) SyncConnection(ctx context.Context, conn Connection, trigger string) (err error) {
	run := SyncRun{
		ConnectionID: conn.ID,
		Trigger:      trigger,
		Status:       string(SyncRunStatusRunning),
		Provider:     conn.Provider,
		StartedAt:    s.now(),
	}
	if conn.ID == "" {
		return fmt.Errorf("connection id is required")
	}
	unlock, ok := s.tryLock(conn.ID)
	if !ok {
		return fmt.Errorf("watch provider sync already running for connection %s", conn.ID)
	}
	defer unlock()

	run, err = s.repo.CreateSyncRun(ctx, run)
	if err != nil {
		return err
	}
	_, err = s.executeSyncRun(ctx, conn, run)
	return err
}

func (s *Service) syncConnectionWithRun(ctx context.Context, conn Connection, run SyncRun) (SyncRun, error) {
	if conn.ID == "" {
		return SyncRun{}, fmt.Errorf("connection id is required")
	}
	unlock, ok := s.tryLock(conn.ID)
	if !ok {
		run.Status = string(SyncRunStatusWarning)
		run.Warning = fmt.Sprintf("watch provider sync already running for connection %s", conn.ID)
		completed, err := s.completeSyncRun(ctx, run)
		if err != nil {
			return SyncRun{}, err
		}
		return completed, nil
	}
	defer unlock()

	return s.executeSyncRun(ctx, conn, run)
}

func (s *Service) tryLock(connectionID string) (func(), bool) {
	value, _ := s.locks.LoadOrStore(connectionID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

func (s *Service) executeSyncRun(ctx context.Context, conn Connection, run SyncRun) (SyncRun, error) {
	provider, ok := s.registry.Get(conn.Provider)
	if !ok {
		run.Status = string(SyncRunStatusFailed)
		run.Error = fmt.Sprintf("unknown provider %q", conn.Provider)
		completed, completeErr := s.completeSyncRun(ctx, run)
		if completeErr != nil {
			return SyncRun{}, completeErr
		}
		return completed, fmt.Errorf("%s", run.Error)
	}
	cfg, err := s.serverConfig(ctx, conn.Provider)
	if err != nil {
		run.Status = string(SyncRunStatusFailed)
		run.Error = err.Error()
		completed, completeErr := s.completeSyncRun(ctx, run)
		if completeErr != nil {
			return SyncRun{}, completeErr
		}
		return completed, err
	}
	caps := provider.Capabilities()
	if providerSyncNeedsAccessToken(caps) && strings.TrimSpace(conn.AccessToken) == "" {
		err := fmt.Errorf("%s connection is missing an access token; reconnect the provider", conn.Provider)
		run.Status = string(SyncRunStatusFailed)
		run.Error = err.Error()
		completed, completeErr := s.completeSyncRun(ctx, run)
		if completeErr != nil {
			return SyncRun{}, completeErr
		}
		return completed, err
	}
	conn, err = s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
	if err != nil {
		run.Status = string(SyncRunStatusFailed)
		run.Error = err.Error()
		completed, completeErr := s.completeSyncRun(ctx, run)
		if completeErr != nil {
			return SyncRun{}, completeErr
		}
		return completed, err
	}

	var flowErrors []string
	if conn.ImportWatchedEnabled && provider.Capabilities().ImportWatched {
		importer, ok := provider.(WatchedImporter)
		if !ok {
			flowErrors = append(flowErrors, fmt.Sprintf("provider %q does not implement watched import", conn.Provider))
		} else {
			result, err := s.ImportWatched(ctx, conn, cfg, importer)
			run.InboundWatchedFound = result.Found
			run.InboundWatchedImported = result.Imported
			run.Warning = appendWarning(run.Warning, result.Warnings)
			if err != nil {
				flowErrors = append(flowErrors, "watched import: "+err.Error())
			} else if refreshed, refreshErr := s.reloadConnection(ctx, conn); refreshErr != nil {
				flowErrors = append(flowErrors, "watched import connection refresh: "+refreshErr.Error())
			} else {
				conn = refreshed
			}
		}
	}
	if conn.ImportProgressEnabled && provider.Capabilities().ImportProgress {
		importer, ok := provider.(ProgressImporter)
		if !ok {
			flowErrors = append(flowErrors, fmt.Sprintf("provider %q does not implement progress import", conn.Provider))
		} else {
			result, err := s.ImportProgress(ctx, conn, cfg, importer)
			run.InboundProgressFound = result.Found
			run.InboundProgressImported = result.Imported
			run.Warning = appendWarning(run.Warning, result.Warnings)
			if err != nil {
				flowErrors = append(flowErrors, "progress import: "+err.Error())
			} else if refreshed, refreshErr := s.reloadConnection(ctx, conn); refreshErr != nil {
				flowErrors = append(flowErrors, "progress import connection refresh: "+refreshErr.Error())
			} else {
				conn = refreshed
			}
		}
	}
	if conn.ImportFavoritesEnabled && provider.Capabilities().ImportFavorites {
		importer, ok := provider.(FavoriteImporter)
		if !ok {
			flowErrors = append(flowErrors, fmt.Sprintf("provider %q does not implement favorites import", conn.Provider))
		} else {
			result, err := s.ImportFavorites(ctx, conn, cfg, importer)
			run.InboundFavoritesFound = result.Found
			run.InboundFavoritesImported = result.Imported
			run.Warning = appendWarning(run.Warning, result.Warnings)
			if err != nil {
				flowErrors = append(flowErrors, "favorites import: "+err.Error())
			} else if refreshed, refreshErr := s.reloadConnection(ctx, conn); refreshErr != nil {
				flowErrors = append(flowErrors, "favorites import connection refresh: "+refreshErr.Error())
			} else {
				conn = refreshed
			}
		}
	}
	if conn.ExportWatchedEnabled && provider.Capabilities().ExportWatched {
		exporter, ok := provider.(WatchedExporter)
		if !ok {
			flowErrors = append(flowErrors, fmt.Sprintf("provider %q does not implement watched export", conn.Provider))
		} else {
			result, err := s.ExportWatched(ctx, conn, cfg, exporter)
			run.OutboundFound = result.LocalFound
			run.OutboundSent = result.Sent
			if err != nil {
				flowErrors = append(flowErrors, "watched export: "+err.Error())
			} else if refreshed, refreshErr := s.reloadConnection(ctx, conn); refreshErr != nil {
				flowErrors = append(flowErrors, "watched export connection refresh: "+refreshErr.Error())
			} else {
				conn = refreshed
			}
		}
	}
	if conn.ExportFavoritesEnabled && provider.Capabilities().ExportFavorites {
		exporter, ok := provider.(FavoriteExporter)
		if !ok {
			flowErrors = append(flowErrors, fmt.Sprintf("provider %q does not implement favorites export", conn.Provider))
		} else {
			result, err := s.ExportFavorites(ctx, conn, cfg, exporter)
			run.OutboundFavoritesFound = result.LocalFound
			run.OutboundFavoritesSent = result.Sent
			run.Warning = appendWarning(run.Warning, result.Warnings)
			if err != nil {
				flowErrors = append(flowErrors, "favorites export: "+err.Error())
			} else if refreshed, refreshErr := s.reloadConnection(ctx, conn); refreshErr != nil {
				flowErrors = append(flowErrors, "favorites export connection refresh: "+refreshErr.Error())
			} else {
				conn = refreshed
			}
		}
	}
	if conn.SyncFavoriteRemovalsEnabled && provider.Capabilities().RemoveFavorites {
		remover, ok := provider.(FavoriteRemover)
		if !ok {
			flowErrors = append(flowErrors, fmt.Sprintf("provider %q does not implement favorites removal", conn.Provider))
		} else {
			removed, err := s.RemovePendingFavorites(ctx, conn, cfg, remover)
			run.FavoriteRemovalsSent = removed
			if err != nil {
				flowErrors = append(flowErrors, "favorites removal: "+err.Error())
			}
		}
	}

	if len(flowErrors) > 0 {
		run.Status = string(SyncRunStatusFailed)
		run.Error = strings.Join(flowErrors, "; ")
		completed, completeErr := s.completeSyncRun(ctx, run)
		if completeErr != nil {
			return SyncRun{}, completeErr
		}
		return completed, fmt.Errorf("%s", run.Error)
	}
	run.Status = string(SyncRunStatusSuccess)
	return s.completeSyncRun(ctx, run)
}

func (s *Service) reloadConnection(ctx context.Context, conn Connection) (Connection, error) {
	if conn.ID == "" {
		return conn, nil
	}
	refreshed, ok, err := s.repo.GetConnectionByID(ctx, conn.ID)
	if err != nil {
		return Connection{}, err
	}
	if !ok {
		return Connection{}, fmt.Errorf("watch provider connection %s not found", conn.ID)
	}
	return refreshed, nil
}

func providerSyncNeedsAccessToken(caps Capabilities) bool {
	return caps.ImportWatched ||
		caps.ImportProgress ||
		caps.ExportWatched ||
		caps.ExportUnwatched ||
		caps.ImportFavorites ||
		caps.ExportFavorites ||
		caps.RemoveFavorites ||
		caps.ScrobblePlayback
}

func (s *Service) completeSyncRun(ctx context.Context, run SyncRun) (SyncRun, error) {
	completedAt := s.now()
	run.CompletedAt = &completedAt
	return s.repo.CompleteSyncRun(ctx, run)
}

func retryAfterSeconds(now time.Time, reference time.Time, cooldown time.Duration) int {
	if reference.IsZero() {
		return 0
	}
	remaining := cooldown - now.Sub(reference)
	if remaining <= 0 {
		return 0
	}
	seconds := int(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func appendWarning(existing string, warnings []string) string {
	if len(warnings) == 0 {
		return existing
	}
	parts := make([]string, 0, len(warnings)+1)
	if existing != "" {
		parts = append(parts, existing)
	}
	parts = append(parts, summarizeWarnings(warnings)...)
	return strings.Join(parts, "; ")
}

const maxSyncWarningReasons = 20

func summarizeWarnings(warnings []string) []string {
	counts := make(map[string]int)
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		counts[warning]++
	}
	if len(counts) == 0 {
		return nil
	}
	type warningCount struct {
		reason string
		count  int
	}
	items := make([]warningCount, 0, len(counts))
	for reason, count := range counts {
		items = append(items, warningCount{reason: reason, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].reason < items[j].reason
	})
	limit := len(items)
	if limit > maxSyncWarningReasons {
		limit = maxSyncWarningReasons
	}
	summary := make([]string, 0, limit+1)
	for _, item := range items[:limit] {
		if item.count == 1 {
			summary = append(summary, item.reason)
			continue
		}
		summary = append(summary, fmt.Sprintf("%s (%d items)", item.reason, item.count))
	}
	if remaining := len(items) - limit; remaining > 0 {
		summary = append(summary, fmt.Sprintf("%d more unmatched reasons omitted", remaining))
	}
	return summary
}

type ImportWatchedResult struct {
	Found     int
	Imported  int
	Unmatched int
	Warnings  []string
}

func (s *Service) ImportWatched(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	importer WatchedImporter,
) (ImportWatchedResult, error) {
	if s.matcher == nil {
		return ImportWatchedResult{}, fmt.Errorf("watch provider matcher is not configured")
	}
	if s.watchState == nil {
		return ImportWatchedResult{}, fmt.Errorf("watch state service is not configured")
	}
	batch, err := fetchWatchedImportBatch(ctx, cfg, conn, importer)
	if err != nil {
		return ImportWatchedResult{}, err
	}
	rows := batch.Rows
	result := ImportWatchedResult{Found: len(rows), Warnings: append([]string{}, batch.Warnings...)}
	for _, row := range rows {
		match, reason, err := s.matcher.Match(ctx, row.HistoryRecord())
		if err != nil {
			return result, err
		}
		if match == nil {
			result.Unmatched++
			if reason != "" {
				result.Warnings = append(result.Warnings, reason)
			}
			continue
		}
		if row.LastWatchedAt == nil {
			continue
		}
		duration, _ := s.mediaDuration(ctx, match.MediaItemID)
		created, err := s.watchState.RecordImportedWatchIfNewerWithSource(
			ctx,
			conn.UserID,
			conn.ProfileID,
			match.MediaItemID,
			duration,
			0,
			true,
			*row.LastWatchedAt,
			row.LastWatchedAt,
			historySourceForProvider(importer),
		)
		if err != nil {
			return result, err
		}
		if created {
			result.Imported++
		}
	}
	now := s.now()
	conn.LastInboundSyncAt = &now
	conn.LastError = ""
	conn.SyncCursors = mergeSyncCursors(conn.SyncCursors, batch.UpdatedCursors)
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

func fetchWatchedImportBatch(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
	importer WatchedImporter,
) (WatchedImportBatch, error) {
	if batchImporter, ok := importer.(WatchedBatchImporter); ok {
		return batchImporter.FetchWatchedBatch(ctx, cfg, conn)
	}
	rows, err := importer.FetchWatched(ctx, cfg, conn)
	if err != nil {
		return WatchedImportBatch{}, err
	}
	return WatchedImportBatch{Rows: rows}, nil
}

func (s *Service) mediaDuration(ctx context.Context, mediaItemID string) (float64, error) {
	type durationResolver interface {
		GetMediaDuration(ctx context.Context, mediaItemID string) (float64, error)
	}
	resolver, ok := s.repo.(durationResolver)
	if !ok {
		return 0, nil
	}
	return resolver.GetMediaDuration(ctx, mediaItemID)
}

type ImportProgressResult struct {
	Found     int
	Imported  int
	Skipped   int
	Unmatched int
	Warnings  []string
}

func (s *Service) ImportProgress(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	importer ProgressImporter,
) (ImportProgressResult, error) {
	if s.matcher == nil {
		return ImportProgressResult{}, fmt.Errorf("watch provider matcher is not configured")
	}
	if s.storeProvider == nil {
		return ImportProgressResult{}, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return ImportProgressResult{}, fmt.Errorf("open user store: %w", err)
	}
	batch, err := fetchProgressImportBatch(ctx, cfg, conn, importer)
	if err != nil {
		return ImportProgressResult{}, err
	}
	rows := batch.Rows
	result := ImportProgressResult{Found: len(rows), Warnings: append([]string{}, batch.Warnings...)}
	for _, row := range rows {
		match, reason, err := s.matcher.Match(ctx, row.HistoryRecord())
		if err != nil {
			return result, err
		}
		if match == nil {
			result.Unmatched++
			if reason != "" {
				result.Warnings = append(result.Warnings, reason)
			}
			continue
		}
		duration, err := s.mediaDuration(ctx, match.MediaItemID)
		if err != nil {
			return result, err
		}
		if duration <= 0 {
			result.Skipped++
			continue
		}
		if newerHistory, err := hasVisibleCompletedHistoryAtOrAfter(ctx, store, conn.ProfileID, match.MediaItemID, row.PausedAt); err != nil {
			return result, err
		} else if newerHistory {
			result.Skipped++
			continue
		}
		position := duration * row.ProgressPercent / 100
		wrote, err := store.SetProgressIfNewer(ctx, conn.ProfileID, match.MediaItemID, position, duration, false, row.PausedAt)
		if err != nil {
			return result, err
		}
		if wrote {
			result.Imported++
		} else {
			result.Skipped++
		}
	}
	now := s.now()
	conn.LastProgressSyncAt = &now
	conn.LastError = ""
	conn.SyncCursors = mergeSyncCursors(conn.SyncCursors, batch.UpdatedCursors)
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

func fetchProgressImportBatch(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
	importer ProgressImporter,
) (ProgressImportBatch, error) {
	if batchImporter, ok := importer.(ProgressBatchImporter); ok {
		return batchImporter.FetchProgressBatch(ctx, cfg, conn)
	}
	rows, err := importer.FetchProgress(ctx, cfg, conn)
	if err != nil {
		return ProgressImportBatch{}, err
	}
	return ProgressImportBatch{Rows: rows}, nil
}

type ImportFavoritesResult struct {
	Found     int
	Imported  int
	Unmatched int
	Removed   int
	Warnings  []string
}

func (s *Service) ImportFavorites(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	importer FavoriteImporter,
) (ImportFavoritesResult, error) {
	if s.matcher == nil {
		return ImportFavoritesResult{}, fmt.Errorf("watch provider matcher is not configured")
	}
	if s.storeProvider == nil {
		return ImportFavoritesResult{}, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return ImportFavoritesResult{}, fmt.Errorf("open user store: %w", err)
	}
	batch, err := fetchFavoriteImportBatch(ctx, cfg, conn, importer)
	if err != nil {
		return ImportFavoritesResult{}, err
	}
	rows := batch.Rows
	result := ImportFavoritesResult{Found: len(rows), Warnings: append([]string{}, batch.Warnings...)}
	seenRemoteKeys := make(map[string]bool, len(rows))
	states := make([]FavoriteState, 0, len(rows))
	for _, row := range rows {
		match, reason, err := s.matcher.Match(ctx, row.HistoryRecord())
		if err != nil {
			return result, err
		}
		if match == nil {
			result.Unmatched++
			if reason != "" {
				result.Warnings = append(result.Warnings, reason)
			}
			continue
		}
		if err := store.AddFavoriteAt(ctx, conn.ProfileID, match.MediaItemID, row.FavoritedAt); err != nil {
			return result, err
		}
		result.Imported++
		key := row.ProviderItemKey
		if key == "" {
			key = providerItemKeyForRemoteFavorite(row)
		}
		if key != "" {
			seenRemoteKeys[key] = true
		}
		favoritedAt := row.FavoritedAt
		states = append(states, FavoriteState{
			ConnectionID:     conn.ID,
			MediaItemID:      match.MediaItemID,
			ProviderItemKey:  key,
			Kind:             row.Kind,
			Title:            row.Title,
			Year:             row.Year,
			RemotePresent:    true,
			LocalPresent:     true,
			LastSeenRemoteAt: &favoritedAt,
			LastSeenLocalAt:  &favoritedAt,
		})
	}
	if err := s.repo.UpsertFavoriteStates(ctx, states); err != nil {
		return result, err
	}
	if err := s.reconcileMissingRemoteFavorites(ctx, conn, store, seenRemoteKeys, &result); err != nil {
		return result, err
	}
	now := s.now()
	conn.LastFavoritesSyncAt = &now
	conn.LastError = ""
	conn.SyncCursors = mergeSyncCursors(conn.SyncCursors, batch.UpdatedCursors)
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

func fetchFavoriteImportBatch(
	ctx context.Context,
	cfg ServerConfig,
	conn Connection,
	importer FavoriteImporter,
) (FavoriteImportBatch, error) {
	if batchImporter, ok := importer.(FavoriteBatchImporter); ok {
		return batchImporter.FetchFavoritesBatch(ctx, cfg, conn)
	}
	rows, err := importer.FetchFavorites(ctx, cfg, conn)
	if err != nil {
		return FavoriteImportBatch{}, err
	}
	return FavoriteImportBatch{Rows: rows}, nil
}

func (s *Service) reconcileMissingRemoteFavorites(ctx context.Context, conn Connection, store userstore.UserStore, seenRemoteKeys map[string]bool, result *ImportFavoritesResult) error {
	states, err := s.repo.ListFavoriteStates(ctx, conn.ID)
	if err != nil {
		return err
	}
	now := s.now()
	for _, state := range states {
		if !state.RemotePresent || state.ProviderItemKey == "" || seenRemoteKeys[state.ProviderItemKey] {
			continue
		}
		if conn.SyncFavoriteRemovalsEnabled && state.LocalPresent {
			if err := store.RemoveFavorite(ctx, conn.ProfileID, state.MediaItemID); err != nil {
				return err
			}
			if err := s.repo.MarkFavoriteLocalRemoved(ctx, conn.ID, state.MediaItemID, now); err != nil {
				return err
			}
			result.Removed++
		}
		if err := s.repo.MarkFavoriteRemoteRemoved(ctx, conn.ID, state.MediaItemID, now); err != nil {
			return err
		}
	}
	return nil
}

func mergeSyncCursors(existing map[string]string, updates map[string]string) map[string]string {
	merged := make(map[string]string, len(existing)+len(updates))
	for key, value := range existing {
		merged[key] = value
	}
	for key, value := range updates {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		merged[key] = value
	}
	return merged
}

type completedHistoryLister interface {
	ListCompletedHistory(ctx context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error)
}

const completedHistoryPageSize = 500

func listAllCompletedHistory(ctx context.Context, store completedHistoryLister, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	var all []userstore.WatchHistoryEntry
	for offset := 0; ; offset += completedHistoryPageSize {
		query.Limit = completedHistoryPageSize
		query.Offset = offset
		rows, err := store.ListCompletedHistory(ctx, query)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) < completedHistoryPageSize {
			return all, nil
		}
	}
}

func hasVisibleCompletedHistoryAtOrAfter(ctx context.Context, store completedHistoryLister, profileID, mediaItemID string, at time.Time) (bool, error) {
	for offset := 0; ; offset += completedHistoryPageSize {
		rows, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{
			ProfileID:    profileID,
			MediaItemIDs: []string{mediaItemID},
			Limit:        completedHistoryPageSize,
			Offset:       offset,
		})
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			watchedAt, err := time.Parse(time.RFC3339, row.WatchedAt)
			if err != nil {
				continue
			}
			if !watchedAt.Before(at) {
				return true, nil
			}
		}
		if len(rows) < completedHistoryPageSize {
			return false, nil
		}
	}
}

const tokenRefreshSkew = 5 * time.Minute

func (s *Service) refreshConnectionIfNeeded(ctx context.Context, provider Provider, cfg ServerConfig, conn Connection) (Connection, error) {
	if conn.TokenExpiresAt == nil || conn.TokenExpiresAt.After(s.now().Add(tokenRefreshSkew)) {
		return conn, nil
	}
	if conn.RefreshToken == "" {
		return Connection{}, fmt.Errorf("watch provider token expired and refresh token is missing")
	}
	authProvider, ok := provider.(AuthProvider)
	if !ok {
		return Connection{}, fmt.Errorf("provider %q does not support token refresh", conn.Provider)
	}
	tokens, err := authProvider.RefreshToken(ctx, cfg, conn)
	if err != nil {
		return Connection{}, fmt.Errorf("refresh %s token: %w", conn.Provider, err)
	}
	if tokens.AccessToken != "" {
		conn.AccessToken = tokens.AccessToken
	}
	if tokens.RefreshToken != "" {
		conn.RefreshToken = tokens.RefreshToken
	}
	if tokens.TokenExpiresAt != nil {
		conn.TokenExpiresAt = tokens.TokenExpiresAt
	}
	conn.LastError = ""
	return s.repo.UpsertConnection(ctx, conn)
}

type ExportWatchedResult struct {
	LocalFound    int
	RemoteFound   int
	Queued        int
	RemotePresent int
	Sent          int
	Failed        int
}

type ExportFavoritesResult struct {
	LocalFound int
	Queued     int
	Sent       int
	Failed     int
	Warnings   []string
}

func (s *Service) ExportWatched(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	exporter WatchedExporter,
) (ExportWatchedResult, error) {
	if s.storeProvider == nil {
		return ExportWatchedResult{}, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return ExportWatchedResult{}, fmt.Errorf("open user store: %w", err)
	}
	remote, err := exporter.FetchHistory(ctx, cfg, conn)
	if err != nil {
		return ExportWatchedResult{}, err
	}
	result := ExportWatchedResult{RemoteFound: len(remote)}

	historyRows, err := listAllCompletedHistory(ctx, store, userstore.CompletedHistoryQuery{
		ProfileID:      conn.ProfileID,
		ExcludeSources: []userstore.WatchHistorySource{historySourceForProvider(exporter)},
	})
	if err != nil {
		return result, err
	}
	local := make([]LocalPlay, 0, len(historyRows))
	for _, row := range historyRows {
		play, ok := localPlayFromHistory(row)
		if !ok {
			continue
		}
		local = append(local, play)
	}
	result.LocalFound = len(local)

	exports := reconcileHistoryExports(conn.ID, local, remote)
	for _, export := range exports {
		if export.Status == "remote_present" {
			result.RemotePresent++
		} else if export.Status == "pending" {
			result.Queued++
		}
	}
	if err := s.repo.UpsertHistoryExports(ctx, exports); err != nil {
		return result, err
	}

	localByHistoryID := make(map[string]LocalPlay, len(local))
	for _, play := range local {
		localByHistoryID[play.HistoryID] = play
	}
	for {
		pending, err := s.repo.ListPendingHistoryExports(ctx, conn.ID, 100)
		if err != nil {
			return result, err
		}
		if len(pending) == 0 {
			break
		}
		pendingPlays := make([]LocalPlay, 0, len(pending))
		exportByHistoryID := make(map[string]HistoryExport, len(pending))
		progressed := false
		for _, export := range pending {
			play, ok := localByHistoryID[export.HistoryID]
			if !ok {
				if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "not_found", "local history entry not found"); err != nil {
					return result, err
				}
				progressed = true
				continue
			}
			pendingPlays = append(pendingPlays, play)
			exportByHistoryID[export.HistoryID] = export
		}
		if len(pendingPlays) == 0 {
			continue
		}
		exportResult, err := exporter.ExportHistory(ctx, cfg, conn, pendingPlays)
		if err != nil {
			for _, export := range pending {
				_ = s.repo.MarkHistoryExportStatus(ctx, export.ID, "failed", err.Error())
			}
			result.Failed += len(pending)
			return result, err
		}
		for _, historyID := range exportResult.Sent {
			export := exportByHistoryID[historyID]
			if export.ID == "" {
				continue
			}
			if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "sent", ""); err != nil {
				return result, err
			}
			result.Sent++
			progressed = true
		}
		for _, historyID := range exportResult.NotFound {
			export := exportByHistoryID[historyID]
			if export.ID == "" {
				continue
			}
			if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "not_found", "provider item not found"); err != nil {
				return result, err
			}
			progressed = true
		}
		for historyID, message := range exportResult.Failed {
			export := exportByHistoryID[historyID]
			if export.ID == "" {
				continue
			}
			if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "failed", message); err != nil {
				return result, err
			}
			result.Failed++
			progressed = true
		}
		if !progressed {
			break
		}
	}

	now := s.now()
	conn.LastOutboundSyncAt = &now
	conn.LastError = ""
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) ExportFavorites(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	exporter FavoriteExporter,
) (ExportFavoritesResult, error) {
	if s.storeProvider == nil {
		return ExportFavoritesResult{}, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return ExportFavoritesResult{}, fmt.Errorf("open user store: %w", err)
	}
	rows, err := store.ListFavorites(ctx, conn.ProfileID, 10000, 0)
	if err != nil {
		return ExportFavoritesResult{}, err
	}
	result := ExportFavoritesResult{LocalFound: len(rows)}
	favorites, states, warnings, err := s.localFavoritesFromRows(ctx, conn, rows)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	if err := s.repo.UpsertFavoriteStates(ctx, states); err != nil {
		return result, err
	}
	for {
		pending, err := s.repo.ListPendingFavoriteExports(ctx, conn.ID, 100)
		if err != nil {
			return result, err
		}
		if len(pending) == 0 {
			break
		}
		byMedia := make(map[string]LocalFavorite, len(favorites))
		for _, favorite := range favorites {
			byMedia[favorite.MediaItemID] = favorite
		}
		toSend := make([]LocalFavorite, 0, len(pending))
		for _, state := range pending {
			favorite, ok := byMedia[state.MediaItemID]
			if !ok {
				if err := s.repo.MarkFavoriteLocalRemoved(ctx, conn.ID, state.MediaItemID, s.now()); err != nil {
					return result, err
				}
				continue
			}
			toSend = append(toSend, favorite)
		}
		if len(toSend) == 0 {
			continue
		}
		result.Queued += len(toSend)
		exportResult, err := exporter.ExportFavorites(ctx, cfg, conn, toSend)
		if err != nil {
			for _, favorite := range toSend {
				_ = s.repo.MarkFavoriteError(ctx, conn.ID, favorite.MediaItemID, err.Error())
			}
			result.Failed += len(toSend)
			return result, err
		}
		now := s.now()
		sent := exportResultSentSet(exportResult)
		for _, favorite := range toSend {
			if sent[favorite.MediaItemID] || sent[favorite.ProviderItemKey] {
				if err := s.repo.MarkFavoriteExported(ctx, conn.ID, favorite.MediaItemID, now); err != nil {
					return result, err
				}
				result.Sent++
				continue
			}
			if containsString(exportResult.NotFound, favorite.MediaItemID) || containsString(exportResult.NotFound, favorite.ProviderItemKey) {
				msg := "favorite not found by provider"
				if err := s.repo.MarkFavoriteError(ctx, conn.ID, favorite.MediaItemID, msg); err != nil {
					return result, err
				}
				result.Warnings = append(result.Warnings, msg+": "+favorite.MediaItemID)
			}
		}
	}
	now := s.now()
	conn.LastFavoritesSyncAt = &now
	conn.LastError = ""
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) RemovePendingFavorites(ctx context.Context, conn Connection, cfg ServerConfig, remover FavoriteRemover) (int, error) {
	removed := 0
	for {
		pending, err := s.repo.ListPendingFavoriteRemovals(ctx, conn.ID, 100)
		if err != nil {
			return removed, err
		}
		if len(pending) == 0 {
			return removed, nil
		}
		favorites := make([]LocalFavorite, 0, len(pending))
		for _, state := range pending {
			favorites = append(favorites, LocalFavorite{
				MediaItemID:     state.MediaItemID,
				ProviderItemKey: state.ProviderItemKey,
				Kind:            state.Kind,
				Title:           state.Title,
				Year:            state.Year,
			})
		}
		result, err := remover.RemoveFavorites(ctx, cfg, conn, favorites)
		if err != nil {
			for _, favorite := range favorites {
				_ = s.repo.MarkFavoriteError(ctx, conn.ID, favorite.MediaItemID, err.Error())
			}
			return removed, err
		}
		now := s.now()
		sent := exportResultSentSet(result)
		for _, favorite := range favorites {
			if sent[favorite.MediaItemID] || sent[favorite.ProviderItemKey] {
				if err := s.repo.MarkFavoriteRemoteRemoved(ctx, conn.ID, favorite.MediaItemID, now); err != nil {
					return removed, err
				}
				removed++
			}
		}
	}
}

func (s *Service) localFavoritesFromRows(ctx context.Context, conn Connection, rows []userstore.Favorite) ([]LocalFavorite, []FavoriteState, []string, error) {
	ids := make([]string, 0, len(rows))
	addedAtByID := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MediaItemID)
		if addedAt, err := time.Parse(time.RFC3339, row.AddedAt); err == nil {
			addedAtByID[row.MediaItemID] = addedAt
		}
	}
	type favoriteMediaResolver interface {
		GetFavoriteMediaItems(ctx context.Context, mediaItemIDs []string) (map[string]LocalFavorite, error)
	}
	resolver, ok := s.repo.(favoriteMediaResolver)
	if !ok {
		return nil, nil, nil, fmt.Errorf("favorite media resolver is not configured")
	}
	items, err := resolver.GetFavoriteMediaItems(ctx, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	favorites := make([]LocalFavorite, 0, len(rows))
	states := make([]FavoriteState, 0, len(rows))
	var warnings []string
	for _, row := range rows {
		favorite, ok := items[row.MediaItemID]
		if !ok {
			warnings = append(warnings, "favorite media item not found: "+row.MediaItemID)
			continue
		}
		favorite.FavoritedAt = addedAtByID[row.MediaItemID]
		if favorite.FavoritedAt.IsZero() {
			favorite.FavoritedAt = s.now()
		}
		if favorite.Kind != historyimport.KindMovie && favorite.Kind != historyimport.KindSeries {
			warnings = append(warnings, "favorite kind is not supported by provider: "+row.MediaItemID)
			continue
		}
		if favorite.ProviderItemKey == "" {
			warnings = append(warnings, "favorite has no provider ids: "+row.MediaItemID)
			continue
		}
		favorites = append(favorites, favorite)
		favoritedAt := favorite.FavoritedAt
		states = append(states, FavoriteState{
			ConnectionID:    conn.ID,
			MediaItemID:     favorite.MediaItemID,
			ProviderItemKey: favorite.ProviderItemKey,
			Kind:            favorite.Kind,
			Title:           favorite.Title,
			Year:            favorite.Year,
			RemotePresent:   false,
			LocalPresent:    true,
			LastSeenLocalAt: &favoritedAt,
		})
	}
	return favorites, states, warnings, nil
}

func (s *Service) exportLocalPlays(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	exporter WatchedExporter,
	local []LocalPlay,
) error {
	exports := make([]HistoryExport, 0, len(local))
	for _, play := range local {
		if play.HistoryID == "" || play.ProviderItemKey == "" {
			continue
		}
		exports = append(exports, HistoryExport{
			ConnectionID:    conn.ID,
			HistoryID:       play.HistoryID,
			MediaItemID:     play.MediaItemID,
			WatchedAt:       play.WatchedAt,
			ProviderItemKey: play.ProviderItemKey,
			Status:          "pending",
		})
	}
	if len(exports) == 0 {
		return nil
	}
	if err := s.repo.UpsertHistoryExports(ctx, exports); err != nil {
		return err
	}
	pending, err := s.repo.ListPendingHistoryExports(ctx, conn.ID, 100)
	if err != nil {
		return err
	}
	localByHistoryID := make(map[string]LocalPlay, len(local))
	for _, play := range local {
		localByHistoryID[play.HistoryID] = play
	}
	exportByHistoryID := make(map[string]HistoryExport, len(pending))
	pendingPlays := make([]LocalPlay, 0, len(pending))
	for _, export := range pending {
		play, ok := localByHistoryID[export.HistoryID]
		if !ok {
			continue
		}
		pendingPlays = append(pendingPlays, play)
		exportByHistoryID[export.HistoryID] = export
	}
	if len(pendingPlays) == 0 {
		return nil
	}
	exportResult, err := exporter.ExportHistory(ctx, cfg, conn, pendingPlays)
	if err != nil {
		for _, export := range exportByHistoryID {
			_ = s.repo.MarkHistoryExportStatus(ctx, export.ID, "failed", err.Error())
		}
		return err
	}
	for _, historyID := range exportResult.Sent {
		export := exportByHistoryID[historyID]
		if export.ID == "" {
			continue
		}
		if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "sent", ""); err != nil {
			return err
		}
	}
	for _, historyID := range exportResult.NotFound {
		export := exportByHistoryID[historyID]
		if export.ID == "" {
			continue
		}
		if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "not_found", "provider item not found"); err != nil {
			return err
		}
	}
	for historyID, message := range exportResult.Failed {
		export := exportByHistoryID[historyID]
		if export.ID == "" {
			continue
		}
		if err := s.repo.MarkHistoryExportStatus(ctx, export.ID, "failed", message); err != nil {
			return err
		}
	}
	now := s.now()
	conn.LastOutboundSyncAt = &now
	conn.LastError = ""
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return err
	}
	return nil
}

func reconcileHistoryExports(connectionID string, local []LocalPlay, remote []RemotePlay) []HistoryExport {
	remoteExact := make(map[string]struct{}, len(remote))
	for _, play := range remote {
		remoteExact[remotePlayKey(play.ProviderItemKey, play.WatchedAt)] = struct{}{}
	}
	exports := make([]HistoryExport, 0, len(local))
	for _, play := range local {
		status := "pending"
		if _, ok := remoteExact[remotePlayKey(play.ProviderItemKey, play.WatchedAt)]; ok {
			status = "remote_present"
		}
		exports = append(exports, HistoryExport{
			ConnectionID:    connectionID,
			HistoryID:       play.HistoryID,
			MediaItemID:     play.MediaItemID,
			WatchedAt:       play.WatchedAt,
			ProviderItemKey: play.ProviderItemKey,
			Status:          status,
		})
	}
	return exports
}

func remotePlayKey(providerItemKey string, watchedAt time.Time) string {
	return providerItemKey + "|" + watchedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func localPlayFromHistory(row userstore.WatchHistoryEntry) (LocalPlay, bool) {
	watchedAt, err := time.Parse(time.RFC3339, row.WatchedAt)
	if err != nil || row.ID == "" {
		return LocalPlay{}, false
	}
	play := LocalPlay{
		HistoryID:       row.ID,
		MediaItemID:     row.MediaItemID,
		WatchedAt:       watchedAt,
		DurationSeconds: row.DurationSeconds,
		Source:          row.Source,
		Kind:            row.Identity.StableType,
		SeasonNumber:    intValue(row.Identity.Season),
		EpisodeNumber:   intValue(row.Identity.Episode),
	}
	if row.Identity.ProviderIDs != nil {
		play.IMDbID = row.Identity.ProviderIDs["imdb"]
		play.TMDBID = row.Identity.ProviderIDs["tmdb"]
		play.TVDBID = row.Identity.ProviderIDs["tvdb"]
	}
	if row.Identity.SeriesProviderIDs != nil {
		play.SeriesIMDbID = row.Identity.SeriesProviderIDs["imdb"]
		play.SeriesTMDBID = row.Identity.SeriesProviderIDs["tmdb"]
		play.SeriesTVDBID = row.Identity.SeriesProviderIDs["tvdb"]
	}
	play.ProviderItemKey = providerItemKeyForLocalPlay(play)
	return play, play.ProviderItemKey != ""
}

func LocalPlaysFromHistory(entries []userstore.WatchHistoryEntry) []LocalPlay {
	plays := make([]LocalPlay, 0, len(entries))
	for _, entry := range entries {
		play, ok := localPlayFromHistory(entry)
		if !ok {
			continue
		}
		plays = append(plays, play)
	}
	return plays
}

func providerItemKeyForLocalPlay(play LocalPlay) string {
	if play.Kind == historyimport.KindEpisode {
		switch {
		case play.TVDBID != "":
			return "tvdb:" + play.TVDBID
		case play.TMDBID != "":
			return "tmdb:" + play.TMDBID
		case play.SeriesTVDBID != "":
			return fmt.Sprintf("show:tvdb:%s:s%d:e%d", play.SeriesTVDBID, play.SeasonNumber, play.EpisodeNumber)
		case play.SeriesTMDBID != "":
			return fmt.Sprintf("show:tmdb:%s:s%d:e%d", play.SeriesTMDBID, play.SeasonNumber, play.EpisodeNumber)
		case play.SeriesIMDbID != "":
			return fmt.Sprintf("show:imdb:%s:s%d:e%d", play.SeriesIMDbID, play.SeasonNumber, play.EpisodeNumber)
		}
	}
	switch {
	case play.IMDbID != "":
		return "imdb:" + play.IMDbID
	case play.TMDBID != "":
		return "tmdb:" + play.TMDBID
	case play.TVDBID != "":
		return "tvdb:" + play.TVDBID
	default:
		return ""
	}
}

func providerItemKeyForLocalFavorite(favorite LocalFavorite) string {
	switch {
	case favorite.IMDbID != "":
		return "imdb:" + favorite.IMDbID
	case favorite.TMDBID != "":
		return "tmdb:" + favorite.TMDBID
	case favorite.TVDBID != "":
		return "tvdb:" + favorite.TVDBID
	default:
		return ""
	}
}

func providerItemKeyForRemoteFavorite(favorite RemoteFavorite) string {
	switch {
	case favorite.IMDbID != "":
		return "imdb:" + favorite.IMDbID
	case favorite.TMDBID != "":
		return "tmdb:" + favorite.TMDBID
	case favorite.TVDBID != "":
		return "tvdb:" + favorite.TVDBID
	default:
		return ""
	}
}

func exportResultSentSet(result ExportResult) map[string]bool {
	sent := make(map[string]bool, len(result.Sent))
	for _, value := range result.Sent {
		if value != "" {
			sent[value] = true
		}
	}
	return sent
}

func containsString(values []string, candidate string) bool {
	if candidate == "" {
		return false
	}
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func historySourceForProvider(provider any) userstore.WatchHistorySource {
	if sourceProvider, ok := provider.(HistorySourceProvider); ok {
		if source := sourceProvider.HistorySource(); source != "" {
			return source
		}
	}
	return userstore.WatchHistorySourceImport
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func (s *Service) ScrobbleStart(ctx context.Context, event ScrobbleEvent) error {
	return s.scrobble(ctx, event, "start")
}

func (s *Service) ScrobblePause(ctx context.Context, event ScrobbleEvent) error {
	return s.scrobble(ctx, event, "pause")
}

func (s *Service) ScrobbleStop(ctx context.Context, event ScrobbleEvent) error {
	return s.scrobble(ctx, event, "stop")
}

func (s *Service) scrobble(ctx context.Context, event ScrobbleEvent, action string) error {
	if event.PlaybackSessionID == "" || event.UserID == 0 || event.ProfileID == "" {
		return nil
	}
	conns, err := s.repo.ListScrobbleConnections(ctx, event.UserID, event.ProfileID)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		provider, ok := s.registry.Get(conn.Provider)
		if !ok || !provider.Capabilities().ScrobblePlayback {
			continue
		}
		scrobbler, ok := provider.(Scrobbler)
		if !ok {
			continue
		}
		if action == "start" {
			if err := s.repo.UpsertScrobbleSession(ctx, event, conn.ID, action); err != nil {
				return err
			}
		} else {
			if err := s.repo.UpdateScrobbleSession(ctx, event.PlaybackSessionID, conn.ID, action, event.PositionSeconds, event.HistoryID, "", nil); err != nil {
				return err
			}
		}
		cfg, err := s.serverConfig(ctx, conn.Provider)
		if err != nil {
			_ = s.repo.UpdateScrobbleSession(ctx, event.PlaybackSessionID, conn.ID, action, event.PositionSeconds, event.HistoryID, err.Error(), nil)
			continue
		}
		conn, err = s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
		if err != nil {
			_ = s.repo.UpdateScrobbleSession(ctx, event.PlaybackSessionID, conn.ID, action, event.PositionSeconds, event.HistoryID, err.Error(), nil)
			continue
		}
		s.dispatchScrobbleAsync(scrobbler, cfg, conn, event, action)
	}
	return nil
}

func (s *Service) dispatchScrobbleAsync(scrobbler Scrobbler, cfg ServerConfig, conn Connection, event ScrobbleEvent, action string) {
	if ordered, ok := scrobbler.(OrderedScrobbler); ok {
		key := ordered.ScrobbleOrderingKey(conn, event)
		if strings.TrimSpace(key) != "" {
			s.enqueueOrderedScrobble(key, func() {
				s.dispatchScrobble(context.Background(), scrobbler, cfg, conn, event, action)
			})
			return
		}
	}
	go s.dispatchScrobble(context.Background(), scrobbler, cfg, conn, event, action)
}

func (s *Service) enqueueOrderedScrobble(key string, dispatch func()) {
	value, _ := s.scrobbleQueues.LoadOrStore(key, &scrobbleQueue{})
	queue := value.(*scrobbleQueue)

	queue.mu.Lock()
	previous := queue.tail
	current := make(chan struct{})
	queue.tail = current
	queue.mu.Unlock()

	go func() {
		if previous != nil {
			<-previous
		}
		defer close(current)
		dispatch()
	}()
}

func (s *Service) dispatchScrobble(ctx context.Context, scrobbler Scrobbler, cfg ServerConfig, conn Connection, event ScrobbleEvent, action string) {
	var err error
	switch action {
	case "pause":
		err = scrobbler.Pause(ctx, cfg, conn, event)
	case "stop":
		err = scrobbler.Stop(ctx, cfg, conn, event)
	default:
		err = scrobbler.Start(ctx, cfg, conn, event)
	}
	if err != nil {
		_ = s.repo.UpdateScrobbleSession(ctx, event.PlaybackSessionID, conn.ID, action, event.PositionSeconds, event.HistoryID, err.Error(), nil)
		return
	}
	if action == "stop" {
		stopSentAt := s.now()
		_ = s.repo.UpdateScrobbleSession(ctx, event.PlaybackSessionID, conn.ID, action, event.PositionSeconds, event.HistoryID, "", &stopSentAt)
	}
}

func (s *Service) SweepOpenScrobbles(ctx context.Context) error {
	sessions, err := s.repo.ListOpenScrobbleSessions(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		conn, ok, err := s.repo.GetConnectionByID(ctx, session.ConnectionID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		provider, ok := s.registry.Get(conn.Provider)
		if !ok || !provider.Capabilities().ScrobblePlayback {
			continue
		}
		scrobbler, ok := provider.(Scrobbler)
		if !ok {
			continue
		}
		cfg, err := s.serverConfig(ctx, conn.Provider)
		if err != nil {
			_ = s.repo.UpdateScrobbleSession(ctx, session.PlaybackSessionID, session.ConnectionID, "stop", session.LastProgress, session.HistoryID, err.Error(), nil)
			continue
		}
		conn, err = s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
		if err != nil {
			_ = s.repo.UpdateScrobbleSession(ctx, session.PlaybackSessionID, session.ConnectionID, "stop", session.LastProgress, session.HistoryID, err.Error(), nil)
			continue
		}
		s.dispatchScrobble(ctx, scrobbler, cfg, conn, scrobbleEventFromSession(session, conn, s.now()), "stop")
	}
	return nil
}

func scrobbleEventFromSession(session ScrobbleSession, conn Connection, occurredAt time.Time) ScrobbleEvent {
	return ScrobbleEvent{
		PlaybackSessionID: session.PlaybackSessionID,
		UserID:            conn.UserID,
		ProfileID:         conn.ProfileID,
		MediaItemID:       session.MediaItemID,
		ProviderItemKey:   session.ProviderItemKey,
		Kind:              session.Kind,
		IMDbID:            session.IMDbID,
		TMDBID:            session.TMDBID,
		TVDBID:            session.TVDBID,
		SeriesIMDbID:      session.SeriesIMDbID,
		SeriesTMDBID:      session.SeriesTMDBID,
		SeriesTVDBID:      session.SeriesTVDBID,
		SeasonNumber:      session.SeasonNumber,
		EpisodeNumber:     session.EpisodeNumber,
		HistoryID:         session.HistoryID,
		PositionSeconds:   session.LastProgress,
		DurationSeconds:   session.DurationSeconds,
		Completed:         session.Completed,
		OccurredAt:        occurredAt,
	}
}

func authMethodOf(provider Provider) string {
	if _, ok := provider.(APIKeyAuthProvider); ok {
		return AuthMethodAPIKey
	}
	return AuthMethodDeviceCode
}

func (s *Service) serverConfig(ctx context.Context, providerKey string) (ServerConfig, error) {
	if provider, ok := s.registry.Get(providerKey); ok && authMethodOf(provider) == AuthMethodAPIKey {
		// API-key providers carry their credential on the connection itself
		// and don't consult server settings. Return a zero config so sync
		// callers that pass cfg through to provider methods keep working.
		return ServerConfig{}, nil
	}

	clientID, err := s.repo.GetServerSetting(ctx, "watchsync."+providerKey+".client_id")
	if err != nil {
		return ServerConfig{}, err
	}
	clientSecret, err := s.repo.GetServerSetting(ctx, "watchsync."+providerKey+".client_secret")
	if err != nil {
		return ServerConfig{}, err
	}

	cfg := ServerConfig{ClientID: clientID, ClientSecret: clientSecret}
	if !cfg.Configured() {
		return ServerConfig{}, fmt.Errorf("%s credentials are not configured", providerKey)
	}
	return cfg, nil
}

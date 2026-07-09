package historyimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// maxConcurrentRuns limits how many import runs execute simultaneously.
// Additional runs stay queued until a slot opens. This prevents overwhelming
// the external server and database when bulk-importing many users at once.
const maxConcurrentRuns = 5

type Service struct {
	repo       *Repository
	matcher    *Matcher
	emby       *EmbyClient
	jellyfin   *JellyfinClient
	plex       *PlexClient
	watchState *watchstate.Service
	stores     userstore.UserStoreProvider
	bgContext  context.Context

	// runSemaphore limits concurrent run goroutines to maxConcurrentRuns.
	runSemaphore chan struct{}

	// runCancels allows in-process cancellation of running import goroutines.
	runCancels   map[string]context.CancelFunc
	runCancelsMu sync.Mutex
	observers    []Observer
}

func NewService(bgContext context.Context, repo *Repository, storeProvider userstore.UserStoreProvider) *Service {
	if bgContext == nil {
		bgContext = context.Background()
	}
	service := &Service{
		repo:         repo,
		matcher:      NewMatcher(repo),
		emby:         NewEmbyClient(),
		jellyfin:     NewJellyfinClient(),
		plex:         NewPlexClient(),
		watchState:   watchstate.NewService(storeProvider),
		stores:       storeProvider,
		bgContext:    bgContext,
		runSemaphore: make(chan struct{}, maxConcurrentRuns),
		runCancels:   make(map[string]context.CancelFunc),
	}
	service.startStaleRunMonitor()
	return service
}

func (s *Service) SetStableIdentityResolver(identity *watchstate.StableIdentityResolver) {
	if s == nil || s.watchState == nil {
		return
	}
	s.watchState = s.watchState.WithStableIdentityResolver(identity)
}

func (s *Service) registerRunCancel(runID string, cancel context.CancelFunc) {
	s.runCancelsMu.Lock()
	s.runCancels[runID] = cancel
	s.runCancelsMu.Unlock()
}

func (s *Service) deregisterRunCancel(runID string) {
	s.runCancelsMu.Lock()
	delete(s.runCancels, runID)
	s.runCancelsMu.Unlock()
}

func (s *Service) cancelRunInProcess(runID string) {
	s.runCancelsMu.Lock()
	cancel, ok := s.runCancels[runID]
	s.runCancelsMu.Unlock()
	if ok {
		cancel()
	}
}

func (s *Service) ListUserSources(ctx context.Context) ([]Source, error) {
	if err := s.repo.DeleteExpiredConnectSessions(ctx); err != nil {
		return nil, err
	}
	return s.repo.ListEnabledSources(ctx)
}

func (s *Service) LoginConnect(ctx context.Context, userID int, input LoginConnectInput) (*ConnectSessionLoginResult, error) {
	if err := s.repo.DeleteExpiredConnectSessions(ctx); err != nil {
		return nil, err
	}
	authResp, err := s.emby.ConnectAuthenticate(ctx, input.Username, input.Password)
	if err != nil {
		return nil, err
	}
	servers, err := s.emby.ConnectServers(ctx, authResp.ConnectUserID, authResp.ConnectAccessToken)
	if err != nil {
		return nil, err
	}

	session, err := s.repo.CreateConnectSession(ctx, ConnectSession{
		ID:                 uuid.NewString(),
		UserID:             userID,
		ConnectUserID:      authResp.ConnectUserID,
		ConnectAccessToken: authResp.ConnectAccessToken,
		Servers:            servers,
		ExpiresAt:          time.Now().UTC().Add(connectSessionTTL),
	})
	if err != nil {
		return nil, err
	}
	return &ConnectSessionLoginResult{
		ConnectSessionID: session.ID,
		Servers:          toConnectServerResponses(session.Servers),
		ExpiresAt:        session.ExpiresAt,
	}, nil
}

func (s *Service) CreatePlexPin(ctx context.Context, userID int) (*PlexPinResponse, error) {
	if err := s.repo.DeleteExpiredPlexSessions(ctx); err != nil {
		return nil, err
	}
	pinID, pinCode, err := s.plex.CreatePin(ctx)
	if err != nil {
		return nil, err
	}
	session, err := s.repo.CreatePlexSession(ctx, PlexSession{
		ID:        uuid.NewString(),
		UserID:    userID,
		PinID:     fmt.Sprintf("%d", pinID),
		PinCode:   pinCode,
		ExpiresAt: time.Now().UTC().Add(connectSessionTTL),
	})
	if err != nil {
		return nil, err
	}
	authURL := fmt.Sprintf("https://app.plex.tv/auth#?clientID=%s&code=%s&context%%5Bdevice%%5D%%5Bproduct%%5D=%s",
		url.QueryEscape(plexClientIdentifier),
		url.QueryEscape(pinCode),
		url.QueryEscape(plexProduct),
	)
	return &PlexPinResponse{
		SessionID: session.ID,
		PinCode:   pinCode,
		AuthURL:   authURL,
		ExpiresAt: session.ExpiresAt,
	}, nil
}

func (s *Service) CheckPlexPin(ctx context.Context, userID int, sessionID string) (*PlexCheckResponse, error) {
	session, err := s.repo.GetPlexSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}

	if session.AuthToken != "" {
		return &PlexCheckResponse{
			Authenticated: true,
			Servers:       toPlexServerPublicList(session.Servers),
		}, nil
	}

	pinID, err := strconv.Atoi(session.PinID)
	if err != nil {
		return nil, fmt.Errorf("invalid pin ID in session: %w", err)
	}
	authToken, err := s.plex.CheckPin(ctx, pinID)
	if err != nil {
		return nil, err
	}
	if authToken == "" {
		return &PlexCheckResponse{Authenticated: false}, nil
	}

	servers, err := s.plex.GetResources(ctx, authToken)
	if err != nil {
		return nil, fmt.Errorf("discovering Plex servers: %w", err)
	}
	if err := s.repo.UpdatePlexSessionAuth(ctx, session.ID, authToken, servers); err != nil {
		return nil, err
	}
	return &PlexCheckResponse{
		Authenticated: true,
		Servers:       toPlexServerPublicList(servers),
	}, nil
}

func toPlexServerPublicList(servers []PlexServer) []PlexServerPublic {
	result := make([]PlexServerPublic, 0, len(servers))
	for _, s := range servers {
		result = append(result, PlexServerPublic{
			Name:             s.Name,
			ClientIdentifier: s.ClientIdentifier,
			Owned:            s.Owned,
			HasRemoteURL:     s.HasRemoteURL,
			HasLocalURL:      s.HasLocalURL,
		})
	}
	return result
}

func (s *Service) CreateRun(ctx context.Context, userID int, input CreateRunInput) (*Run, error) {
	if input.ProfileID == "" {
		return nil, fmt.Errorf("profile_id is required")
	}

	var sourceType, connectionMode string
	var provider Provider

	switch input.Source {
	case SourceTypeEmby:
		mode, err := resolveConnectionMode(input)
		if err != nil {
			return nil, err
		}
		if err := s.repo.DeleteExpiredConnectSessions(ctx); err != nil {
			return nil, err
		}
		auth, err := s.resolveAuth(ctx, userID, mode, input)
		if err != nil {
			return nil, err
		}
		sourceType = SourceTypeEmby
		connectionMode = mode
		provider = NewEmbyProvider(s.emby, *auth)

	case SourceTypeJellyfin:
		if input.JellyfinBaseURL == "" || input.JellyfinUsername == "" || input.JellyfinPassword == "" {
			return nil, fmt.Errorf("jellyfin_base_url, jellyfin_username, and jellyfin_password are required")
		}
		auth, err := s.jellyfin.AuthenticateServerUser(ctx, input.JellyfinBaseURL, input.JellyfinUsername, input.JellyfinPassword)
		if err != nil {
			return nil, err
		}
		sourceType = SourceTypeJellyfin
		connectionMode = ConnectionModeCustom
		provider = NewJellyfinProvider(s.jellyfin, *auth)

	case SourceTypePlex:
		auth, mode, err := s.resolvePlexAuth(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		sourceType = SourceTypePlex
		connectionMode = mode
		provider = NewPlexServerProvider(s.plex, auth.BaseURL, auth.Token).WithAccountToken(auth.AccountToken)

	default:
		return nil, fmt.Errorf("unsupported source type")
	}

	exists, err := s.repo.ProfileExistsForUser(ctx, userID, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrProfileNotFound
	}

	run := Run{
		ID:               uuid.NewString(),
		UserID:           userID,
		ProfileID:        input.ProfileID,
		SourceType:       sourceType,
		ConnectionMode:   connectionMode,
		Status:           RunStatusQueued,
		Warnings:         []string{},
		UnmatchedSamples: []UnmatchedSample{},
	}
	created, err := s.repo.CreateRun(ctx, run)
	if err != nil {
		return nil, err
	}
	s.notifyRun(created)

	go s.executeRun(created, provider)
	return created, nil
}

func (s *Service) resolveAuth(ctx context.Context, userID int, connectionMode string, input CreateRunInput) (*embyLocalAuth, error) {
	switch connectionMode {
	case ConnectionModeConnect:
		session, err := s.repo.GetConnectSession(ctx, userID, input.ConnectSessionID)
		if err != nil {
			return nil, err
		}
		var server *ConnectServer
		for i := range session.Servers {
			if session.Servers[i].ID == input.ServerID {
				server = &session.Servers[i]
				break
			}
		}
		if server == nil {
			return nil, fmt.Errorf("selected server not found in connect session")
		}
		baseURL := firstNonEmpty(server.URL, server.LocalAddress)
		if baseURL == "" {
			return nil, fmt.Errorf("selected server does not expose a usable address")
		}
		auth, err := s.emby.ConnectExchange(ctx, baseURL, session.ConnectUserID, server.AccessKey)
		if err != nil {
			return nil, err
		}
		if err := s.repo.ConsumeConnectSession(ctx, session.ID); err != nil {
			return nil, err
		}
		return auth, nil
	case ConnectionModePredefined:
		if input.SourceID <= 0 {
			return nil, fmt.Errorf("source_id is required")
		}
		if input.Username == "" || input.Password == "" {
			return nil, fmt.Errorf("username and password are required")
		}
		source, err := s.repo.GetSourceByID(ctx, input.SourceID)
		if err != nil {
			return nil, err
		}
		if !source.Enabled {
			return nil, fmt.Errorf("selected source is disabled")
		}
		return s.emby.AuthenticateServerUser(ctx, source.BaseURL, input.Username, input.Password)
	default:
		return nil, fmt.Errorf("invalid connection mode")
	}
}

func (s *Service) resolvePlexAuth(ctx context.Context, userID int, input CreateRunInput) (*plexAuth, string, error) {
	if err := s.repo.DeleteExpiredPlexSessions(ctx); err != nil {
		return nil, "", err
	}

	if input.PlexSessionID != "" {
		session, err := s.repo.GetPlexSession(ctx, userID, input.PlexSessionID)
		if err != nil {
			return nil, "", err
		}
		if session.AuthToken == "" {
			return nil, "", fmt.Errorf("plex OAuth not completed yet")
		}
		var server *PlexServer
		for i := range session.Servers {
			if session.Servers[i].ClientIdentifier == input.PlexServerID {
				server = &session.Servers[i]
				break
			}
		}
		if server == nil {
			return nil, "", fmt.Errorf("selected Plex server not found in session")
		}
		baseURL := firstNonEmpty(server.RemoteURL, server.LocalURL)
		if baseURL == "" {
			return nil, "", fmt.Errorf("selected Plex server has no usable address")
		}
		if err := s.repo.ConsumePlexSession(ctx, session.ID); err != nil {
			return nil, "", err
		}
		return &plexAuth{BaseURL: baseURL, Token: server.AccessToken, AccountToken: session.AuthToken}, ConnectionModePlexOAuth, nil
	}

	if input.PlexBaseURL != "" {
		if input.PlexToken == "" {
			return nil, "", fmt.Errorf("plex_token is required for browser Plex imports")
		}
		// Prefer the explicit account token: in the browser OAuth flow
		// PlexToken is a PMS access token, which account-level APIs (the
		// watchlist) reject. Falling back to PlexToken keeps manually pasted
		// plex.tv account tokens working.
		return &plexAuth{BaseURL: input.PlexBaseURL, Token: input.PlexToken, AccountToken: firstNonEmpty(input.PlexAccountToken, input.PlexToken)}, ConnectionModePlexOAuth, nil
	}
	if input.PlexToken != "" {
		return nil, "", fmt.Errorf("plex_base_url is required for browser Plex imports")
	}

	if input.SourceID > 0 {
		source, err := s.repo.GetSourceByID(ctx, input.SourceID)
		if err != nil {
			return nil, "", err
		}
		if !source.Enabled {
			return nil, "", fmt.Errorf("selected source is disabled")
		}
		if source.SourceType != SourceTypePlex {
			return nil, "", fmt.Errorf("source is not a Plex server")
		}
		if input.PlexToken == "" {
			return nil, "", fmt.Errorf("plex_token is required for predefined Plex sources")
		}
		return &plexAuth{BaseURL: source.BaseURL, Token: input.PlexToken, AccountToken: firstNonEmpty(input.PlexAccountToken, input.PlexToken)}, ConnectionModePredefined, nil
	}

	return nil, "", fmt.Errorf("plex_session_id, plex_base_url, or source_id is required for Plex imports")
}

// addToWatchlist puts a matched watchlist import onto the importing
// profile's watchlist (idempotent: the store insert is ON CONFLICT DO
// NOTHING, so re-imports do not duplicate or reorder entries).
func (s *Service) addToWatchlist(ctx context.Context, userID int, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	if s.stores == nil {
		return false, fmt.Errorf("watchlist import: user store provider not configured")
	}
	store, err := s.stores.ForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("watchlist import: open user store: %w", err)
	}
	inserted, err := store.AddToWatchlistAt(ctx, profileID, mediaItemID, addedAt)
	if err != nil {
		return false, fmt.Errorf("watchlist import: add %s: %w", mediaItemID, err)
	}
	return inserted, nil
}

func (s *Service) executeRun(run *Run, provider Provider) {
	ctx, cancel := newRunContext(s.bgContext)
	s.registerRunCancel(run.ID, cancel)
	defer s.deregisterRunCancel(run.ID)
	defer cancel()

	// Wait for a concurrency slot. The run stays in "queued" status until
	// a slot opens. If the context is cancelled (server shutdown or admin
	// cancel), the goroutine exits without executing.
	select {
	case s.runSemaphore <- struct{}{}:
		defer func() { <-s.runSemaphore }()
	case <-ctx.Done():
		slog.Info("history import: run cancelled while queued", "run_id", run.ID)
		return
	}

	summary := ExecutionSummary{
		Warnings:         []string{},
		UnmatchedSamples: []UnmatchedSample{},
	}
	if err := s.repo.MarkRunStarted(ctx, run.ID); err != nil {
		slog.Error("history import: failed to mark run started", "run_id", run.ID, "error", err)
		return
	}
	s.notifyRunByID(ctx, run.ID)
	stopHeartbeat := s.startRunHeartbeat(ctx, run.ID)
	defer stopHeartbeat()
	slog.Info(
		"history import: started",
		"run_id", run.ID,
		"user_id", run.UserID,
		"profile_id", run.ProfileID,
		"source_type", run.SourceType,
		"connection_mode", run.ConnectionMode,
	)

	records, warnings, err := provider.Fetch(ctx)
	if err != nil {
		summary.Warnings = append(summary.Warnings, warnings...)
		s.failRun(ctx, run.ID, summary, err)
		return
	}
	summary.Warnings = append(summary.Warnings, warnings...)
	summary.Fetched = len(records)
	slog.Info(
		"history import: fetched source records",
		"run_id", run.ID,
		"count", len(records),
	)
	s.persistProgress(ctx, run.ID, summary)

	for i, record := range records {
		match, reason, err := s.matcher.Match(ctx, record)
		if err != nil {
			summary.Warnings = append(summary.Warnings, err.Error())
			s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
			continue
		}
		if match == nil {
			summary.Unmatched++
			if reason != "" {
				if summary.UnmatchedReasonCounts == nil {
					summary.UnmatchedReasonCounts = make(map[string]int)
				}
				summary.UnmatchedReasonCounts[reason]++
			}
			if len(summary.UnmatchedSamples) < maxUnmatchedSamples {
				summary.UnmatchedSamples = append(summary.UnmatchedSamples, UnmatchedSample{
					Kind:   record.Kind,
					Title:  recordTitle(record),
					Year:   record.Year,
					Reason: reason,
				})
			}
			if summary.Unmatched <= maxUnmatchedLogSamples {
				s.logUnmatched(run.ID, i+1, len(records), record, reason)
			}
			s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
			continue
		}
		summary.Matched++

		// Watchlist entries carry no watch state: the matched item joins the
		// importing profile's watchlist and the progress pipeline is skipped.
		if record.Watchlisted {
			inserted, err := s.addToWatchlist(ctx, run.UserID, run.ProfileID, match.MediaItemID, record.UpdatedAt)
			if err != nil {
				summary.Warnings = append(summary.Warnings, err.Error())
			} else if inserted {
				summary.WatchlistAdded++
			}
			s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
			continue
		}

		shouldWriteProgress := true
		localProgress, err := s.repo.GetProgress(ctx, run.UserID, run.ProfileID, match.MediaItemID)
		if err != nil {
			summary.Warnings = append(summary.Warnings, err.Error())
			s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
			continue
		}
		if !shouldWriteImportedProgress(record, localProgress) {
			shouldWriteProgress = false
			summary.Skipped++
		}
		if shouldWriteProgress {
			created, err := s.watchState.RecordImportedWatch(
				ctx,
				run.UserID,
				run.ProfileID,
				match.MediaItemID,
				record.DurationSeconds,
				importedPosition(record),
				record.Played,
				record.UpdatedAt,
				record.LastPlayedAt,
			)
			if err != nil {
				summary.Warnings = append(summary.Warnings, err.Error())
				s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
				continue
			}
			summary.ProgressUpdated++
			if created {
				summary.HistoryCreated++
			}
		} else if record.LastPlayedAt != nil {
			created, err := s.watchState.RecordImportedHistory(
				ctx,
				run.UserID,
				run.ProfileID,
				match.MediaItemID,
				record.DurationSeconds,
				record.Played,
				record.LastPlayedAt,
			)
			if err != nil {
				summary.Warnings = append(summary.Warnings, err.Error())
				s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
				continue
			}
			if created {
				summary.HistoryCreated++
			}
		}
		s.persistProgressMaybe(ctx, run.ID, summary, i+1, len(records))
	}

	if err := s.repo.CompleteRun(ctx, run.ID, summary); err != nil {
		slog.Error("history import: failed to complete run", "run_id", run.ID, "error", err)
		return
	}
	s.notifyRunByID(ctx, run.ID)
	slog.Info(
		"history import: completed",
		"run_id", run.ID,
		"fetched", summary.Fetched,
		"matched", summary.Matched,
		"unmatched", summary.Unmatched,
		"progress_updated", summary.ProgressUpdated,
		"history_created", summary.HistoryCreated,
		"skipped", summary.Skipped,
		"warnings", len(summary.Warnings),
	)

	// Update the mapping's last_imported_at timestamp for admin-initiated runs.
	if run.MappingID != nil {
		if err := s.repo.TouchMappingLastImported(s.bgContext, *run.MappingID); err != nil {
			slog.Warn("history import: failed to touch mapping last_imported_at", "mapping_id", *run.MappingID, "error", err)
		}
	}
}

func (s *Service) failRun(ctx context.Context, runID string, summary ExecutionSummary, err error) {
	message := userFacingRunError(summary, err)
	if updateErr := s.repo.FailRun(ctx, runID, summary, message); updateErr != nil {
		slog.ErrorContext(ctx, "history import: failed to mark run failed", "component", "historyimport", "run_id", runID, "error", updateErr, "root_error", err)
		return
	}
	s.notifyRunByID(ctx, runID)
	slog.ErrorContext(ctx,
		"history import: failed", "component", "historyimport",
		"run_id", runID,
		"error", message,
		"root_error", err,
		"fetched", summary.Fetched,
		"matched", summary.Matched,
		"unmatched", summary.Unmatched,
		"progress_updated", summary.ProgressUpdated,
		"history_created", summary.HistoryCreated,
		"skipped", summary.Skipped,
	)
}

func userFacingRunError(summary ExecutionSummary, err error) string {
	if UpstreamHTTPStatus(err) == http.StatusUnauthorized {
		return "Couldn't connect to that server. Check the URL, username, and password and try again."
	}
	if summary.Fetched > 0 || summary.Matched > 0 || summary.ProgressUpdated > 0 || summary.HistoryCreated > 0 {
		return "Import stopped early. Some history may already be imported."
	}
	return "Import couldn't be completed. Please try again."
}

func (s *Service) ListRuns(ctx context.Context, userID, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	return s.repo.ListRunsForUser(ctx, userID, limit)
}

func (s *Service) ListActiveRuns(ctx context.Context, userID int) ([]Run, error) {
	return s.repo.ListActiveRunsForUser(ctx, userID)
}

func (s *Service) GetRun(ctx context.Context, userID int, runID string) (*Run, error) {
	return s.repo.GetRunForUser(ctx, userID, runID)
}

func (s *Service) ListAdminSources(ctx context.Context) ([]Source, error) {
	return s.repo.ListAdminSources(ctx)
}

func (s *Service) CreateSource(ctx context.Context, input CreateSourceInput) (*Source, error) {
	if input.Name == "" || input.BaseURL == "" {
		return nil, fmt.Errorf("name and base_url are required")
	}
	switch input.SourceType {
	case SourceTypeEmby, SourceTypeJellyfin, SourceTypePlex:
	default:
		return nil, fmt.Errorf("source_type must be emby, jellyfin, or plex")
	}
	return s.repo.CreateSource(ctx, input)
}

func (s *Service) UpdateSource(ctx context.Context, id int, input UpdateSourceInput) (*Source, error) {
	return s.repo.UpdateSource(ctx, id, input)
}

func (s *Service) DeleteSource(ctx context.Context, id int) error {
	return s.repo.DeleteSource(ctx, id)
}

func resolveConnectionMode(input CreateRunInput) (string, error) {
	if input.ServerURL != "" {
		return "", fmt.Errorf("direct server_url imports are no longer supported")
	}
	hasConnect := input.ConnectSessionID != ""
	hasSource := input.SourceID > 0
	if hasConnect == hasSource {
		return "", fmt.Errorf("exactly one of connect_session_id or source_id is required")
	}
	if hasConnect {
		return ConnectionModeConnect, nil
	}
	return ConnectionModePredefined, nil
}

func recordTitle(record Record) string {
	if record.Kind == KindEpisode && record.SeriesTitle != "" {
		return record.SeriesTitle
	}
	return record.Title
}

func importedPosition(record Record) float64 {
	if record.Played && record.DurationSeconds > 0 {
		return record.DurationSeconds
	}
	return record.PositionSeconds
}

func (s *Service) logUnmatched(runID string, processed, total int, record Record, reason string) {
	slog.Info(
		"history import: unmatched item",
		"run_id", runID,
		"processed", processed,
		"total", total,
		"kind", record.Kind,
		"title", record.Title,
		"year", record.Year,
		"series_title", record.SeriesTitle,
		"series_year", record.SeriesYear,
		"season_number", record.SeasonNumber,
		"episode_number", record.EpisodeNumber,
		"tmdb_id", record.TMDBID,
		"imdb_id", record.IMDbID,
		"tvdb_id", record.TVDBID,
		"series_tmdb_id", record.SeriesTMDBID,
		"series_imdb_id", record.SeriesIMDbID,
		"series_tvdb_id", record.SeriesTVDBID,
		"reason", reason,
	)
}

func IsNotFoundError(err error) bool {
	return errors.Is(err, ErrRunNotFound) ||
		errors.Is(err, ErrSourceNotFound) ||
		errors.Is(err, ErrProfileNotFound) ||
		errors.Is(err, ErrConnectSessionNotFound) ||
		errors.Is(err, ErrPlexSessionNotFound)
}

func shouldWriteImportedProgress(record Record, localProgress *localProgressRow) bool {
	if localProgress == nil {
		return true
	}
	if record.UpdatedAt.IsZero() {
		return false
	}
	return record.UpdatedAt.After(localProgress.UpdatedAt)
}

func toConnectServerResponses(servers []ConnectServer) []ConnectServerResponse {
	resp := make([]ConnectServerResponse, 0, len(servers))
	for _, server := range servers {
		resp = append(resp, ConnectServerResponse{
			ServerID:        server.ID,
			Name:            server.Name,
			SystemID:        server.SystemID,
			HasRemoteURL:    server.HasRemoteURL,
			HasLocalAddress: server.HasLocalURL,
		})
	}
	return resp
}

func (s *Service) persistProgress(ctx context.Context, runID string, summary ExecutionSummary) {
	if err := s.repo.UpdateRunProgress(ctx, runID, summary); err != nil && !errors.Is(err, ErrRunNotFound) {
		slog.WarnContext(ctx, "history import: failed to persist progress", "component", "historyimport", "run_id", runID, "error", err)
		return
	}
	s.notifyRunByID(ctx, runID)
}

func (s *Service) persistProgressMaybe(ctx context.Context, runID string, summary ExecutionSummary, processed, total int) {
	if processed == total || processed%25 == 0 {
		s.persistProgress(ctx, runID, summary)
		slog.InfoContext(ctx,
			"history import: progress", "component", "historyimport",
			"run_id", runID,
			"processed", processed,
			"total", total,
			"matched", summary.Matched,
			"unmatched", summary.Unmatched,
			"progress_updated", summary.ProgressUpdated,
			"history_created", summary.HistoryCreated,
			"skipped", summary.Skipped,
		)
	}
}

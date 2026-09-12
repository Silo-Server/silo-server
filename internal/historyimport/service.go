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

	"github.com/Silo-Server/silo-server/internal/workmetrics"

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
	runSemaphore   chan struct{}
	queueWake      chan struct{}
	backgroundOnce sync.Once

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
		queueWake:    make(chan struct{}, 1),
		runCancels:   make(map[string]context.CancelFunc),
	}
	return service
}

// StartBackgroundWork activates recovery and dispatch after the resolver and
// observers have been configured. Construction must not consume persisted jobs.
func (s *Service) StartBackgroundWork() {
	s.backgroundOnce.Do(func() { s.startStaleRunMonitor(); s.startImportQueue() })
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

	exists, err := s.repo.ProfileExistsForUser(ctx, userID, input.ProfileID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrProfileNotFound
	}

	// Reject an unavailable durable store before exchanging any credentials.
	if s.repo.cipher == nil {
		return nil, ErrPersonalCredentialsUnavailable
	}
	admission, err := s.preparePersonalRun(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	created, err := s.repo.enqueuePersonalRun(ctx, admission)
	if err != nil {
		return nil, err
	}
	s.notifyRun(created)
	s.wakeImportQueue()
	return created, nil
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

func (s *Service) addFavorite(ctx context.Context, userID int, profileID, mediaItemID string) (bool, error) {
	if s.stores == nil {
		return false, fmt.Errorf("favorites import: user store provider not configured")
	}
	store, err := s.stores.ForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("favorites import: open user store: %w", err)
	}
	inserted, err := store.AddFavoriteAt(ctx, profileID, mediaItemID, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("favorites import: add %s: %w", mediaItemID, err)
	}
	return inserted, nil
}

func (s *Service) executeRun(run *Run, provider Provider) {
	s.executeRunWithClaim(run, provider, RunClaim{RunID: run.ID}, false)
}

func (s *Service) executeRunWithClaim(run *Run, provider Provider, claim RunClaim, capacityHeld bool) {
	parent := s.bgContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	s.registerRunCancel(run.ID, func() { cancel(ErrRunCancellationRequested) })
	defer s.deregisterRunCancel(run.ID)
	defer cancel(nil)

	// Wait for a concurrency slot. The run stays in "queued" status until
	// a slot opens. If the context is cancelled (server shutdown or admin
	// cancel), the goroutine exits without executing.
	if !capacityHeld {
		select {
		case s.runSemaphore <- struct{}{}:
			defer func() { <-s.runSemaphore }()
		case <-ctx.Done():
			slog.Info("history import: run canceled while queued", "run_id", run.ID)
			return
		}

	}
	ctx, observation := workmetrics.Start(ctx, "history_import", run.CreatedAt)
	defer workmetrics.Profile(ctx)()
	defer observation.Finish("unknown")
	summary := ExecutionSummary{
		Warnings:         []string{},
		UnmatchedSamples: []UnmatchedSample{},
	}
	if claim.Generation == 0 {
		if err := s.repo.MarkRunStarted(ctx, run.ID); err != nil {
			slog.Error("history import: failed to mark run started", "run_id", run.ID, "error", err)
			return
		}
	}
	if err := s.repo.validateRunClaim(ctx, claim); err != nil {
		s.failClaim(ctx, claim, summary, err)
		return
	}
	s.notifyRunByID(ctx, run.ID)
	stopHeartbeat := s.startClaimHeartbeat(ctx, claim, cancel)
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
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		s.failClaim(ctx, claim, summary, err)
		return
	}
	summary.Warnings = append(summary.Warnings, warnings...)
	summary.Fetched = len(records)
	slog.Info(
		"history import: fetched source records",
		"run_id", run.ID,
		"count", len(records),
	)
	s.persistClaimProgress(ctx, claim, summary)

	for i, record := range records {
		if err := s.repo.validateRunClaim(ctx, claim); err != nil {
			s.failClaim(ctx, claim, summary, err)
			return
		}
		match, reason, err := s.matcher.Match(ctx, record)
		if err != nil {
			summary.Warnings = append(summary.Warnings, err.Error())
			s.persistClaimProgressMaybe(ctx, claim, summary, i+1, len(records))
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
			s.persistClaimProgressMaybe(ctx, claim, summary, i+1, len(records))
			continue
		}
		summary.Matched++

		if record.Favorite {
			if err := s.repo.validateRunClaim(ctx, claim); err != nil {
				s.failClaim(ctx, claim, summary, err)
				return
			}
			inserted, err := s.addFavorite(ctx, run.UserID, run.ProfileID, match.MediaItemID)
			if err != nil {
				summary.Warnings = append(summary.Warnings, err.Error())
			} else if inserted {
				summary.FavoritesImported++
			}
		}
		// Watchlist entries carry no watch state: the matched item joins the
		// importing profile's watchlist and the progress pipeline is skipped.
		if record.Watchlisted {
			if err := s.repo.validateRunClaim(ctx, claim); err != nil {
				s.failClaim(ctx, claim, summary, err)
				return
			}
			inserted, err := s.addToWatchlist(ctx, run.UserID, run.ProfileID, match.MediaItemID, record.UpdatedAt)
			if err != nil {
				summary.Warnings = append(summary.Warnings, err.Error())
			} else if inserted {
				summary.WatchlistAdded++
			}
			s.persistClaimProgressMaybe(ctx, claim, summary, i+1, len(records))
			continue
		}
		if record.FavoriteOnly {
			s.persistClaimProgressMaybe(ctx, claim, summary, i+1, len(records))
			continue
		}

		if err := s.repo.validateRunClaim(ctx, claim); err != nil {
			s.failClaim(ctx, claim, summary, err)
			return
		}
		updated, created, err := s.applyImportedWatch(ctx, run.UserID, run.ProfileID, match.MediaItemID, record)
		if err != nil {
			summary.Warnings = append(summary.Warnings, err.Error())
		} else {
			if updated {
				summary.ProgressUpdated++
			} else {
				summary.Skipped++
			}
			if created {
				summary.HistoryCreated++
			}
		}

		s.persistClaimProgressMaybe(ctx, claim, summary, i+1, len(records))
	}

	if err := s.repo.validateRunClaim(ctx, claim); err != nil {
		s.failClaim(ctx, claim, summary, err)
		return
	}
	if err := s.repo.completeRun(ctx, claim, summary); err != nil {
		s.failClaim(ctx, claim, summary, err)
		slog.Error("history import: failed to complete run", "run_id", run.ID, "error", err)
		return
	}
	observation.Finish("success")
	s.notifyRunByID(ctx, run.ID)
	slog.Info(
		"history import: completed",
		"run_id", run.ID,
		"fetched", summary.Fetched,
		"matched", summary.Matched,
		"unmatched", summary.Unmatched,
		"progress_updated", summary.ProgressUpdated,
		"history_created", summary.HistoryCreated,
		"favorites_imported", summary.FavoritesImported,
		"skipped", summary.Skipped,
		"warnings", len(summary.Warnings),
	)

	// Only the completed claim may update the unchanged mapping snapshot.
	if claim.Generation > 0 {
		if err := s.repo.touchCompletedMapping(ctx, claim); err != nil {
			slog.WarnContext(ctx, "history import: mapping timestamp update failed", "run_id", run.ID, "error", err)
		}
	}
	// Update the mapping's last_imported_at timestamp for legacy executions.
	if run.MappingID != nil && claim.Generation == 0 {
		if err := s.repo.TouchMappingLastImported(s.bgContext, *run.MappingID); err != nil {
			slog.Warn("history import: failed to touch mapping last_imported_at", "mapping_id", *run.MappingID, "error", err)
		}
	}
}

// applyImportedWatch uses the selected user store's atomic freshness guard.
// Unknown source timestamps sort before real activity, so replay never
// replaces progress merely because an import happened later.
func (s *Service) applyImportedWatch(ctx context.Context, userID int, profileID, itemID string, record Record) (bool, bool, error) {
	store, err := s.stores.ForUser(ctx, userID)
	if err != nil {
		return false, false, err
	}
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Unix(0, 0).UTC()
	}
	updated, err := store.SetProgressIfNewer(ctx, profileID, itemID, importedPosition(record), record.DurationSeconds, record.Played, updatedAt)
	if err != nil {
		return false, false, err
	}
	created, err := s.watchState.RecordImportedHistory(ctx, userID, profileID, itemID, record.DurationSeconds, record.Played, record.LastPlayedAt)
	return updated, created, err
}

func userFacingRunError(summary ExecutionSummary, err error) string {
	if UpstreamHTTPStatus(err) == http.StatusUnauthorized {
		return "Couldn't connect to that server. Check the URL, username, and password and try again."
	}
	if summary.Fetched > 0 || summary.Matched > 0 || summary.ProgressUpdated > 0 || summary.HistoryCreated > 0 || summary.FavoritesImported > 0 {
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

// ListRunsPage is the keyset listing behind v2: up to limit runs strictly
// older than after, newest first, and whether more follow.
func (s *Service) ListRunsPage(ctx context.Context, userID int, after *RunKey, limit int) ([]Run, bool, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.repo.ListRunsPageForUser(ctx, userID, after, limit)
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
	if err := validateSource(Source{Name: input.Name, BaseURL: input.BaseURL, SourceType: input.SourceType}); err != nil {
		return nil, err
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

func (s *Service) persistClaimProgress(ctx context.Context, claim RunClaim, summary ExecutionSummary) {
	if err := s.repo.updateRunProgress(ctx, claim, summary); err != nil {
		return
	}
	s.notifyRunByID(ctx, claim.RunID)
}
func (s *Service) persistClaimProgressMaybe(ctx context.Context, claim RunClaim, summary ExecutionSummary, processed, total int) {
	workmetrics.Progress("history_import")
	if processed == total || processed%25 == 0 {
		s.persistClaimProgress(ctx, claim, summary)
	}
}

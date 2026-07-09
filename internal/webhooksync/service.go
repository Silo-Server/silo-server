package webhooksync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type Service struct {
	repo       *Repository
	importRepo *historyimport.Repository
	matcher    *historyimport.Matcher
	watch      *watchstate.Service
	providers  map[string]Provider
}

func NewService(repo *Repository, importRepo *historyimport.Repository, storeProvider userstore.UserStoreProvider) *Service {
	plexClient := historyimport.NewPlexClient()
	return &Service{
		repo:       repo,
		importRepo: importRepo,
		matcher:    historyimport.NewMatcher(importRepo),
		watch:      watchstate.NewService(storeProvider),
		providers: map[string]Provider{
			ProviderPlex:     NewPlexProvider(plexClient),
			ProviderEmby:     NewEmbyProvider(),
			ProviderJellyfin: NewJellyfinProvider(),
		},
	}
}

func (s *Service) SetStableIdentityResolver(identity *watchstate.StableIdentityResolver) {
	if s == nil || s.watch == nil {
		return
	}
	s.watch = s.watch.WithStableIdentityResolver(identity)
}

func (s *Service) provider(id string) (Provider, error) {
	provider, ok := s.providers[id]
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", id)
	}
	return provider, nil
}

func (s *Service) ListConnections(ctx context.Context, userID int) ([]Connection, error) {
	connections, err := s.repo.ListConnections(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range connections {
		provider, providerErr := s.provider(connections[i].Provider)
		if providerErr != nil {
			continue
		}
		if _, available, err := provider.DiscoverUsers(ctx, &connections[i], nil); err == nil {
			connections[i].AccountDiscoveryAvailable = available
			if err := s.repo.SetDiscoveryAvailable(ctx, connections[i].ID, available); err != nil {
				slog.WarnContext(ctx, "webhook sync: failed to persist discovery availability", "component", "webhooksync", "connection_id", connections[i].ID, "error", err)
			}
		}
	}
	return connections, nil
}

func (s *Service) CreateConnection(ctx context.Context, userID int, input CreateConnectionInput, baseURL string) (*CreateConnectionResult, error) {
	provider, err := s.provider(input.Provider)
	if err != nil {
		return nil, err
	}
	if err := provider.ValidateCreateInput(input); err != nil {
		return nil, err
	}
	exists, err := s.repo.ProfileExistsForUser(ctx, userID, input.DefaultProfileID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, historyimport.ErrProfileNotFound
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	conn, err := s.repo.CreateConnection(ctx, Connection{
		ID:               uuid.NewString(),
		UserID:           userID,
		Provider:         input.Provider,
		ServerID:         input.ServerID,
		ServerName:       input.ServerName,
		BaseURL:          input.BaseURL,
		AccessToken:      input.AccessToken,
		DefaultProfileID: input.DefaultProfileID,
		WebhookSecret:    secret,
	})
	if err != nil {
		return nil, err
	}
	if userID, userName, ok, err := provider.DefaultUser(ctx, conn, input); err != nil {
		return nil, err
	} else if ok {
		if _, err := s.repo.CreateDefaultMapping(ctx, conn.ID, userID, userName, input.DefaultProfileID); err != nil {
			return nil, err
		}
	}

	return &CreateConnectionResult{
		Connection: *conn,
		WebhookURL: buildWebhookURL(baseURL, secret),
	}, nil
}

func (s *Service) UpdateConnection(ctx context.Context, userID int, id string, input UpdateConnectionInput) (*Connection, error) {
	if input.ServerName != nil && strings.TrimSpace(*input.ServerName) == "" {
		return nil, fmt.Errorf("server_name cannot be empty")
	}
	if input.DefaultProfileID != nil && *input.DefaultProfileID != "" {
		exists, err := s.repo.ProfileExistsForUser(ctx, userID, *input.DefaultProfileID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, historyimport.ErrProfileNotFound
		}
	}
	return s.repo.UpdateConnection(ctx, userID, id, input)
}

func (s *Service) DeleteConnection(ctx context.Context, userID int, id string) error {
	return s.repo.DeleteConnection(ctx, userID, id)
}

func (s *Service) RotateWebhook(ctx context.Context, userID int, id, baseURL string) (*RotateWebhookResult, error) {
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateWebhookSecret(ctx, userID, id, secret); err != nil {
		return nil, err
	}
	return &RotateWebhookResult{WebhookURL: buildWebhookURL(baseURL, secret)}, nil
}

func (s *Service) GetProfileMappings(ctx context.Context, userID int, id string) (*ProfileMappingsResponse, error) {
	conn, err := s.repo.GetConnection(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	mappings, err := s.repo.ListMappings(ctx, conn.ID)
	if err != nil {
		return nil, err
	}
	provider, err := s.provider(conn.Provider)
	if err != nil {
		return nil, err
	}
	discovered, available, discoverErr := provider.DiscoverUsers(ctx, conn, mappings)
	if discoverErr == nil {
		conn.AccountDiscoveryAvailable = available
		_ = s.repo.SetDiscoveryAvailable(ctx, conn.ID, available)
	} else {
		discovered = mappingsToDiscoveredUsers(mappings)
	}
	if len(discovered) == 0 {
		discovered = mappingsToDiscoveredUsers(mappings)
	}
	return &ProfileMappingsResponse{
		Mappings:                  mappings,
		DiscoveredUsers:           discovered,
		AccountDiscoveryAvailable: conn.AccountDiscoveryAvailable,
	}, nil
}

func (s *Service) UpdateProfileMappings(ctx context.Context, userID int, id string, input UpdateProfileMappingsInput) ([]ProfileMapping, error) {
	conn, err := s.repo.GetConnection(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	for _, mapping := range input.Mappings {
		if mapping.SiloProfileID == nil || *mapping.SiloProfileID == "" {
			continue
		}
		exists, err := s.repo.ProfileExistsForUser(ctx, userID, *mapping.SiloProfileID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, historyimport.ErrProfileNotFound
		}
	}
	return s.repo.ReplaceMappings(ctx, conn.ID, input.Mappings)
}

func (s *Service) ListEventLogs(ctx context.Context, userID int, id string, limit int) ([]WebhookEventLog, error) {
	conn, err := s.repo.GetConnection(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return s.repo.ListEventLogs(ctx, conn.ID, limit)
}

func (s *Service) CreateEventLog(ctx context.Context, entry WebhookEventLog) (*WebhookEventLog, error) {
	return s.repo.CreateEventLog(ctx, entry)
}

func (s *Service) ProcessWebhook(ctx context.Context, secret string, r *http.Request) (*ProcessWebhookResult, error) {
	conn, err := s.repo.GetConnectionBySecret(ctx, secret)
	if err != nil {
		return nil, err
	}
	result := &ProcessWebhookResult{
		ConnectionID: conn.ID,
		Provider:     conn.Provider,
	}
	provider, err := s.provider(conn.Provider)
	if err != nil {
		return s.failWebhook(ctx, conn.ID, result, err, "Provider configuration failed")
	}
	event, err := provider.ParseWebhook(ctx, conn, r)
	if err != nil {
		result.Outcome = OutcomeRejected
		result.Summary = "Rejected invalid " + conn.Provider + " webhook payload"
		result.ErrorMessage = err.Error()
		_ = s.repo.MarkWebhookError(ctx, conn.ID, err.Error())
		return result, err
	}
	if event != nil {
		result.EventKind = event.EventKind
		result.Action = event.Action
		result.UserID = event.UserID
		result.UserName = event.UserName
		result.ExternalItemID = event.ExternalItemID
		result.MediaKind = event.MediaKind
	}
	if err := s.repo.MarkWebhookReceived(ctx, conn.ID); err != nil {
		slog.WarnContext(ctx, "webhook sync: failed to mark receipt", "component", "webhooksync", "connection_id", conn.ID, "error", err)
	}
	if event == nil || !event.Apply {
		result.Outcome = OutcomeIgnored
		result.Summary = webhookResultSummary(event, "Ignored unsupported webhook event")
		return result, nil
	}
	if err := s.repo.UpsertSeenUser(ctx, conn.ID, event.UserID, event.UserName); err != nil {
		slog.WarnContext(ctx, "webhook sync: failed to upsert seen external user", "component", "webhooksync", "connection_id", conn.ID, "external_user_id", event.UserID, "error", err)
	}

	if mapping, err := s.repo.GetMappingByUser(ctx, conn.ID, event.UserID); err != nil {
		return s.failWebhook(ctx, conn.ID, result, err, "Failed to resolve profile mapping")
	} else if profileID, ok := resolveWebhookProfileID(mapping); ok {
		result.ProfileID = profileID
	} else {
		result.Outcome = OutcomeSkipped
		result.Summary = "Skipped because external user is not linked to a Silo profile"
		return result, nil
	}
	profileID := result.ProfileID

	record := event.Record.toHistoryImportRecord()
	match, _, err := s.matcher.Match(ctx, record)
	if err != nil {
		return s.failWebhook(ctx, conn.ID, result, err, "Failed to match webhook item against the catalog")
	}
	if match == nil {
		result.Outcome = OutcomeUnmatched
		result.Summary = "Received webhook event but found no matching Silo item"
		return result, nil
	}
	result.MatchedMediaItemID = match.MediaItemID
	result.MatchedMediaItemTitle = match.Title

	switch event.Action {
	case ActionMarkUnplayed:
		if state, err := s.repo.GetItemState(ctx, conn.ID, event.UserID, event.ExternalItemID); err != nil {
			return s.failWebhook(ctx, conn.ID, result, err, "Failed to load existing item state")
		} else if state != nil && !event.OccurredAt.After(state.LastEventAt) {
			result.Outcome = OutcomeSkipped
			result.Summary = "Skipped stale mark-unplayed event"
			return result, nil
		}
		if err := s.watch.RecordImportedMarkUnplayed(ctx, conn.UserID, profileID, match.MediaItemID, event.OccurredAt); err != nil {
			return s.failWebhook(ctx, conn.ID, result, err, "Failed to apply mark-unplayed event")
		}
		if err := s.repo.UpsertItemState(ctx, ItemState{
			ConnectionID:       conn.ID,
			ExternalUserID:     event.UserID,
			ExternalItemID:     event.ExternalItemID,
			MediaItemID:        match.MediaItemID,
			LastEventAt:        event.OccurredAt,
			LastCompleted:      false,
			LastPositionSecond: 0,
		}); err != nil {
			return s.failWebhook(ctx, conn.ID, result, err, "Failed to persist mark-unplayed item state")
		}
		result.Outcome = OutcomeApplied
		result.Summary = "Applied mark-unplayed event"
		return result, nil
	case ActionAddFavorite:
		if err := s.watch.SetFavorite(ctx, conn.UserID, profileID, match.MediaItemID, true); err != nil {
			return s.failWebhook(ctx, conn.ID, result, err, "Failed to add favorite from webhook event")
		}
		result.Outcome = OutcomeApplied
		result.Summary = "Applied favorite add event"
		return result, nil
	case ActionRemoveFavorite:
		if err := s.watch.SetFavorite(ctx, conn.UserID, profileID, match.MediaItemID, false); err != nil {
			return s.failWebhook(ctx, conn.ID, result, err, "Failed to remove favorite from webhook event")
		}
		result.Outcome = OutcomeApplied
		result.Summary = "Applied favorite removal event"
		return result, nil
	case ActionToggleFavorite:
		if _, err := s.watch.ToggleFavorite(ctx, conn.UserID, profileID, match.MediaItemID); err != nil {
			return s.failWebhook(ctx, conn.ID, result, err, "Failed to toggle favorite from webhook event")
		}
		result.Outcome = OutcomeApplied
		result.Summary = "Applied favorite toggle event"
		return result, nil
	case "", ActionImportProgress:
	default:
		result.Outcome = OutcomeIgnored
		result.Summary = "Ignored unsupported webhook action"
		return result, nil
	}

	localProgress, err := s.importRepo.GetProgress(ctx, conn.UserID, profileID, match.MediaItemID)
	if err == nil && localProgress != nil && !record.UpdatedAt.After(localProgress.UpdatedAt) {
		result.Outcome = OutcomeSkipped
		result.Summary = "Skipped because local watch progress is newer"
		return result, nil
	}

	state, err := s.repo.GetItemState(ctx, conn.ID, event.UserID, event.ExternalItemID)
	if err != nil {
		return s.failWebhook(ctx, conn.ID, result, err, "Failed to load existing item state")
	}
	if shouldSkipEvent(state, event) {
		result.Outcome = OutcomeSkipped
		result.Summary = "Skipped stale or duplicate progress event"
		return result, nil
	}

	_, err = s.watch.RecordImportedWatch(
		ctx,
		conn.UserID,
		profileID,
		match.MediaItemID,
		record.DurationSeconds,
		importedPosition(record),
		record.Played,
		record.UpdatedAt,
		record.LastPlayedAt,
	)
	if err != nil {
		return s.failWebhook(ctx, conn.ID, result, err, "Failed to apply imported watch progress")
	}

	if err := s.repo.UpsertItemState(ctx, ItemState{
		ConnectionID:       conn.ID,
		ExternalUserID:     event.UserID,
		ExternalItemID:     event.ExternalItemID,
		MediaItemID:        match.MediaItemID,
		LastEventAt:        event.OccurredAt,
		LastCompleted:      event.Completed,
		LastPositionSecond: event.PositionSeconds,
	}); err != nil {
		return s.failWebhook(ctx, conn.ID, result, err, "Failed to persist imported watch progress")
	}

	result.Outcome = OutcomeApplied
	result.Summary = "Applied watch progress event"
	return result, nil
}

func (s *Service) failWebhook(ctx context.Context, connectionID string, result *ProcessWebhookResult, err error, summary string) (*ProcessWebhookResult, error) {
	if result == nil {
		result = &ProcessWebhookResult{ConnectionID: connectionID}
	}
	result.Outcome = OutcomeError
	result.Summary = summary
	result.ErrorMessage = err.Error()
	_ = s.repo.MarkWebhookError(ctx, connectionID, err.Error())
	return result, err
}

func webhookResultSummary(event *CanonicalEvent, fallback string) string {
	if event != nil && strings.TrimSpace(event.Summary) != "" {
		return event.Summary
	}
	return fallback
}

func mappingsToDiscoveredUsers(mappings []ProfileMapping) []DiscoveredUser {
	if len(mappings) == 0 {
		return nil
	}
	out := make([]DiscoveredUser, 0, len(mappings))
	for _, mapping := range mappings {
		out = append(out, DiscoveredUser{
			ExternalUserID:   mapping.ExternalUserID,
			ExternalUserName: mapping.ExternalUserName,
		})
	}
	return out
}

func shouldSkipEvent(state *ItemState, event *CanonicalEvent) bool {
	if state == nil {
		return false
	}
	if event.OccurredAt.After(state.LastEventAt) {
		return false
	}
	if !state.LastCompleted && event.Completed {
		return false
	}
	if event.PositionSeconds >= state.LastPositionSecond+5 {
		return false
	}
	return true
}

func resolveWebhookProfileID(mapping *ProfileMapping) (string, bool) {
	if mapping == nil || mapping.SiloProfileID == nil || strings.TrimSpace(*mapping.SiloProfileID) == "" {
		return "", false
	}
	return *mapping.SiloProfileID, true
}

func buildWebhookURL(baseURL, secret string) string {
	if baseURL == "" {
		return webhookSyncPathPrefix + secret
	}
	return strings.TrimRight(baseURL, "/") + webhookSyncPathPrefix + secret
}

func randomSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func importedPosition(record historyimport.Record) float64 {
	if record.Played && record.DurationSeconds > 0 {
		return record.DurationSeconds
	}
	return record.PositionSeconds
}

func (r CanonicalRecord) toHistoryImportRecord() historyimport.Record {
	return historyimport.Record{
		ExternalID:      r.ExternalID,
		Kind:            r.Kind,
		Title:           r.Title,
		Year:            r.Year,
		IMDbID:          r.IMDbID,
		TMDBID:          r.TMDBID,
		TVDBID:          r.TVDBID,
		SeriesTitle:     r.SeriesTitle,
		SeriesYear:      r.SeriesYear,
		SeriesIMDbID:    r.SeriesIMDbID,
		SeriesTMDBID:    r.SeriesTMDBID,
		SeriesTVDBID:    r.SeriesTVDBID,
		SeasonNumber:    r.SeasonNumber,
		EpisodeNumber:   r.EpisodeNumber,
		Played:          r.Played,
		LastPlayedAt:    r.LastPlayedAt,
		PositionSeconds: r.PositionSeconds,
		DurationSeconds: r.DurationSeconds,
		UpdatedAt:       r.UpdatedAt,
	}
}

type Provider interface {
	ID() string
	ValidateCreateInput(input CreateConnectionInput) error
	DefaultUser(ctx context.Context, conn *Connection, input CreateConnectionInput) (externalUserID string, externalUserName string, ok bool, err error)
	DiscoverUsers(ctx context.Context, conn *Connection, mappings []ProfileMapping) ([]DiscoveredUser, bool, error)
	ParseWebhook(ctx context.Context, conn *Connection, r *http.Request) (*CanonicalEvent, error)
}

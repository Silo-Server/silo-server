package historyimport

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// AuthenticatePlex exchanges Plex username/password for an auth token via plex.tv.
func (s *Service) AuthenticatePlex(ctx context.Context, username, password string) (string, error) {
	if username == "" || password == "" {
		return "", fmt.Errorf("username and password are required")
	}
	return s.plex.Authenticate(ctx, username, password)
}

// SetSourceAdminToken stores an admin token for the given source.
func (s *Service) SetSourceAdminToken(ctx context.Context, sourceID int, token string) error {
	if token == "" {
		return fmt.Errorf("token must not be empty")
	}
	return s.repo.SetSourceAdminToken(ctx, sourceID, token)
}

// ClearSourceAdminToken removes the stored admin token from the given source.
func (s *Service) ClearSourceAdminToken(ctx context.Context, sourceID int) error {
	return s.repo.ClearSourceAdminToken(ctx, sourceID)
}

// DiscoverExternalUsers queries the external server for its user list using the
// source's stored admin token.
func (s *Service) DiscoverExternalUsers(ctx context.Context, sourceID int) ([]ExternalUser, error) {
	source, token, err := s.repo.GetSourceWithAdminToken(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNoAdminToken
	}

	switch source.SourceType {
	case SourceTypeEmby:
		return s.emby.ListUsers(ctx, source.BaseURL, token)
	case SourceTypeJellyfin:
		return s.jellyfin.ListUsers(ctx, source.BaseURL, token)
	case SourceTypePlex:
		return s.plex.ListAccounts(ctx, source.BaseURL, token)
	default:
		return nil, fmt.Errorf("unsupported source type: %s", source.SourceType)
	}
}

// CreateMapping persists a new (source user → Silo user + profile) mapping.
func (s *Service) CreateMapping(ctx context.Context, input CreateMappingInput) (*UserMapping, error) {
	if input.ExternalUserID == "" {
		return nil, fmt.Errorf("external_user_id is required")
	}
	if input.SiloUserID == 0 {
		return nil, fmt.Errorf("silo_user_id is required")
	}
	if input.SiloProfileID == "" {
		return nil, fmt.Errorf("silo_profile_id is required")
	}

	exists, err := s.repo.ProfileExistsForUser(ctx, input.SiloUserID, input.SiloProfileID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrProfileNotFound
	}

	return s.repo.CreateMapping(ctx, input)
}

// ListMappings returns all mappings for a source, enriched with Silo user/profile names.
func (s *Service) ListMappings(ctx context.Context, sourceID int) ([]UserMapping, error) {
	return s.repo.ListMappingsForSource(ctx, sourceID)
}

// UpdateMapping changes the Silo target of an existing mapping.
func (s *Service) UpdateMapping(ctx context.Context, id int, input UpdateMappingInput) (*UserMapping, error) {
	if input.SiloUserID != nil && input.SiloProfileID != nil {
		exists, err := s.repo.ProfileExistsForUser(ctx, *input.SiloUserID, *input.SiloProfileID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrProfileNotFound
		}
	}
	return s.repo.UpdateMapping(ctx, id, input)
}

// DeleteMapping removes a mapping.
func (s *Service) DeleteMapping(ctx context.Context, id int) error {
	return s.repo.DeleteMapping(ctx, id)
}

// GetMapping returns a single mapping by ID.
func (s *Service) GetMapping(ctx context.Context, id int) (*UserMapping, error) {
	return s.repo.GetMappingByID(ctx, id)
}

// CreateAdminRun triggers an import for a single mapping using the source's admin token.
func (s *Service) CreateAdminRun(ctx context.Context, mappingID int) (*Run, error) {
	mapping, err := s.repo.GetMappingByID(ctx, mappingID)
	if err != nil {
		return nil, err
	}

	// Prevent duplicate active runs for the same mapping.
	active, err := s.repo.HasActiveRunForMapping(ctx, mappingID)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, ErrActiveRunExists
	}

	source, token, err := s.repo.GetSourceWithAdminToken(ctx, mapping.SourceID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNoAdminToken
	}
	if !source.Enabled {
		return nil, fmt.Errorf("source is disabled")
	}

	provider, err := s.buildAdminProvider(source, token, mapping.ExternalUserID)
	if err != nil {
		return nil, err
	}

	run := Run{
		ID:               uuid.NewString(),
		UserID:           mapping.SiloUserID,
		ProfileID:        mapping.SiloProfileID,
		SourceType:       source.SourceType,
		ConnectionMode:   ConnectionModeAdminToken,
		Status:           RunStatusQueued,
		MappingID:        &mappingID,
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

// BulkCreateAdminRuns triggers imports for all eligible mappings on a source.
// Mappings that already have an active run are skipped. Errors on individual
// mappings are logged but do not abort the bulk operation.
func (s *Service) BulkCreateAdminRuns(ctx context.Context, sourceID int) (*BulkRunResult, error) {
	source, token, err := s.repo.GetSourceWithAdminToken(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNoAdminToken
	}
	if !source.Enabled {
		return nil, fmt.Errorf("source is disabled")
	}

	mappings, err := s.repo.ListMappingsForBulkRun(ctx, sourceID)
	if err != nil {
		return nil, err
	}

	result := &BulkRunResult{}
	for _, mapping := range mappings {
		mappingID := mapping.ID
		provider, err := s.buildAdminProvider(source, token, mapping.ExternalUserID)
		if err != nil {
			slog.ErrorContext(ctx, "history import bulk: failed to build provider", "component", "historyimport", "mapping_id", mappingID, "error", err)
			result.Errors++
			continue
		}

		run := Run{
			ID:               uuid.NewString(),
			UserID:           mapping.SiloUserID,
			ProfileID:        mapping.SiloProfileID,
			SourceType:       source.SourceType,
			ConnectionMode:   ConnectionModeAdminToken,
			Status:           RunStatusQueued,
			MappingID:        &mappingID,
			Warnings:         []string{},
			UnmatchedSamples: []UnmatchedSample{},
		}
		created, err := s.repo.CreateRun(ctx, run)
		if err != nil {
			slog.ErrorContext(ctx, "history import bulk: failed to create run", "component", "historyimport", "mapping_id", mappingID, "error", err)
			result.Errors++
			continue
		}
		s.notifyRun(created)
		go s.executeRun(created, provider)
		result.Runs = append(result.Runs, created)
	}
	return result, nil
}

// ListAdminRuns returns recent runs across all users. If sourceID is non-nil, only
// runs linked to that source (via mapping) are returned.
func (s *Service) ListAdminRuns(ctx context.Context, sourceID *int, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 200 {
		limit = 200
	}
	return s.repo.ListAllRuns(ctx, sourceID, limit)
}

func (s *Service) ListAdminActiveRuns(ctx context.Context, sourceID *int) ([]Run, error) {
	return s.repo.ListActiveRuns(ctx, sourceID)
}

// GetAdminRun returns any run by ID regardless of which user owns it.
func (s *Service) GetAdminRun(ctx context.Context, runID string) (*Run, error) {
	return s.repo.GetRunByID(ctx, runID)
}

// CancelAdminRun marks a queued or running run as failed and signals any in-process goroutine.
func (s *Service) CancelAdminRun(ctx context.Context, runID string) error {
	if err := s.repo.CancelRunIfActive(ctx, runID); err != nil {
		return err
	}
	// Signal in-process goroutine if it's running on this instance.
	s.cancelRunInProcess(runID)
	s.notifyRunByID(ctx, runID)
	return nil
}

// buildAdminProvider constructs the appropriate Provider for an admin-initiated run.
func (s *Service) buildAdminProvider(source *Source, adminToken, externalUserID string) (Provider, error) {
	switch source.SourceType {
	case SourceTypeEmby:
		auth := embyLocalAuth{
			BaseURL:     source.BaseURL,
			UserID:      externalUserID,
			AccessToken: adminToken,
		}
		return NewEmbyProvider(s.emby, auth), nil
	case SourceTypeJellyfin:
		auth := jellyfinLocalAuth{
			BaseURL:     source.BaseURL,
			UserID:      externalUserID,
			AccessToken: adminToken,
		}
		return NewJellyfinProvider(s.jellyfin, auth), nil
	case SourceTypePlex:
		return NewPlexAdminProvider(s.plex, source.BaseURL, adminToken, externalUserID), nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", source.SourceType)
	}
}

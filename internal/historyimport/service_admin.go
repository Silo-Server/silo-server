package historyimport

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/netguard"
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
	// An admin configured this server, so it may be on the local network.
	ctx = netguard.WithPrivateAccess(ctx)

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
	return s.repo.CreateMapping(ctx, input)
}

// ListMappings returns all mappings for a source, enriched with Silo user/profile names.
func (s *Service) ListMappings(ctx context.Context, sourceID int) ([]UserMapping, error) {
	return s.repo.ListMappingsForSource(ctx, sourceID)
}

// UpdateMapping changes the Silo target of an existing mapping.
func (s *Service) UpdateMapping(ctx context.Context, id int, input UpdateMappingInput) (*UserMapping, error) {
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

const BulkRunAccepted = "accepted"

// CreateAdminRun persists dispatch intent before notifying the local runner.
func (s *Service) CreateAdminRun(ctx context.Context, mappingID int) (*Run, error) {
	run, err := s.repo.EnqueueAdminRun(ctx, mappingID)
	if err != nil {
		return nil, err
	}
	s.notifyRun(run)
	s.wakeImportQueue()
	return run, nil
}

// BulkCreateAdminRuns reports every mapping in stable source order. The size
// check precedes all admissions; active mappings are outcomes, not omissions.
func (s *Service) BulkCreateAdminRuns(ctx context.Context, sourceID int) (*BulkRunResult, error) {
	ids, err := s.repo.bulkMappingIDs(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if _, err = s.repo.GetSourceByID(ctx, sourceID); err != nil {
		return nil, err
	}
	result := &BulkRunResult{Runs: []*Run{}, Outcomes: []BulkRunOutcome{}}
	for _, id := range ids {
		outcome := BulkRunOutcome{MappingID: id}
		run, err := s.CreateAdminRun(ctx, id)
		switch {
		case err == nil:
			outcome.Status = BulkRunAccepted
			outcome.Run = run
			result.Runs = append(result.Runs, run)
		case errors.Is(err, ErrActiveRunExists):
			outcome.Status = "active"
			result.Skipped++
			outcome.Run, _ = s.repo.activeRunForMapping(ctx, id)
		default:
			outcome.Status = "failed"
			outcome.Error = "Import could not be queued. Review this mapping and source before starting a new run."
			result.Errors++
			if errors.Is(err, ErrNoAdminToken) {
				outcome.Error = "Source has no admin token configured."
			}
			if errors.Is(err, ErrMappingNotFound) {
				outcome.Error = "Mapping no longer exists."
			}
		}
		result.Outcomes = append(result.Outcomes, outcome)
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

// CancelAdminRun persists cancellation and signals any in-process worker.
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

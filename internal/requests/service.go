package requests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"golang.org/x/sync/errgroup"
)

type TMDBClient interface {
	SearchMedia(ctx context.Context, mediaType, query string, page int) (*tmdb.MediaPage, error)
	DiscoverSection(ctx context.Context, section string, page int) (*tmdb.MediaPage, error)
	GetMediaDetail(ctx context.Context, mediaType string, id int) (*tmdb.MediaDetail, error)
	DiscoverPage(ctx context.Context, mediaType string, params tmdb.DiscoverParams, page int) (*tmdb.MediaPage, error)
}

type TMDBExternalIDClient interface {
	GetExternalIDs(ctx context.Context, mediaType string, id int) (*tmdb.ExternalIDs, error)
}

const externalIDHydrationConcurrency = 4

type SecretResolver interface {
	Get(ctx context.Context, key string) (string, error)
}

type MovieFulfillmentAdapter interface {
	SubmitMovie(ctx context.Context, req Request, integration Integration) (FulfillmentResult, error)
}

type SeriesFulfillmentAdapter interface {
	SubmitSeries(ctx context.Context, req Request, integration Integration) (FulfillmentResult, error)
}

type MovieStatusAdapter interface {
	CheckMovieStatus(ctx context.Context, req Request, integration Integration) (FulfillmentStatus, error)
}

type SeriesStatusAdapter interface {
	CheckSeriesStatus(ctx context.Context, req Request, integration Integration) (FulfillmentStatus, error)
}

type MovieIntegrationOptionsAdapter interface {
	ListMovieIntegrationOptions(ctx context.Context, integration Integration) (*IntegrationOptions, error)
}

type SeriesIntegrationOptionsAdapter interface {
	ListSeriesIntegrationOptions(ctx context.Context, integration Integration) (*IntegrationOptions, error)
}

type EntitlementResolver interface {
	// MaxPlaybackQuality returns the requester's effective playback-quality
	// ceiling (already combining account- and profile-level caps). Empty string
	// means "no cap".
	MaxPlaybackQuality(ctx context.Context, userID int, profileID string) (string, error)
}

type Service struct {
	store         Store
	tmdb          TMDBClient
	presence      PresenceResolver
	secrets       SecretResolver
	movieAdapter  MovieFulfillmentAdapter
	seriesAdapter SeriesFulfillmentAdapter
	entitlements  EntitlementResolver
	Now           func() time.Time
}

type DiscoverySection struct {
	Key          string        `json:"key"`
	Title        string        `json:"title"`
	Page         int           `json:"page"`
	TotalPages   int           `json:"total_pages"`
	TotalResults int           `json:"total_results"`
	Results      []MediaResult `json:"results"`
}

func NewService(store Store, tmdbClient TMDBClient, presence PresenceResolver) *Service {
	return &Service{
		store:    store,
		tmdb:     tmdbClient,
		presence: presence,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) SetSecretResolver(resolver SecretResolver) {
	s.secrets = resolver
}

func (s *Service) SetFulfillmentAdapters(movie MovieFulfillmentAdapter, series SeriesFulfillmentAdapter) {
	s.movieAdapter = movie
	s.seriesAdapter = series
}

func (s *Service) SetEntitlementResolver(r EntitlementResolver) { s.entitlements = r }

func (s *Service) requesterCeiling(ctx context.Context, userID int, profileID string) string {
	if s.entitlements == nil {
		return "" // no resolver -> unlimited (1080p baseline still applies)
	}
	q, err := s.entitlements.MaxPlaybackQuality(ctx, userID, profileID)
	if err != nil {
		return access.PlaybackQualityStandard // fail safe: HD only
	}
	return q
}

func (s *Service) Search(ctx context.Context, viewer Viewer, query string, mediaType MediaType, page int) (*MediaPage, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	mediaType, err := normalizeSearchMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidInput)
	}
	raw, err := s.tmdb.SearchMedia(ctx, string(mediaType), query, page)
	if err != nil {
		return nil, err
	}
	return s.enrichPage(ctx, viewer, raw)
}

func (s *Service) Discover(ctx context.Context, viewer Viewer, section string, page int) (*DiscoverySection, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	section = strings.TrimSpace(section)
	if _, ok := discoverySectionTitles[section]; !ok {
		return nil, fmt.Errorf("%w: invalid discovery section", ErrInvalidInput)
	}
	raw, err := s.tmdb.DiscoverSection(ctx, section, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, raw)
	if err != nil {
		return nil, err
	}
	return &DiscoverySection{
		Key:          section,
		Title:        discoverySectionTitles[section],
		Page:         enriched.Page,
		TotalPages:   enriched.TotalPages,
		TotalResults: enriched.TotalResults,
		Results:      enriched.Results,
	}, nil
}

func (s *Service) DiscoverAll(ctx context.Context, viewer Viewer) ([]DiscoverySection, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	sections := make([]DiscoverySection, len(discoverySectionOrder))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(externalIDHydrationConcurrency)
	for i, key := range discoverySectionOrder {
		i, key := i, key
		group.Go(func() error {
			section, err := s.Discover(gctx, viewer, key, 1)
			if err != nil {
				return err
			}
			sections[i] = *section
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return sections, nil
}

// GetDetail fetches a TMDB detail payload and overlays the same availability /
// request-state signals used by search and discovery. Recommendations carry
// their own per-item state so the detail page can render them as request cards.
func (s *Service) GetDetail(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID int) (*MediaDetail, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	if tmdbID <= 0 {
		return nil, fmt.Errorf("%w: tmdb id is required", ErrInvalidInput)
	}

	raw, err := s.tmdb.GetMediaDetail(ctx, string(mediaType), tmdbID)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrNotFound
	}

	policy, err := s.EffectivePolicy(ctx, viewer.UserID)
	if err != nil {
		return nil, err
	}

	primaryAvailable, err := s.lookupAvailable(ctx, mediaType, []int{raw.ID})
	if err != nil {
		return nil, err
	}
	primaryRequests, err := s.store.ListActiveByTMDB(ctx, mediaType, []int{raw.ID})
	if err != nil {
		return nil, err
	}

	detail := &MediaDetail{
		MediaType:           mediaType,
		TMDBID:              raw.ID,
		IMDbID:              raw.IMDbID,
		Title:               raw.Title,
		OriginalTitle:       raw.OriginalTitle,
		Tagline:             raw.Tagline,
		Overview:            raw.Overview,
		PosterPath:          raw.PosterPath,
		BackdropPath:        raw.BackdropPath,
		ReleaseDate:         raw.ReleaseDate,
		Year:                raw.Year,
		Runtime:             raw.Runtime,
		Genres:              raw.Genres,
		VoteAverage:         raw.VoteAverage,
		VoteCount:           raw.VoteCount,
		Status:              raw.Status,
		Homepage:            raw.Homepage,
		ContentRating:       raw.ContentRating,
		ProductionCompanies: raw.ProductionCompanies,
		NumberOfSeasons:     raw.NumberOfSeasons,
		NumberOfEpisodes:    raw.NumberOfEpisodes,
		FirstAirDate:        raw.FirstAirDate,
		LastAirDate:         raw.LastAirDate,
		Networks:            raw.Networks,
		Director:            raw.Director,
		Creators:            raw.Creators,
		Availability:        availabilityValue(primaryAvailable[raw.ID]),
		Request:             requestStateFor(viewer, policy, primaryAvailable[raw.ID], primaryRequests[raw.ID]),
	}
	if raw.TVDBID > 0 {
		tvdb := raw.TVDBID
		detail.TVDBID = &tvdb
	}
	if len(raw.Cast) > 0 {
		detail.Cast = make([]MediaCastMember, 0, len(raw.Cast))
		for _, member := range raw.Cast {
			detail.Cast = append(detail.Cast, MediaCastMember{
				Name:        member.Name,
				Character:   member.Character,
				ProfilePath: member.ProfilePath,
				Order:       member.Order,
			})
		}
	}

	if len(raw.Recommendations) > 0 {
		recPage := &tmdb.MediaPage{Results: raw.Recommendations}
		enriched, err := s.enrichPage(ctx, viewer, recPage)
		if err != nil {
			return nil, err
		}
		detail.Recommendations = enriched.Results
	}

	return detail, nil
}

func (s *Service) CreateRequest(ctx context.Context, viewer Viewer, input CreateRequestInput) (*Request, error) {
	if err := validateViewer(viewer); err != nil {
		return nil, err
	}
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}
	s.enrichExternalIDs(ctx, &normalized)
	isAnime := s.detectRequestAnime(ctx, normalized.MediaType, normalized.TMDBID)

	matches, err := s.lookupPresence(ctx, normalized.MediaType, []PresenceCandidate{createPresenceCandidate(normalized)})
	if err != nil {
		return nil, err
	}
	if matches[normalized.TMDBID].Available {
		return nil, ErrAlreadyAvailable
	}

	active, err := s.store.ListActiveByTMDB(ctx, normalized.MediaType, []int{normalized.TMDBID})
	if err != nil {
		return nil, err
	}
	if active[normalized.TMDBID] != nil {
		return nil, ErrAlreadyRequested
	}

	// Re-requesting media that previously failed (e.g., transient integration
	// error) should not leave stale failed rows behind in user/admin lists.
	if _, err := s.store.DeleteFailedByTMDB(ctx, normalized.MediaType, normalized.TMDBID); err != nil {
		return nil, err
	}

	policy, err := s.EffectivePolicy(ctx, viewer.UserID)
	if err != nil {
		return nil, err
	}
	if err := validateCreatePolicy(policy); err != nil {
		return nil, err
	}

	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	status := StatusPending
	if policy.AutoApprove {
		configured, err := s.integrationConfigured(ctx, normalized.MediaType)
		if err == nil && configured {
			status = StatusApproved
		}
	}
	record := CreateRequestRecord{
		ID:        id,
		Input:     normalized,
		Status:    status,
		Outcome:   OutcomeActive,
		IsAnime:   isAnime,
		Requester: viewer,
		Now:       s.now(),
	}
	if !policy.Unlimited {
		record.Quota = &QuotaCheck{
			UserID:      viewer.UserID,
			WindowStart: policy.WindowStart,
			MaxRequests: policy.MaxRequests,
		}
	}
	req, err := s.store.CreateRequest(ctx, record)
	if err != nil {
		if errors.Is(err, ErrAlreadyRequested) {
			return nil, ErrAlreadyRequested
		}
		if errors.Is(err, ErrQuotaExceeded) {
			return nil, QuotaError{
				Used:       policy.MaxRequests,
				Limit:      policy.MaxRequests,
				WindowDays: policy.WindowDays,
			}
		}
		return nil, err
	}
	if req.Status == StatusApproved {
		return s.submitApprovedRequest(ctx, *req, viewer)
	}
	return req, nil
}

func (s *Service) ListMine(ctx context.Context, viewer Viewer, filter ListFilter) ([]*Request, error) {
	if viewer.UserID == 0 {
		return nil, ErrForbidden
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	reqs, err := s.store.ListMine(ctx, viewer.UserID, normalizeListFilter(filter))
	if err != nil {
		return nil, err
	}
	if err := s.attachTargets(ctx, reqs...); err != nil {
		return nil, err
	}
	return reqs, nil
}

func (s *Service) ListAdmin(ctx context.Context, viewer Viewer, filter ListFilter) ([]*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	reqs, err := s.store.ListAdmin(ctx, normalizeListFilter(filter))
	if err != nil {
		return nil, err
	}
	if err := s.attachTargets(ctx, reqs...); err != nil {
		return nil, err
	}
	return reqs, nil
}

// attachTargets loads and attaches the per-instance fulfillment targets for each
// request so callers (admin queue, detail view) can surface multi-target status.
func (s *Service) attachTargets(ctx context.Context, reqs ...*Request) error {
	for _, r := range reqs {
		if r == nil {
			continue
		}
		targets, err := s.store.ListTargets(ctx, r.ID)
		if err != nil {
			return err
		}
		r.Targets = targets
	}
	return nil
}

func (s *Service) GetRequest(ctx context.Context, viewer Viewer, id string) (*Request, error) {
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !viewer.IsAdmin && req.RequestedByUserID != viewer.UserID {
		return nil, ErrForbidden
	}
	if err := s.attachTargets(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

func (s *Service) Approve(ctx context.Context, viewer Viewer, id string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if req.Outcome != OutcomeActive || req.Status != StatusPending {
		return nil, ErrInvalidState
	}
	approved, err := s.store.SetStatus(ctx, req.ID, StatusApproved, viewer)
	if err != nil {
		return nil, err
	}
	return s.submitApprovedRequest(ctx, *approved, viewer)
}

func (s *Service) Decline(ctx context.Context, viewer Viewer, id, reason string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	// Approved requests are pending submission by the reconciler; declining
	// while submission may be in flight risks a divergent external state.
	if req.Outcome != OutcomeActive ||
		req.Status == StatusApproved ||
		req.Status == StatusCompleted ||
		req.Status == StatusQueued ||
		req.Status == StatusDownloading ||
		strings.TrimSpace(req.ExternalID) != "" ||
		strings.TrimSpace(req.IntegrationKind) != "" {
		return nil, ErrInvalidState
	}
	return s.store.SetOutcome(ctx, req.ID, OutcomeDeclined, viewer, reason)
}

// Cancel withdraws a request that has not yet been submitted to a downstream
// integration. Owners can cancel their own pending requests; admins can cancel
// any active request that has not entered the fulfillment pipeline. Requests
// already approved, queued, downloading, or completed cannot be cancelled —
// callers should decline (admin) or wait for completion in those cases.
func (s *Service) Cancel(ctx context.Context, viewer Viewer, id, reason string) (*Request, error) {
	if viewer.UserID == 0 {
		return nil, ErrForbidden
	}
	if !viewer.IsAdmin {
		if err := s.ensureRequestsEnabled(ctx); err != nil {
			return nil, err
		}
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !viewer.IsAdmin && req.RequestedByUserID != viewer.UserID {
		return nil, ErrForbidden
	}
	if req.Outcome != OutcomeActive ||
		req.Status == StatusApproved ||
		req.Status == StatusCompleted ||
		req.Status == StatusQueued ||
		req.Status == StatusDownloading ||
		strings.TrimSpace(req.ExternalID) != "" ||
		strings.TrimSpace(req.IntegrationKind) != "" {
		return nil, ErrInvalidState
	}
	return s.store.SetOutcome(ctx, req.ID, OutcomeCancelled, viewer, reason)
}

func (s *Service) Retry(ctx context.Context, viewer Viewer, id string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if req.Outcome != OutcomeFailed {
		return nil, ErrInvalidState
	}
	active, err := s.store.SetOutcome(ctx, req.ID, OutcomeActive, viewer, "retry requested")
	if err != nil {
		return nil, err
	}

	instances, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.ListTargets(ctx, active.ID)
	if err != nil {
		return nil, err
	}
	ceiling := s.requesterCeiling(ctx, active.RequestedByUserID, active.RequestedByProfileID)
	planned := routeTargets(*active, ceiling, settings, instances)
	if len(planned) == 0 {
		return s.markSubmissionFailed(ctx, active.ID, viewer,
			fmt.Errorf("no %s instance configured for the requested quality",
				integrationKindForMediaType(active.MediaType)))
	}

	return s.submitPlannedTargets(ctx, *active, planned, existing, viewer)
}

// submitPlannedTargets submits each planned target idempotently against the
// already-recorded targets: a non-failed target for a quality is left alone, a
// failed one is deleted and re-submitted, and a missing one is submitted fresh.
// This is shared by Retry and the reconcile-driven submit so a re-run never
// violates the UNIQUE(request_id, quality) constraint.
func (s *Service) submitPlannedTargets(ctx context.Context, req Request, planned []plannedTarget, existing []Target, actor Viewer) (*Request, error) {
	hasOK := make(map[Quality]bool)
	for _, t := range existing {
		if t.Status != StatusFailed {
			hasOK[t.Quality] = true
		}
	}

	latest := &req
	for _, pt := range planned {
		if hasOK[pt.Quality] {
			continue // healthy target already exists for this quality
		}
		// remove the stale failed target for this quality before re-submitting
		for _, t := range existing {
			if t.Quality == pt.Quality && t.Status == StatusFailed {
				if err := s.store.DeleteTarget(ctx, t.ID); err != nil {
					return nil, err
				}
			}
		}
		updated, err := s.submitPlannedTarget(ctx, req, pt, actor)
		if err != nil {
			return nil, err
		}
		if updated != nil {
			latest = updated
		}
	}
	return latest, nil
}

func (s *Service) ReconcileRequests(ctx context.Context, limit int) (ReconcileResult, error) {
	if s == nil || s.store == nil {
		return ReconcileResult{}, fmt.Errorf("request service is not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	candidates, err := s.store.ListReconciliationCandidates(ctx, limit)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Checked: len(candidates)}
	for _, req := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		change, err := s.reconcileRequest(ctx, *req)
		if err != nil {
			slog.WarnContext(ctx, "request reconcile failed",
				"request_id", req.ID,
				"media_type", req.MediaType,
				"tmdb_id", req.TMDBID,
				"status", req.Status,
				"integration_kind", req.IntegrationKind,
				"err", err,
			)
			result.Errors++
			continue
		}
		switch change {
		case reconcileSubmitted:
			result.Submitted++
		case reconcileDownloading:
			result.Downloading++
		case reconcileCompleted:
			result.Completed++
		case reconcileFailed:
			result.Failed++
		case reconcileSkipped:
			result.Skipped++
		}
	}
	return result, nil
}

func (s *Service) GetSettings(ctx context.Context, viewer Viewer) (Settings, error) {
	if !viewer.IsAdmin {
		return Settings{}, ErrForbidden
	}
	return s.store.GetSettings(ctx)
}

func (s *Service) GetFeatureStatus(ctx context.Context, _ Viewer) (FeatureStatus, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return FeatureStatus{}, err
	}
	return FeatureStatus{RequestsEnabled: settings.RequestsEnabled}, nil
}

func (s *Service) ensureRequestsEnabled(ctx context.Context) error {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.RequestsEnabled {
		return ErrRequestsDisabled
	}
	return nil
}

func (s *Service) UpdateSettings(ctx context.Context, viewer Viewer, settings Settings) (Settings, error) {
	if !viewer.IsAdmin {
		return Settings{}, ErrForbidden
	}
	if settings.GlobalMaxRequests < 0 || settings.GlobalWindowDays <= 0 {
		return Settings{}, fmt.Errorf("%w: invalid request settings", ErrInvalidInput)
	}
	return s.store.UpdateSettings(ctx, settings)
}

func (s *Service) GetUserLimit(ctx context.Context, viewer Viewer, userID int) (*UserLimit, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	if userID <= 0 {
		return nil, fmt.Errorf("%w: invalid user id", ErrInvalidInput)
	}
	limit, err := s.store.GetUserLimit(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit != nil {
		return limit, nil
	}
	return &UserLimit{
		UserID:       userID,
		LimitMode:    LimitModeInherit,
		ApprovalMode: ApprovalModeInherit,
	}, nil
}

func (s *Service) UpsertUserLimit(ctx context.Context, viewer Viewer, limit UserLimit) (*UserLimit, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	normalized, err := normalizeUserLimit(limit)
	if err != nil {
		return nil, err
	}
	return s.store.UpsertUserLimit(ctx, normalized)
}

func (s *Service) ListIntegrations(ctx context.Context, viewer Viewer) ([]Integration, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	return s.store.ListIntegrations(ctx)
}

func (s *Service) CreateIntegration(ctx context.Context, viewer Viewer, in Integration) (*Integration, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	if err := validateInstance(&in); err != nil {
		return nil, err
	}
	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	in.ID = id
	return s.store.SaveIntegrationWithDefaults(ctx, in, true)
}

func (s *Service) UpdateIntegration(ctx context.Context, viewer Viewer, in Integration) (*Integration, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	if strings.TrimSpace(in.ID) == "" {
		return nil, fmt.Errorf("%w: integration id required", ErrInvalidInput)
	}
	if err := validateInstance(&in); err != nil {
		return nil, err
	}
	return s.store.SaveIntegrationWithDefaults(ctx, in, false)
}

func (s *Service) DeleteIntegration(ctx context.Context, viewer Viewer, id string) error {
	if !viewer.IsAdmin {
		return ErrForbidden
	}
	return s.store.DeleteIntegration(ctx, strings.TrimSpace(id))
}

func validateInstance(in *Integration) error {
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind != "radarr" && in.Kind != "sonarr" {
		return fmt.Errorf("%w: kind must be radarr or sonarr", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if in.IsDefault && in.Is4K {
		return fmt.Errorf("%w: the HD default cannot be a 4K server", ErrInvalidInput)
	}
	if in.IsDefault4K && !in.Is4K {
		return fmt.Errorf("%w: the 4K default must be a 4K server", ErrInvalidInput)
	}
	return nil
}

func (s *Service) LoadIntegrationOptions(ctx context.Context, viewer Viewer, integration Integration) (*IntegrationOptions, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	// For a saved instance the request body carries only the path id (no kind and
	// often no creds), so resolve the saved row by id and backfill what the body
	// omitted. This makes "Test connection" reuse the correct per-instance key
	// (each kind can have multiple instances) instead of borrowing a sibling's.
	if id := strings.TrimSpace(integration.ID); id != "" && id != "new" {
		stored, err := s.store.GetIntegration(ctx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if stored != nil {
			if strings.TrimSpace(integration.Kind) == "" {
				integration.Kind = stored.Kind
			}
			if strings.TrimSpace(integration.BaseURL) == "" {
				integration.BaseURL = stored.BaseURL
			}
			if strings.TrimSpace(integration.APIKeyRef) == "" {
				integration.APIKeyRef = stored.APIKeyRef
			}
		}
	}

	normalized, err := normalizeIntegrationConnection(integration)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(normalized.BaseURL) == "" {
		return nil, fmt.Errorf("%w: base_url is required", ErrInvalidInput)
	}
	if strings.TrimSpace(normalized.APIKeyRef) == "" {
		return nil, fmt.Errorf("%w: api_key_ref is required", ErrInvalidInput)
	}
	resolved := normalized
	apiKey, err := s.resolveAPIKey(ctx, resolved)
	if err != nil {
		return nil, err
	}
	resolved.APIKeyRef = apiKey

	switch resolved.Kind {
	case "radarr":
		adapter, ok := s.movieAdapter.(MovieIntegrationOptionsAdapter)
		if !ok {
			return nil, fmt.Errorf("request radarr integration options are not configured")
		}
		return adapter.ListMovieIntegrationOptions(ctx, resolved)
	case "sonarr":
		adapter, ok := s.seriesAdapter.(SeriesIntegrationOptionsAdapter)
		if !ok {
			return nil, fmt.Errorf("request sonarr integration options are not configured")
		}
		return adapter.ListSeriesIntegrationOptions(ctx, resolved)
	default:
		return nil, fmt.Errorf("%w: invalid integration kind", ErrInvalidInput)
	}
}

func (s *Service) EffectivePolicy(ctx context.Context, userID int) (EffectivePolicy, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return EffectivePolicy{}, err
	}
	limit, err := s.store.GetUserLimit(ctx, userID)
	if err != nil {
		return EffectivePolicy{}, err
	}

	policy := EffectivePolicy{
		RequestsEnabled: settings.RequestsEnabled,
		MaxRequests:     settings.GlobalMaxRequests,
		WindowDays:      settings.GlobalWindowDays,
		AutoApprove:     settings.GlobalAutoApprovalEnabled,
	}
	if policy.WindowDays <= 0 {
		policy.WindowDays = 7
	}
	if limit != nil {
		switch limit.LimitMode {
		case LimitModeBlocked:
			policy.Blocked = true
		case LimitModeUnlimited:
			policy.Unlimited = true
		case LimitModeCustom:
			if limit.MaxRequests != nil {
				policy.MaxRequests = *limit.MaxRequests
			}
			if limit.WindowDays != nil && *limit.WindowDays > 0 {
				policy.WindowDays = *limit.WindowDays
			}
		}
		switch limit.ApprovalMode {
		case ApprovalModeBlocked:
			policy.Blocked = true
		case ApprovalModeManual:
			policy.AutoApprove = false
		case ApprovalModeAuto:
			policy.AutoApprove = true
		}
	}

	policy.WindowStart = s.now().AddDate(0, 0, -policy.WindowDays)
	if !policy.Unlimited {
		used, err := s.store.CountUserRequestsSince(ctx, userID, policy.WindowStart)
		if err != nil {
			return EffectivePolicy{}, err
		}
		policy.Used = used
		policy.Remaining = policy.MaxRequests - used
		if policy.Remaining < 0 {
			policy.Remaining = 0
		}
	}
	return policy, nil
}

func (s *Service) enrichPage(ctx context.Context, viewer Viewer, raw *tmdb.MediaPage) (*MediaPage, error) {
	if raw == nil {
		return &MediaPage{Results: []MediaResult{}}, nil
	}
	policy, err := s.EffectivePolicy(ctx, viewer.UserID)
	if err != nil {
		return nil, err
	}

	idsByType := map[MediaType][]int{}
	for _, item := range raw.Results {
		mediaType, err := normalizeMediaType(MediaType(item.MediaType))
		if err != nil || item.ID <= 0 {
			continue
		}
		idsByType[mediaType] = append(idsByType[mediaType], item.ID)
	}

	available := map[MediaType]map[int]bool{}
	active := map[MediaType]map[int]*Request{}
	for mediaType, ids := range idsByType {
		presence, err := s.lookupAvailable(ctx, mediaType, ids)
		if err != nil {
			return nil, err
		}
		available[mediaType] = presence
		requests, err := s.store.ListActiveByTMDB(ctx, mediaType, ids)
		if err != nil {
			return nil, err
		}
		active[mediaType] = requests
	}

	out := &MediaPage{
		Page:         raw.Page,
		TotalPages:   raw.TotalPages,
		TotalResults: raw.TotalResults,
		Results:      make([]MediaResult, 0, len(raw.Results)),
	}
	for _, item := range raw.Results {
		mediaType, err := normalizeMediaType(MediaType(item.MediaType))
		if err != nil || item.ID <= 0 {
			continue
		}
		isAvailable := available[mediaType][item.ID]
		activeRequest := active[mediaType][item.ID]
		out.Results = append(out.Results, MediaResult{
			MediaType:    mediaType,
			TMDBID:       item.ID,
			Title:        item.Title,
			Year:         item.Year,
			Overview:     item.Overview,
			PosterPath:   item.PosterPath,
			BackdropPath: item.BackdropPath,
			ReleaseDate:  item.ReleaseDate,
			Popularity:   item.Popularity,
			VoteAverage:  item.VoteAverage,
			Availability: availabilityValue(isAvailable),
			Request:      requestStateFor(viewer, policy, isAvailable, activeRequest),
		})
	}
	return out, nil
}

func (s *Service) lookupPresence(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error) {
	if s.presence == nil {
		return map[int]PresenceMatch{}, nil
	}
	return s.presence.Lookup(ctx, mediaType, candidates)
}

func availabilityBoolMap(matches map[int]PresenceMatch) map[int]bool {
	out := map[int]bool{}
	for id, match := range matches {
		out[id] = match.Available
	}
	return out
}

func requestPresenceCandidate(req Request) PresenceCandidate {
	candidate := PresenceCandidate{
		TMDBID: req.TMDBID,
		IMDbID: strings.TrimSpace(req.IMDbID),
	}
	if req.TVDBID != nil && *req.TVDBID > 0 {
		tvdbID := *req.TVDBID
		candidate.TVDBID = &tvdbID
	}
	return candidate
}

func createPresenceCandidate(input CreateRequestInput) PresenceCandidate {
	candidate := PresenceCandidate{
		TMDBID: input.TMDBID,
		IMDbID: strings.TrimSpace(input.IMDbID),
	}
	if input.TVDBID != nil && *input.TVDBID > 0 {
		tvdbID := *input.TVDBID
		candidate.TVDBID = &tvdbID
	}
	return candidate
}

func (s *Service) hydratePresenceCandidate(ctx context.Context, mediaType MediaType, candidate PresenceCandidate) PresenceCandidate {
	if candidate.TMDBID <= 0 {
		return candidate
	}
	client, ok := s.tmdb.(TMDBExternalIDClient)
	if !ok {
		return candidate
	}
	externalIDs, err := client.GetExternalIDs(ctx, tmdbMediaType(mediaType), candidate.TMDBID)
	if err != nil || externalIDs == nil {
		return candidate
	}
	if candidate.IMDbID == "" {
		candidate.IMDbID = strings.TrimSpace(externalIDs.IMDbID)
	}
	if candidate.TVDBID == nil && externalIDs.TVDBID > 0 {
		tvdbID := externalIDs.TVDBID
		candidate.TVDBID = &tvdbID
	}
	return candidate
}

func (s *Service) hydratePresenceCandidates(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) []PresenceCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	if _, ok := s.tmdb.(TMDBExternalIDClient); !ok {
		return candidates
	}

	hydrated := append([]PresenceCandidate(nil), candidates...)
	if externalIDHydrationConcurrency <= 1 {
		for i := range hydrated {
			if ctx.Err() != nil {
				return hydrated
			}
			hydrated[i] = s.hydratePresenceCandidate(ctx, mediaType, hydrated[i])
		}
		return hydrated
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(externalIDHydrationConcurrency)
	for i := range hydrated {
		if groupCtx.Err() != nil {
			break
		}
		i := i
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			hydrated[i] = s.hydratePresenceCandidate(groupCtx, mediaType, hydrated[i])
			return nil
		})
	}
	_ = group.Wait()
	return hydrated
}

func tmdbMediaType(mediaType MediaType) string {
	if mediaType == MediaTypeSeries {
		return "tv"
	}
	return "movie"
}

func (s *Service) lookupAvailable(ctx context.Context, mediaType MediaType, ids []int) (map[int]bool, error) {
	if s.presence == nil {
		return map[int]bool{}, nil
	}
	candidates := make([]PresenceCandidate, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			candidates = append(candidates, PresenceCandidate{TMDBID: id})
		}
	}
	candidates = s.hydratePresenceCandidates(ctx, mediaType, candidates)
	matches, err := s.lookupPresence(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	return availabilityBoolMap(matches), nil
}

func (s *Service) enrichExternalIDs(ctx context.Context, input *CreateRequestInput) {
	if input == nil {
		return
	}
	client, ok := s.tmdb.(TMDBExternalIDClient)
	if !ok {
		return
	}
	externalIDs, err := client.GetExternalIDs(ctx, tmdbMediaType(input.MediaType), input.TMDBID)
	if err != nil || externalIDs == nil {
		return
	}
	if input.IMDbID == "" {
		input.IMDbID = strings.TrimSpace(externalIDs.IMDbID)
	}
	if input.TVDBID == nil && externalIDs.TVDBID > 0 {
		tvdbID := externalIDs.TVDBID
		input.TVDBID = &tvdbID
	}
}

func (s *Service) detectRequestAnime(ctx context.Context, mediaType MediaType, tmdbID int) bool {
	detail, err := s.tmdb.GetMediaDetail(ctx, tmdbMediaType(mediaType), tmdbID)
	if err != nil || detail == nil {
		return false
	}
	return detectAnime(detail.KeywordIDs)
}

func (s *Service) integrationConfigured(ctx context.Context, mediaType MediaType) (bool, error) {
	instances, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return false, err
	}
	kind := integrationKindForMediaType(mediaType)
	for _, in := range instances {
		if in.Kind == kind && in.Enabled && (in.IsDefault || in.IsDefault4K) && integrationIsConfigured(in) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) submitApprovedRequest(ctx context.Context, req Request, actor Viewer) (*Request, error) {
	if req.Outcome != OutcomeActive || req.Status != StatusApproved {
		return &req, nil
	}
	instances, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	ceiling := s.requesterCeiling(ctx, req.RequestedByUserID, req.RequestedByProfileID)
	planned := routeTargets(req, ceiling, settings, instances)
	if len(planned) == 0 {
		return s.markSubmissionFailed(ctx, req.ID, actor,
			fmt.Errorf("no %s instance configured for the requested quality",
				integrationKindForMediaType(req.MediaType)))
	}

	// Reconcile can re-run submit while the request is still 'approved'; skip
	// qualities that already have a live target and replace failed ones so the
	// UNIQUE(request_id, quality) constraint is never violated.
	existing, err := s.store.ListTargets(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return s.submitPlannedTargets(ctx, req, planned, existing, actor)
}

// submitPlannedTarget creates a target row, submits it to the adapter, and
// records the result (queued or failed) via UpdateTargetStatus (which recomputes
// the request aggregate). Returns the latest request snapshot.
func (s *Service) submitPlannedTarget(ctx context.Context, req Request, pt plannedTarget, actor Viewer) (*Request, error) {
	resolved := resolveInstance(pt)
	apiKey, err := s.resolveAPIKey(ctx, resolved)
	if err != nil || apiKey == "" {
		msg := "missing api key"
		if err != nil {
			msg = err.Error()
		}
		return s.createFailedTarget(ctx, req, pt, resolved, msg, actor)
	}
	resolved.APIKeyRef = apiKey

	target, err := s.store.CreateTarget(ctx, Target{
		RequestID: req.ID, IntegrationID: resolved.ID, IntegrationKind: resolved.Kind,
		Quality: pt.Quality, IsAnime: pt.IsAnime, Status: StatusQueued,
	})
	if err != nil {
		return nil, err
	}

	result, serr := s.submitTarget(ctx, req, resolved)
	if serr != nil {
		return s.store.UpdateTargetStatus(ctx, target.ID, StatusFailed, "", "", serr.Error(), actor)
	}
	return s.store.UpdateTargetStatus(ctx, target.ID, StatusQueued,
		result.ExternalID, result.ExternalStatus, "", actor)
}

func (s *Service) createFailedTarget(ctx context.Context, req Request, pt plannedTarget, resolved Integration, msg string, actor Viewer) (*Request, error) {
	target, err := s.store.CreateTarget(ctx, Target{
		RequestID: req.ID, IntegrationID: resolved.ID, IntegrationKind: resolved.Kind,
		Quality: pt.Quality, IsAnime: pt.IsAnime, Status: StatusFailed, LastError: msg,
	})
	if err != nil {
		return nil, err
	}
	return s.store.UpdateTargetStatus(ctx, target.ID, StatusFailed, "", "", msg, actor)
}

// submitTarget calls the correct adapter with a per-target Request copy. The
// adapters read root folder/quality/tags (and Sonarr series_type) from the
// resolved Integration.
func (s *Service) submitTarget(ctx context.Context, req Request, resolved Integration) (FulfillmentResult, error) {
	switch req.MediaType {
	case MediaTypeMovie:
		if s.movieAdapter == nil {
			return FulfillmentResult{}, fmt.Errorf("no movie adapter configured")
		}
		return s.movieAdapter.SubmitMovie(ctx, req, resolved)
	case MediaTypeSeries:
		if s.seriesAdapter == nil {
			return FulfillmentResult{}, fmt.Errorf("no series adapter configured")
		}
		return s.seriesAdapter.SubmitSeries(ctx, req, resolved)
	default:
		return FulfillmentResult{}, fmt.Errorf("unsupported media type %q", req.MediaType)
	}
}

func (s *Service) markSubmissionFailed(ctx context.Context, requestID string, actor Viewer, submitErr error) (*Request, error) {
	failed, err := s.store.SetOutcome(ctx, requestID, OutcomeFailed, actor, submitErr.Error())
	if err != nil {
		return nil, fmt.Errorf("submit request failed: %w; mark failed: %v", submitErr, err)
	}
	return failed, nil
}

type reconcileChange string

const (
	reconcileUnchanged   reconcileChange = "unchanged"
	reconcileSkipped     reconcileChange = "skipped"
	reconcileSubmitted   reconcileChange = "submitted"
	reconcileDownloading reconcileChange = "downloading"
	reconcileCompleted   reconcileChange = "completed"
	reconcileFailed      reconcileChange = "failed"
)

func (s *Service) reconcileRequest(ctx context.Context, req Request) (reconcileChange, error) {
	completed, err := s.requestAvailable(ctx, req)
	if err != nil {
		return reconcileUnchanged, err
	}
	if completed {
		// The presence check is quality-agnostic (TMDB id only), so it must not
		// force-complete a request whose targets are still in flight — that would
		// orphan in-progress downloads. Only take the shortcut for legacy/no-live
		// -target requests; otherwise let per-target reconcile + aggregate drive
		// completion.
		hasLiveTargets, err := s.hasLiveTargets(ctx, req.ID)
		if err != nil {
			return reconcileUnchanged, err
		}
		if !hasLiveTargets {
			if req.Status == StatusCompleted {
				return reconcileUnchanged, nil
			}
			if _, err := s.store.SetStatus(ctx, req.ID, StatusCompleted, Viewer{}); err != nil {
				return reconcileUnchanged, err
			}
			return reconcileCompleted, nil
		}
	}

	if req.Status == StatusApproved {
		updated, err := s.submitApprovedRequest(ctx, req, Viewer{})
		if err != nil {
			return reconcileUnchanged, err
		}
		switch {
		case updated.Outcome == OutcomeFailed:
			return reconcileFailed, nil
		case updated.Status == StatusQueued:
			return reconcileSubmitted, nil
		default:
			return reconcileSkipped, nil
		}
	}

	targets, err := s.store.ListTargets(ctx, req.ID)
	if err != nil {
		return reconcileUnchanged, err
	}
	instances, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return reconcileUnchanged, err
	}
	byID := make(map[string]Integration, len(instances))
	for _, in := range instances {
		byID[in.ID] = in
	}
	change := reconcileUnchanged
	for _, t := range targets {
		if t.Status == StatusCompleted || t.Status == StatusFailed {
			continue
		}
		in, ok := byID[t.IntegrationID]
		if !ok {
			continue
		}
		apiKey, err := s.resolveAPIKey(ctx, in)
		if err != nil || apiKey == "" {
			continue
		}
		in.APIKeyRef = apiKey
		probe := req
		probe.ExternalID = t.ExternalID
		st, err := s.checkFulfillmentStatus(ctx, probe, in)
		if err != nil {
			continue
		}
		if st.Status == "" && st.Outcome == "" {
			continue
		}
		newStatus := targetStatusFromFulfillment(st)
		if newStatus == t.Status {
			continue
		}
		if _, err := s.store.UpdateTargetStatus(ctx, t.ID, newStatus, st.ExternalID, st.ExternalStatus, "", Viewer{}); err != nil {
			return reconcileUnchanged, err
		}
		switch newStatus {
		case StatusCompleted:
			change = reconcileCompleted
		case StatusDownloading:
			if change == reconcileUnchanged {
				change = reconcileDownloading
			}
		case StatusFailed:
			if change == reconcileUnchanged {
				change = reconcileFailed
			}
		}
	}
	return change, nil
}

func targetStatusFromFulfillment(st FulfillmentStatus) Status {
	switch st.Status {
	case StatusCompleted:
		return StatusCompleted
	case StatusDownloading:
		return StatusDownloading
	default:
		if st.Outcome == OutcomeFailed {
			return StatusFailed
		}
		return StatusQueued
	}
}

// hasLiveTargets reports whether the request has any non-terminal (queued or
// downloading) fulfillment target.
func (s *Service) hasLiveTargets(ctx context.Context, requestID string) (bool, error) {
	targets, err := s.store.ListTargets(ctx, requestID)
	if err != nil {
		return false, err
	}
	for _, t := range targets {
		if t.Status == StatusQueued || t.Status == StatusDownloading {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) requestAvailable(ctx context.Context, req Request) (bool, error) {
	matches, err := s.lookupPresence(ctx, req.MediaType, []PresenceCandidate{requestPresenceCandidate(req)})
	if err != nil {
		return false, err
	}
	return matches[req.TMDBID].Available, nil
}

func (s *Service) checkFulfillmentStatus(ctx context.Context, req Request, resolved Integration) (FulfillmentStatus, error) {
	switch req.MediaType {
	case MediaTypeMovie:
		checker, ok := s.movieAdapter.(MovieStatusAdapter)
		if !ok {
			return FulfillmentStatus{}, nil
		}
		return checker.CheckMovieStatus(ctx, req, resolved)
	case MediaTypeSeries:
		checker, ok := s.seriesAdapter.(SeriesStatusAdapter)
		if !ok {
			return FulfillmentStatus{}, nil
		}
		return checker.CheckSeriesStatus(ctx, req, resolved)
	default:
		return FulfillmentStatus{}, nil
	}
}

func (s *Service) resolveAPIKey(ctx context.Context, integration Integration) (string, error) {
	value := strings.TrimSpace(integration.APIKeyRef)
	if value == "" || s.secrets == nil {
		return value, nil
	}
	resolved, err := s.secrets.Get(ctx, value)
	if err != nil {
		return "", err
	}
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return value, nil
	}
	return resolved, nil
}

func integrationIsConfigured(integration Integration) bool {
	return integration.Enabled &&
		strings.TrimSpace(integration.BaseURL) != "" &&
		strings.TrimSpace(integration.APIKeyRef) != "" &&
		strings.TrimSpace(integration.RootFolder) != "" &&
		integration.QualityProfileID != nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func requestStateFor(viewer Viewer, policy EffectivePolicy, available bool, req *Request) RequestState {
	if req != nil {
		state := RequestState{
			Status:      req.Status,
			Requestable: false,
			Reason:      "already_requested",
		}
		if viewer.IsAdmin || req.RequestedByUserID == viewer.UserID {
			state.RequestID = req.ID
		}
		return state
	}
	switch {
	case available:
		return RequestState{Requestable: false, Reason: "already_available"}
	case !policy.RequestsEnabled:
		return RequestState{Requestable: false, Reason: "requests_disabled"}
	case policy.Blocked:
		return RequestState{Requestable: false, Reason: "blocked"}
	case !policy.Unlimited && policy.Used >= policy.MaxRequests:
		return RequestState{Requestable: false, Reason: "quota_exceeded"}
	default:
		return RequestState{Requestable: true}
	}
}

func validateCreatePolicy(policy EffectivePolicy) error {
	switch {
	case !policy.RequestsEnabled:
		return ErrRequestsDisabled
	case policy.Blocked:
		return ErrUserBlocked
	case !policy.Unlimited && policy.Used >= policy.MaxRequests:
		return QuotaError{Used: policy.Used, Limit: policy.MaxRequests, WindowDays: policy.WindowDays}
	default:
		return nil
	}
}

func validateViewer(viewer Viewer) error {
	if viewer.UserID == 0 {
		return ErrForbidden
	}
	if strings.TrimSpace(viewer.ProfileID) == "" {
		return fmt.Errorf("%w: profile is required", ErrInvalidInput)
	}
	return nil
}

func normalizeCreateInput(input CreateRequestInput) (CreateRequestInput, error) {
	mediaType, err := normalizeMediaType(input.MediaType)
	if err != nil {
		return CreateRequestInput{}, err
	}
	input.MediaType = mediaType
	input.Title = strings.TrimSpace(input.Title)
	input.IMDbID = strings.TrimSpace(input.IMDbID)
	input.Overview = strings.TrimSpace(input.Overview)
	input.PosterPath = strings.TrimSpace(input.PosterPath)
	input.BackdropPath = strings.TrimSpace(input.BackdropPath)
	if input.TMDBID <= 0 {
		return CreateRequestInput{}, fmt.Errorf("%w: tmdb_id is required", ErrInvalidInput)
	}
	if input.Title == "" {
		return CreateRequestInput{}, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	return input, nil
}

func normalizeUserLimit(limit UserLimit) (UserLimit, error) {
	if limit.UserID <= 0 {
		return UserLimit{}, fmt.Errorf("%w: invalid user id", ErrInvalidInput)
	}
	switch limit.LimitMode {
	case "", LimitModeInherit:
		limit.LimitMode = LimitModeInherit
		limit.MaxRequests = nil
		limit.WindowDays = nil
	case LimitModeCustom:
		if limit.MaxRequests == nil || limit.WindowDays == nil || *limit.MaxRequests < 0 || *limit.WindowDays <= 0 {
			return UserLimit{}, fmt.Errorf("%w: custom limits require max_requests >= 0 and window_days > 0", ErrInvalidInput)
		}
	case LimitModeUnlimited:
		limit.MaxRequests = nil
		limit.WindowDays = nil
	case LimitModeBlocked:
		limit.MaxRequests = nil
		limit.WindowDays = nil
	default:
		return UserLimit{}, fmt.Errorf("%w: invalid limit mode", ErrInvalidInput)
	}
	switch limit.ApprovalMode {
	case "", ApprovalModeInherit:
		limit.ApprovalMode = ApprovalModeInherit
	case ApprovalModeManual, ApprovalModeAuto, ApprovalModeBlocked:
	default:
		return UserLimit{}, fmt.Errorf("%w: invalid approval mode", ErrInvalidInput)
	}
	return limit, nil
}

func normalizeIntegrationConnection(integration Integration) (Integration, error) {
	integration.Kind = strings.ToLower(strings.TrimSpace(integration.Kind))
	switch integration.Kind {
	case "radarr", "sonarr":
	default:
		return Integration{}, fmt.Errorf("%w: invalid integration kind", ErrInvalidInput)
	}
	integration.BaseURL = strings.TrimRight(strings.TrimSpace(integration.BaseURL), "/")
	integration.APIKeyRef = strings.TrimSpace(integration.APIKeyRef)
	if integration.Options == nil {
		integration.Options = map[string]any{}
	}
	return integration, nil
}

func normalizeMediaType(mediaType MediaType) (MediaType, error) {
	switch MediaType(strings.ToLower(strings.TrimSpace(string(mediaType)))) {
	case MediaTypeMovie:
		return MediaTypeMovie, nil
	case MediaTypeSeries, "tv":
		return MediaTypeSeries, nil
	default:
		return "", ErrInvalidMediaType
	}
}

func normalizeSearchMediaType(mediaType MediaType) (MediaType, error) {
	switch MediaType(strings.ToLower(strings.TrimSpace(string(mediaType)))) {
	case "", MediaTypeAll:
		return MediaTypeAll, nil
	case MediaTypeMovie:
		return MediaTypeMovie, nil
	case MediaTypeSeries, "tv":
		return MediaTypeSeries, nil
	default:
		return "", ErrInvalidMediaType
	}
}

const (
	defaultRequestListLimit = 50
	maxRequestListLimit     = 100
)

func normalizeListFilter(filter ListFilter) ListFilter {
	if filter.Limit <= 0 {
		filter.Limit = defaultRequestListLimit
	}
	if filter.Limit > maxRequestListLimit {
		filter.Limit = maxRequestListLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func availabilityValue(available bool) Availability {
	if available {
		return AvailabilityAvailable
	}
	return AvailabilityMissing
}

var discoverySectionOrder = []string{
	"trending_movies",
	"trending_series",
	"popular_movies",
	"popular_series",
	"upcoming_movies",
	"on_air_series",
}

var discoverySectionTitles = map[string]string{
	"trending_movies": "Trending Movies",
	"trending_series": "Trending Series",
	"popular_movies":  "Popular Movies",
	"popular_series":  "Popular Series",
	"upcoming_movies": "Upcoming Movies",
	"on_air_series":   "On Air Series",
}

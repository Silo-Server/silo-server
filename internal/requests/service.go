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

type EntitlementResolver interface {
	// MaxPlaybackQuality returns the requester's effective playback-quality
	// ceiling (already combining account- and profile-level caps). Empty string
	// means "no cap".
	MaxPlaybackQuality(ctx context.Context, userID int, profileID string) (string, error)
}

// RequesterIdentityResolver resolves a requesting user id into the identity a
// per-user request_router plugin needs (e.g. Seerr attribution by email).
type RequesterIdentityResolver interface {
	ResolveRequester(ctx context.Context, userID int) (email, username string, err error)
}

type Service struct {
	store             Store
	tmdb              TMDBClient
	presence          PresenceResolver
	router            RequestRouterProvider
	entitlements      EntitlementResolver
	groupProvider     access.GroupPolicyProvider
	requesterIdentity RequesterIdentityResolver
	notifier          FulfillmentNotifier
	lifecycle         LifecycleNotifier
	Now               func() time.Time
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

func (s *Service) SetRouterProvider(p RequestRouterProvider) { s.router = p }

func (s *Service) SetEntitlementResolver(r EntitlementResolver) { s.entitlements = r }

func (s *Service) SetGroupPolicyProvider(p access.GroupPolicyProvider) { s.groupProvider = p }

func (s *Service) SetRequesterIdentityResolver(r RequesterIdentityResolver) {
	s.requesterIdentity = r
}

// populateRequesterIdentity fills req.RequesterEmail/Username from the resolver.
// Nil resolver or any error leaves them empty (the plugin then behaves as admin).
func (s *Service) populateRequesterIdentity(ctx context.Context, req *Request) {
	if s.requesterIdentity == nil || req.RequestedByUserID <= 0 {
		return
	}
	email, username, err := s.requesterIdentity.ResolveRequester(ctx, req.RequestedByUserID)
	if err != nil {
		slog.WarnContext(ctx, "requests: requester identity resolve failed; attributing to admin", "user_id", req.RequestedByUserID, "error", err)
		return
	}
	req.RequesterEmail, req.RequesterUsername = email, username
}

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

// allowedQualities returns the qualities a request may receive: 1080p always,
// plus 2160p when force-dual is on or the requester's entitlement ceiling allows 4K.
func (s *Service) allowedQualities(ctx context.Context, req Request, settings Settings) []Quality {
	out := []Quality{Quality1080p}
	ceiling := s.requesterCeiling(ctx, req.RequestedByUserID, req.RequestedByProfileID)
	// QualityAllowed treats an empty ceiling as "no cap" (the "Any" preset), so a
	// requester with unlimited playback quality correctly gets 4K. A raw
	// CompareQuality would rank "" as the LOWEST quality and wrongly drop 4K.
	if settings.ForceDualQuality || access.QualityAllowed(access.PlaybackQuality4K, ceiling) {
		out = append(out, Quality2160p)
	}
	return out
}

// fulfillContext caches the global fulfillment inputs for one reconcile cycle
// (or a single Approve/Retry) so integrations and settings are fetched once
// instead of per request. API keys need no cache here: the repository decrypts
// api_key_ref on read, so Integration.APIKeyRef already holds the literal key.
type fulfillContext struct {
	integrations []Integration
	settings     Settings
}

func (s *Service) newFulfillContext(ctx context.Context) (*fulfillContext, error) {
	integrations, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &fulfillContext{integrations: integrations, settings: settings}, nil
}

// resolveRouterConnections turns enabled request_router integrations that serve
// the given media type into ResolvedRouterConnections (api key resolved to
// plaintext, plugin_config attached), and returns the installation+capability to
// dispatch to.
//
// It filters by media type to match the integrationConfigured auto-approve gate
// (so a series-only connection is never used for a movie request). Multi-
// installation routing isn't supported yet: it picks the first eligible
// connection's installation and includes ONLY connections belonging to it, so a
// second installation's resolved plaintext credentials are never handed to the
// first plugin. A connection whose api key cannot be resolved (or resolves empty)
// is skipped rather than aborting the whole request — a sibling healthy
// connection can still fulfill it, and an unauthenticated request is never sent.
func (s *Service) resolveRouterConnections(ctx context.Context, fc *fulfillContext, mediaType MediaType) ([]ResolvedRouterConnection, int, string, error) {
	var conns []ResolvedRouterConnection
	installationID, capabilityID := 0, ""
	chosen := false
	for _, in := range fc.integrations {
		if !eligibleRouterConnection(in, mediaType) {
			continue
		}
		// Contain to the first chosen (installation, capability): a plugin may
		// expose more than one request_router capability, and a connection of a
		// different capability must never be handed to the chosen one.
		if chosen && (*in.InstallationID != installationID || in.CapabilityID != capabilityID) {
			continue
		}
		// in.APIKeyRef was decrypted by the repo on read; empty means unconfigured.
		apiKey := strings.TrimSpace(in.APIKeyRef)
		if apiKey == "" {
			slog.WarnContext(ctx, "requests: skipping router connection with no api key", "connection_id", in.ID)
			continue
		}
		// Lock on the first SUCCESSFULLY resolved connection so a skipped
		// bad-key connection never pins the installation/capability.
		if !chosen {
			installationID, capabilityID, chosen = *in.InstallationID, in.CapabilityID, true
		}
		conns = append(conns, ResolvedRouterConnection{ID: in.ID, BaseURL: in.BaseURL, APIKey: apiKey, Config: in.PluginConfig})
	}
	return conns, installationID, capabilityID, nil
}

// eligibleRouterConnection reports whether a connection is a candidate fulfillment
// backend for the media type: enabled, bound to an installation, and naming a
// capability sub-id that serves the media type. resolveRouterConnections (which
// then resolves credentials) and integrationConfigured (the auto-approval gate)
// share this predicate so the two cannot drift.
func eligibleRouterConnection(in Integration, mediaType MediaType) bool {
	return in.Enabled && in.CapabilityID != "" && in.InstallationID != nil &&
		integrationSupportsMediaType(in, mediaType)
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

	primaryPresence, err := s.lookupAvailable(ctx, mediaType, []int{raw.ID})
	if err != nil {
		return nil, err
	}
	primaryMatch := primaryPresence[raw.ID]
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
		Availability:        availabilityValue(primaryMatch.Available),
		LibraryContentID:    primaryMatch.ContentID,
		Request:             requestStateFor(viewer, policy, primaryMatch.Available, primaryRequests[raw.ID]),
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
	if err := s.ensureViewerRequestsAllowed(ctx, viewer.UserID); err != nil {
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
	s.notifyLifecycle(ctx, *req, LifecycleNotifier.RequestSubmitted)
	if req.Status == StatusApproved {
		// Auto-approval is a real approval transition; channels subscribed to
		// approvals see it alongside the submission.
		s.notifyLifecycle(ctx, *req, LifecycleNotifier.RequestApproved)
		return s.submitApprovedRequest(ctx, *req, viewer, nil)
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
	if err := s.attachLibraryContent(ctx, reqs...); err != nil {
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
	if err := s.attachLibraryContent(ctx, reqs...); err != nil {
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

func (s *Service) attachLibraryContent(ctx context.Context, reqs ...*Request) error {
	if s == nil || s.presence == nil || len(reqs) == 0 {
		return nil
	}

	type requestKey struct {
		mediaType MediaType
		tmdbID    int
	}

	candidatesByType := map[MediaType][]PresenceCandidate{}
	requestsByKey := map[requestKey][]*Request{}
	seen := map[requestKey]bool{}
	for _, req := range reqs {
		if req == nil || req.TMDBID <= 0 {
			continue
		}
		key := requestKey{mediaType: req.MediaType, tmdbID: req.TMDBID}
		requestsByKey[key] = append(requestsByKey[key], req)
		if seen[key] {
			continue
		}
		seen[key] = true
		candidatesByType[req.MediaType] = append(candidatesByType[req.MediaType], requestPresenceCandidate(*req))
	}

	for mediaType, candidates := range candidatesByType {
		matches, err := s.lookupPresence(ctx, mediaType, candidates)
		if err != nil {
			return err
		}
		for tmdbID, match := range matches {
			if !match.Available || strings.TrimSpace(match.ContentID) == "" {
				continue
			}
			for _, req := range requestsByKey[requestKey{mediaType: mediaType, tmdbID: tmdbID}] {
				req.LibraryContentID = match.ContentID
			}
		}
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
	if err := s.attachLibraryContent(ctx, req); err != nil {
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
	s.notifyLifecycle(ctx, *approved, LifecycleNotifier.RequestApproved)
	return s.submitApprovedRequest(ctx, *approved, viewer, nil)
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
	declined, err := s.store.SetOutcome(ctx, req.ID, OutcomeDeclined, viewer, reason)
	if err != nil {
		return nil, err
	}
	declined.DeclineReason = strings.TrimSpace(reason)
	s.notifyLifecycle(ctx, *declined, LifecycleNotifier.RequestDeclined)
	return declined, nil
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
	if _, err := s.store.SetOutcome(ctx, req.ID, OutcomeActive, viewer, "retry requested"); err != nil {
		return nil, err
	}
	// submitApprovedRequest only re-submits qualities lacking a healthy target, so
	// it is idempotent; gate it on the approved status it expects.
	active, err := s.store.SetStatus(ctx, req.ID, StatusApproved, viewer)
	if err != nil {
		return nil, err
	}
	return s.submitApprovedRequest(ctx, *active, viewer, nil)
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
	fc, err := s.newFulfillContext(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Checked: len(candidates)}
	for _, req := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		change, err := s.reconcileRequest(ctx, *req, fc)
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
	// Presence-gated fulfillment notifications: completion above (and via the
	// per-target aggregate path) only marks status; the notification fires
	// once the media is confirmed present in the catalog.
	if s.notifier != nil {
		s.notifyFulfilledPending(ctx)
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

func (s *Service) ensureViewerRequestsAllowed(ctx context.Context, userID int) error {
	if s.groupProvider == nil {
		return nil
	}
	group, err := s.groupProvider.GetPolicyForUser(ctx, userID)
	if err != nil {
		return ErrForbidden
	}
	if group != nil && !group.RequestsAllowed {
		return ErrForbidden
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
	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	in.ID = id
	if err := validateInstance(&in); err != nil {
		return nil, err
	}
	if err := s.validateViaPlugin(ctx, in); err != nil {
		return nil, err
	}
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
	if err := s.validateViaPlugin(ctx, in); err != nil {
		return nil, err
	}
	return s.store.SaveIntegrationWithDefaults(ctx, in, false)
}

// validateViaPlugin asks the bound request_router plugin to validate the
// connection config on save. Field/form errors are surfaced as *ValidationError
// so the API layer can render them inline.
func (s *Service) validateViaPlugin(ctx context.Context, in Integration) error {
	if s.router == nil || in.InstallationID == nil {
		return nil
	}
	// On UPDATE the client omits api_key_ref ("leave blank to keep saved key"),
	// so we would otherwise validate against an empty credential. Mirror
	// LoadIntegrationOptions's backfill: load the stored row by id and reuse the
	// saved (already-decrypted) api key (and BaseURL/PluginConfig if also blank).
	// Nil-safe — a brand-new id has no stored row, so just proceed with what the
	// body carries.
	if strings.TrimSpace(in.APIKeyRef) == "" && strings.TrimSpace(in.ID) != "" {
		stored, err := s.store.GetIntegration(ctx, in.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if stored != nil {
			// Don't pair a stored API key with a caller-changed base URL: require the
			// key to be re-entered when the server URL changes (defense against
			// exfiltrating a stored, API-unreadable key to an attacker-supplied URL).
			if strings.TrimSpace(in.BaseURL) != "" && strings.TrimSpace(in.BaseURL) != strings.TrimSpace(stored.BaseURL) {
				return &ValidationError{FieldErrors: map[string]string{"api_key_ref": "re-enter the API key when changing the base URL"}}
			}
			in.APIKeyRef = stored.APIKeyRef
			if strings.TrimSpace(in.BaseURL) == "" {
				in.BaseURL = stored.BaseURL
			}
			if in.PluginConfig == nil {
				in.PluginConfig = stored.PluginConfig
			}
		}
	}
	// in.APIKeyRef is the decrypted literal (from the body, or backfilled from the
	// stored row above).
	apiKey := strings.TrimSpace(in.APIKeyRef)
	conn := ResolvedRouterConnection{ID: in.ID, BaseURL: in.BaseURL, APIKey: apiKey, Config: in.PluginConfig}
	siblings, err := s.siblingConnections(ctx, in)
	if err != nil {
		return err
	}
	fe, form, err := s.router.Validate(ctx, *in.InstallationID, in.CapabilityID, conn, siblings)
	if err != nil {
		return err
	}
	if len(fe) > 0 || form != "" {
		return &ValidationError{FieldErrors: fe, FormError: form}
	}
	return nil
}

// siblingConnections returns the other connections bound to the same plugin
// installation as `in` (self excluded), carrying only id + config so a plugin
// can enforce cross-connection rules without the host resolving sibling
// credentials.
func (s *Service) siblingConnections(ctx context.Context, in Integration) ([]ResolvedRouterConnection, error) {
	if in.InstallationID == nil {
		return nil, nil
	}
	all, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	var out []ResolvedRouterConnection
	for _, other := range all {
		if other.ID == in.ID || other.InstallationID == nil || *other.InstallationID != *in.InstallationID {
			continue
		}
		out = append(out, ResolvedRouterConnection{ID: other.ID, Config: other.PluginConfig})
	}
	return out, nil
}

func (s *Service) DeleteIntegration(ctx context.Context, viewer Viewer, id string) error {
	if !viewer.IsAdmin {
		return ErrForbidden
	}
	return s.store.DeleteIntegration(ctx, strings.TrimSpace(id))
}

func validateInstance(in *Integration) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	// capability_id carries the capability SUB-ID ("arr"/"seerr"), not the type:
	// the host resolves the plugin via requireCapability("request_router.v1", id),
	// which keys on (type, id), so storing the type "request_router.v1" here
	// resolves nothing. Matches the scan_source/metadata convention
	// (autoscan_sources.capability_id = "arr"). The bound plugin's Validate RPC is
	// the authority on whether the sub-id names a real capability.
	in.CapabilityID = strings.TrimSpace(in.CapabilityID)
	if in.CapabilityID == "" {
		return fmt.Errorf("%w: capability_id is required", ErrInvalidInput)
	}
	if in.InstallationID == nil {
		return fmt.Errorf("%w: installation_id is required", ErrInvalidInput)
	}
	// The is_default/is_4k/is_default_4k cross-field consistency check is owned by
	// the request_router plugin's Validate RPC, which surfaces it as an inline
	// field error (better UX than a generic host 400). See validateViaPlugin.
	return nil
}

func (s *Service) LoadIntegrationOptions(ctx context.Context, viewer Viewer, integration Integration) (map[string][]RouterOption, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	// For a saved instance the request body carries only the path id (no creds and
	// often no plugin wiring), so resolve the saved row by id and backfill what the
	// body omitted. This makes "Test connection" reuse the correct per-instance key
	// (each plugin can have multiple connections) instead of borrowing a sibling's.
	if id := strings.TrimSpace(integration.ID); id != "" && id != "new" {
		stored, err := s.store.GetIntegration(ctx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if stored != nil {
			submittedBaseURL := strings.TrimSpace(integration.BaseURL)
			storedBaseURL := strings.TrimSpace(stored.BaseURL)
			if strings.TrimSpace(integration.BaseURL) == "" {
				integration.BaseURL = stored.BaseURL
			}
			if strings.TrimSpace(integration.APIKeyRef) == "" && (submittedBaseURL == "" || submittedBaseURL == storedBaseURL) {
				integration.APIKeyRef = stored.APIKeyRef
			}
			if strings.TrimSpace(integration.CapabilityID) == "" {
				integration.CapabilityID = stored.CapabilityID
			}
			if integration.InstallationID == nil {
				integration.InstallationID = stored.InstallationID
			}
			if integration.PluginConfig == nil {
				integration.PluginConfig = stored.PluginConfig
			}
		}
	}

	apiKey := strings.TrimSpace(integration.APIKeyRef)
	if s.router == nil || integration.InstallationID == nil {
		return nil, fmt.Errorf("no fulfillment backend configured")
	}
	conn := ResolvedRouterConnection{ID: integration.ID, BaseURL: integration.BaseURL, APIKey: apiKey, Config: integration.PluginConfig}
	return s.router.ListConfigOptions(ctx, *integration.InstallationID, integration.CapabilityID, conn)
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

	available := map[MediaType]map[int]PresenceMatch{}
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
		match := available[mediaType][item.ID]
		activeRequest := active[mediaType][item.ID]
		out.Results = append(out.Results, MediaResult{
			MediaType:        mediaType,
			TMDBID:           item.ID,
			Title:            item.Title,
			Year:             item.Year,
			Overview:         item.Overview,
			PosterPath:       item.PosterPath,
			BackdropPath:     item.BackdropPath,
			ReleaseDate:      item.ReleaseDate,
			Popularity:       item.Popularity,
			VoteAverage:      item.VoteAverage,
			Availability:     availabilityValue(match.Available),
			LibraryContentID: match.ContentID,
			Request:          requestStateFor(viewer, policy, match.Available, activeRequest),
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

func (s *Service) lookupAvailable(ctx context.Context, mediaType MediaType, ids []int) (map[int]PresenceMatch, error) {
	if s.presence == nil {
		return map[int]PresenceMatch{}, nil
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
	return matches, nil
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

// integrationConfigured reports whether a fulfillment backend exists for the
// media type, gating auto-approval (pending vs approved). It uses the same
// router-connection selection as resolveRouterConnections — an enabled
// request_router.v1 connection with an installation — and additionally honors a
// connection's declared media-type support so a movie request only auto-approves
// when a router connection supporting "movie" exists.
func (s *Service) integrationConfigured(ctx context.Context, mediaType MediaType) (bool, error) {
	instances, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return false, err
	}
	for _, in := range instances {
		if eligibleRouterConnection(in, mediaType) &&
			strings.TrimSpace(in.BaseURL) != "" && strings.TrimSpace(in.APIKeyRef) != "" {
			return true, nil
		}
	}
	return false, nil
}

// integrationSupportsMediaType reports whether a router connection serves the
// given media type. An empty SupportedMediaTypes is treated as "supports all".
func integrationSupportsMediaType(in Integration, mediaType MediaType) bool {
	if len(in.SupportedMediaTypes) == 0 {
		return true
	}
	for _, mt := range in.SupportedMediaTypes {
		if mt == string(mediaType) {
			return true
		}
	}
	return false
}

func (s *Service) submitApprovedRequest(ctx context.Context, req Request, actor Viewer, fc *fulfillContext) (*Request, error) {
	if req.Outcome != OutcomeActive || req.Status != StatusApproved {
		return &req, nil
	}
	if s.router == nil {
		return s.markSubmissionFailed(ctx, req.ID, actor, fmt.Errorf("no fulfillment backend configured"))
	}
	if fc == nil {
		built, err := s.newFulfillContext(ctx)
		if err != nil {
			return nil, err
		}
		fc = built
	}
	conns, installationID, capabilityID, err := s.resolveRouterConnections(ctx, fc, req.MediaType)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		// Distinguish "no backend at all" from the migration breakage where an
		// existing connection row exists but its installation_id is NULL (the row
		// predates the plugin install and was never re-bound).
		msg := "no fulfillment backend configured"
		for _, in := range fc.integrations {
			if in.Enabled && in.CapabilityID != "" && in.InstallationID == nil {
				msg = "request backend connection is not bound to a plugin installation; re-save it in admin"
				break
			}
		}
		return s.markSubmissionFailed(ctx, req.ID, actor, errors.New(msg))
	}
	existing, err := s.store.ListTargets(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	healthy := map[Quality]bool{}
	for _, t := range existing {
		if t.Status != StatusFailed {
			healthy[t.Quality] = true
		}
	}
	allowed := s.allowedQualities(ctx, req, fc.settings)
	if !fc.settings.ForceDualQuality {
		allowed = filterUnconfiguredOptionalQualities(allowed, conns)
	}
	var want []Quality
	for _, q := range allowed {
		if !healthy[q] {
			want = append(want, q)
		}
	}
	if len(want) == 0 {
		return &req, nil
	}
	for _, t := range existing { // drop stale failed targets for the qualities we re-submit
		if t.Status == StatusFailed {
			for _, q := range want {
				if t.Quality == q {
					if err := s.store.DeleteTarget(ctx, t.ID); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	s.populateRequesterIdentity(ctx, &req)
	targets, msg, err := s.router.Fulfill(ctx, installationID, capabilityID, req, want, conns)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		if msg == "" {
			msg = "fulfillment backend created no targets"
		}
		return s.markSubmissionFailed(ctx, req.ID, actor, errors.New(msg))
	}
	connKind := connectionKindByID(conns)
	latest := &req
	// The plugin is an out-of-process trust boundary: validate every returned
	// target against the DB CHECK constraints (quality, status) and skip any
	// quality that is duplicated in the batch or already has a healthy target, so
	// a misbehaving plugin can't violate UNIQUE(request_id, quality) and wedge the
	// request.
	validQuality := map[Quality]bool{Quality1080p: true, Quality2160p: true}
	validStatus := map[Status]bool{StatusQueued: true, StatusDownloading: true, StatusCompleted: true, StatusFailed: true}
	returned := map[Quality]bool{}
	for _, rt := range targets {
		if !validQuality[rt.Quality] {
			slog.WarnContext(ctx, "requests: plugin returned unknown quality; skipping", "request_id", req.ID, "quality", string(rt.Quality))
			continue
		}
		if returned[rt.Quality] || healthy[rt.Quality] {
			continue // dup-in-batch, or a healthy target already exists for this quality
		}
		if rt.ConnectionID != "" {
			if _, ok := connKind[rt.ConnectionID]; !ok {
				slog.WarnContext(ctx, "requests: plugin returned unknown connection id; skipping target", "request_id", req.ID, "connection_id", rt.ConnectionID)
				continue
			}
		}
		returned[rt.Quality] = true
		created, err := s.store.CreateTarget(ctx, Target{
			RequestID: req.ID, IntegrationID: rt.ConnectionID, IntegrationKind: connKind[rt.ConnectionID],
			Quality: rt.Quality, IsAnime: req.IsAnime, Status: StatusQueued,
		})
		if err != nil {
			return nil, err
		}
		status := rt.Status
		if status == "" || !validStatus[status] {
			status = StatusQueued // coerce unknown/empty status to the DB-valid default
		}
		updated, err := s.store.UpdateTargetStatus(ctx, created.ID, status, rt.ExternalID, rt.ExternalStatus, rt.Message, actor)
		if err != nil {
			return nil, err
		}
		if updated != nil {
			latest = updated
		}
	}
	// Any wanted quality the plugin did not fulfill is recorded as a failed target
	// rather than silently dropped, so it stays visible and Retry re-attempts it
	// (a failed target is not "healthy").
	const noTargetMsg = "fulfillment backend returned no target for this quality"
	for _, q := range want {
		if returned[q] {
			continue
		}
		created, err := s.store.CreateTarget(ctx, Target{
			RequestID: req.ID, Quality: q, IsAnime: req.IsAnime, Status: StatusFailed, LastError: noTargetMsg,
		})
		if err != nil {
			return nil, err
		}
		updated, err := s.store.UpdateTargetStatus(ctx, created.ID, StatusFailed, "", "", noTargetMsg, actor)
		if err != nil {
			return nil, err
		}
		if updated != nil {
			latest = updated
		}
	}
	return latest, nil
}

// connectionKindByID maps each connection id to its plugin-declared service kind
// (e.g. "radarr"/"sonarr") from PluginConfig["service_kind"], for the
// integration_kind column on persisted targets. Missing kinds map to "".
func connectionKindByID(conns []ResolvedRouterConnection) map[string]string {
	out := make(map[string]string, len(conns))
	for _, c := range conns {
		out[c.ID] = ""
		if c.Config != nil {
			if kind, ok := c.Config["service_kind"].(string); ok {
				out[c.ID] = kind
			}
		}
	}
	return out
}

func filterUnconfiguredOptionalQualities(qualities []Quality, conns []ResolvedRouterConnection) []Quality {
	out := make([]Quality, 0, len(qualities))
	for _, q := range qualities {
		if q == Quality2160p && !routerQualityConfigured(q, conns) {
			continue
		}
		out = append(out, q)
	}
	return out
}

func routerQualityConfigured(q Quality, conns []ResolvedRouterConnection) bool {
	usesTieredDefaults := false
	for _, conn := range conns {
		if conn.Config == nil {
			continue
		}
		if hasRouterQualityKey(conn.Config) {
			usesTieredDefaults = true
		}
		if q == Quality2160p && boolConfig(conn.Config, "is_default_4k") {
			return true
		}
	}
	// Generic request_router implementations may not expose arr-style HD/4K
	// default flags. In that case, preserve the host's requested qualities and
	// let the plugin decide what it can fulfill.
	return !usesTieredDefaults
}

func hasRouterQualityKey(config map[string]any) bool {
	for _, key := range []string{"is_default", "is_default_4k", "is_4k"} {
		if _, ok := config[key]; ok {
			return true
		}
	}
	return false
}

func boolConfig(config map[string]any, key string) bool {
	v, ok := config[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
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

func (s *Service) reconcileRequest(ctx context.Context, req Request, fc *fulfillContext) (reconcileChange, error) {
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
		updated, err := s.submitApprovedRequest(ctx, req, Viewer{}, fc)
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
	if s.router == nil {
		return reconcileUnchanged, nil
	}
	conns, installationID, capabilityID, err := s.resolveRouterConnections(ctx, fc, req.MediaType)
	if err != nil {
		return reconcileUnchanged, err
	}
	if len(conns) == 0 {
		return reconcileUnchanged, nil
	}

	var refs []RouterTargetRef
	for _, t := range targets {
		if t.Status == StatusCompleted || t.Status == StatusFailed {
			continue
		}
		refs = append(refs, RouterTargetRef{Quality: t.Quality, ConnectionID: t.IntegrationID, ExternalID: t.ExternalID})
	}
	if len(refs) == 0 {
		return reconcileUnchanged, nil
	}

	statuses, err := s.router.CheckStatus(ctx, installationID, capabilityID, req, refs, conns)
	if err != nil {
		return reconcileUnchanged, err
	}

	change := reconcileUnchanged
	for _, st := range statuses {
		// Match the returned status to the live target by (quality, connection).
		var target *Target
		for i := range targets {
			if targets[i].Quality == st.Quality && targets[i].IntegrationID == st.ConnectionID {
				target = &targets[i]
				break
			}
		}
		if target == nil || target.Status == StatusCompleted || target.Status == StatusFailed {
			continue
		}
		newStatus := st.Status
		if newStatus == "" || newStatus == target.Status {
			continue
		}
		if _, err := s.store.UpdateTargetStatus(ctx, target.ID, newStatus, "", st.ExternalStatus, st.Message, Viewer{}); err != nil {
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

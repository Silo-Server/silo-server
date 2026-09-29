package requests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// TMDBExternalIDRefresher bypasses the client's external-ID cache. Detected by
// type assertion, like TMDBExternalIDClient.
type TMDBExternalIDRefresher interface {
	RefreshExternalIDs(ctx context.Context, mediaType string, id int) (*tmdb.ExternalIDs, error)
}

// TMDBCertificationClient resolves a title's content rating. Detected by type
// assertion on the service's TMDBClient, like TMDBExternalIDClient.
type TMDBCertificationClient interface {
	GetCertification(ctx context.Context, mediaType string, id int) (string, error)
}

const externalIDHydrationConcurrency = 4

const certificationHydrationConcurrency = 8

type EntitlementResolver interface {
	// MaxPlaybackQuality returns the requester's effective playback-quality
	// ceiling (already combining account- and profile-level caps). Empty string
	// means "no cap".
	MaxPlaybackQuality(ctx context.Context, userID int, profileID string) (string, error)
}

// ContentRatingResolver resolves the viewer's effective parental rating
// ceiling. Detected by type assertion on the service's EntitlementResolver so
// existing EntitlementResolver fakes keep compiling.
type ContentRatingResolver interface {
	// MaxContentRating returns the profile's content-rating ceiling. Empty
	// string means "no ceiling".
	MaxContentRating(ctx context.Context, userID int, profileID string) (string, error)
}

// RequesterIdentityResolver resolves a requesting user id into the identity a
// per-user request_router plugin needs (e.g. Seerr attribution by email).
type RequesterIdentityResolver interface {
	ResolveRequester(ctx context.Context, userID int) (email, username string, err error)
}

// TVDBIDResolver finds a series' TVDB ID from its other IDs when TMDB has no
// TVDB cross-reference, by asking the configured metadata providers (TVDB's own
// remote-ID search). It returns 0 when no provider knows the series.
type TVDBIDResolver interface {
	ResolveSeriesTVDBID(ctx context.Context, tmdbID int, imdbID string) (int, error)
}

type Service struct {
	store             Store
	tmdb              TMDBClient
	animeIndex        AnimeIndex
	presence          PresenceResolver
	router            RequestRouterProvider
	entitlements      EntitlementResolver
	groupProvider     access.GroupPolicyProvider
	users             access.UserRepository
	requesterIdentity RequesterIdentityResolver
	tvdbResolver      TVDBIDResolver
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
	// NextPage is the cursor to request for the following page, needed when
	// rating-filter backfill consumes more than one TMDB page per request
	// (page+1 would repeat consumed pages). 0 when there are no more pages.
	// Additive v1 field; absent (0) also when the viewer is unrestricted and
	// plain page+1 semantics apply.
	NextPage int `json:"next_page,omitempty"`
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

// SetUserRepository wires the account loader so the per-user requests_allowed
// override is honored on top of the access group's gate.
func (s *Service) SetUserRepository(users access.UserRepository) { s.users = users }

func (s *Service) SetRequesterIdentityResolver(r RequesterIdentityResolver) {
	s.requesterIdentity = r
}

// SetTVDBIDResolver wires the metadata-provider fallback used when TMDB has no
// TVDB ID for a requested series.
func (s *Service) SetTVDBIDResolver(r TVDBIDResolver) { s.tvdbResolver = r }

// populateRequesterIdentity fills req.RequesterEmail/Username from the resolver.
// Nil resolver or any error leaves them empty (the plugin then behaves as admin).
func (s *Service) populateRequesterIdentity(ctx context.Context, req *Request) {
	if s.requesterIdentity == nil || req.RequestedByUserID <= 0 {
		return
	}
	email, username, err := s.requesterIdentity.ResolveRequester(ctx, req.RequestedByUserID)
	if err != nil {
		slog.WarnContext(ctx, "requests: requester identity resolve failed; attributing to admin", "component", "requests", "user_id", req.RequestedByUserID, "error", err)
		return
	}
	req.RequesterEmail, req.RequesterUsername = email, username
}

// requesterCeiling resolves the requester's playback quality ceiling. resolved
// is false when the lookup failed and the HD-only fail-safe was used instead.
func (s *Service) requesterCeiling(ctx context.Context, userID int, profileID string) (ceiling string, resolved bool) {
	if s.entitlements == nil {
		return "", true // no resolver -> unlimited (1080p baseline still applies)
	}
	q, err := s.entitlements.MaxPlaybackQuality(ctx, userID, profileID)
	if err != nil {
		return access.PlaybackQualityStandard, false // fail safe: HD only
	}
	return q, true
}

// viewerContentCeiling resolves the viewer's parental rating ceiling. Empty
// string means unrestricted. Unlike the quality ceiling this is a safety
// filter, so a resolver error propagates instead of degrading: silently
// treating a failed lookup as "unrestricted" would leak adult content to a
// kid profile, and treating it as "restricted" would render every carousel
// empty with no visible cause.
//
// access.unrated_content does not apply here. It governs titles already in
// the library; a TMDB title with no US certification stays hidden from a
// ceilinged profile, which is also all the certification.lte push-down can
// express.
func (s *Service) viewerContentCeiling(ctx context.Context, viewer Viewer) (string, error) {
	resolver, ok := s.entitlements.(ContentRatingResolver)
	if !ok {
		return "", nil
	}
	return resolver.MaxContentRating(ctx, viewer.UserID, viewer.ProfileID)
}

type certKey struct {
	mediaType MediaType
	id        int
}

// hydrateCertifications resolves content ratings for every unique
// (mediaType, id) pair on the page. The TMDB client caches certifications
// title-keyed with a long TTL, so in steady state this issues no requests.
func (s *Service) hydrateCertifications(ctx context.Context, raw *tmdb.MediaPage) (map[certKey]string, error) {
	client, ok := s.tmdb.(TMDBCertificationClient)
	if !ok {
		return nil, fmt.Errorf("requests: tmdb client cannot resolve certifications")
	}

	keys := make([]certKey, 0, len(raw.Results))
	seen := map[certKey]bool{}
	for _, item := range raw.Results {
		mediaType, err := normalizeMediaType(MediaType(item.MediaType))
		if err != nil || item.ID <= 0 {
			continue
		}
		key := certKey{mediaType: mediaType, id: item.ID}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}

	certs := make([]string, len(keys))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(certificationHydrationConcurrency)
	for i, key := range keys {
		i, key := i, key
		group.Go(func() error {
			cert, err := client.GetCertification(gctx, tmdbMediaType(key.mediaType), key.id)
			if err != nil {
				return err
			}
			certs[i] = cert
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	out := make(map[certKey]string, len(keys))
	for i, key := range keys {
		out[key] = certs[i]
	}
	return out, nil
}

// Section backfill: fail-closed filtering hides most of a TMDB section page
// for a restricted profile (typically 15+ of 20, since unrated titles are
// dropped), which renders as a nearly empty carousel. To compensate, a
// restricted viewer's section page consumes consecutive TMDB pages, starting
// at the requested page, until a page's worth of titles survives filtering or
// the per-request budget (sectionBackfillMaxPagesPerRequest) is spent.
//
// Pagination stays honest through two properties: `page` keeps plain TMDB
// cursor semantics (same as for unrestricted viewers), and the response's
// next_page reports the cursor after the last TMDB page actually consumed.
// Every survivor from a consumed page is returned — nothing is trimmed and no
// consumed page is partially dropped — so resuming at next_page never skips
// or repeats an allowed title. The budget also bounds the cold-cache cost: a
// permissive ceiling (R/TV-MA) fills from one TMDB page and stops immediately,
// while a strict ceiling spends at most the budget per request.
const (
	// Per-request TMDB page budgets. A single-section request can afford a
	// deeper scan than the six-section DiscoverAll aggregate: on a cold
	// certification cache each consumed page costs up to 20 cert lookups
	// against the client's rate limiter, so DiscoverAll's worst case is
	// 6 sections x sectionBackfillBudgetAggregate pages x 20. Keeping the
	// aggregate budget small bounds the first-paint latency for a restricted
	// profile; the per-section endpoint (carousel "load more") gets the
	// deeper budget. Steady state is unaffected — certifications are cached
	// for 7 days, shared across all profiles.
	sectionBackfillBudgetSingle    = 5
	sectionBackfillBudgetAggregate = 2
	sectionResultsPerPage          = 20
	sectionBackfillMaxPage         = 500 // TMDB hard-caps page at 500
)

func (s *Service) backfillSectionPage(ctx context.Context, section string, page, pageBudget int, ceiling string) (result *tmdb.MediaPage, nextPage int, err error) {
	if page <= 0 {
		page = 1
	}
	if pageBudget <= 0 {
		pageBudget = 1
	}

	out := &tmdb.MediaPage{Page: page}
	var results []tmdb.MediaResult
	// Trending/popular orderings shift between fetches, so consecutive TMDB
	// pages can overlap; dedupe within the request.
	seen := map[certKey]bool{}
	totalPages := page // until TMDB tells us the real count, assume the current page exists
	consumed := 0
	for next := page; consumed < pageBudget && next <= totalPages && next <= sectionBackfillMaxPage; next++ {
		// Enough survived — stop before spending another TMDB page. Titles on
		// unconsumed pages are not lost: next_page points at the first page
		// this request did not consume.
		if len(results) >= sectionResultsPerPage {
			break
		}
		raw, err := s.tmdb.DiscoverSection(ctx, section, next)
		if err != nil {
			return nil, 0, err
		}
		if raw == nil {
			break
		}
		consumed++
		nextPage = next + 1
		if raw.TotalPages > 0 {
			totalPages = raw.TotalPages
			out.TotalResults = raw.TotalResults
		}
		filtered, err := s.filterPageByCeiling(ctx, raw, ceiling)
		if err != nil {
			return nil, 0, err
		}
		for _, item := range filtered.Results {
			key := certKey{mediaType: MediaType(item.MediaType), id: item.ID}
			if !seen[key] {
				seen[key] = true
				results = append(results, item)
			}
		}
	}
	out.Results = results
	// TotalPages/TotalResults stay TMDB's unfiltered counts — page keeps TMDB
	// cursor semantics, and the filtered totals are unknowable without a full
	// scan. next_page is the honest resume cursor.
	out.TotalPages = totalPages
	if nextPage > totalPages || nextPage > sectionBackfillMaxPage {
		nextPage = 0 // exhausted
	}
	return out, nextPage, nil
}

// ensureCreateAllowedByCeiling rejects request submissions for titles above
// the viewer's rating ceiling. List filtering alone is cosmetic — the create
// endpoint is directly callable with a guessable TMDB id.
func (s *Service) ensureCreateAllowedByCeiling(ctx context.Context, viewer Viewer, input CreateRequestInput) error {
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return err
	}
	if ceiling == "" {
		return nil
	}
	client, ok := s.tmdb.(TMDBCertificationClient)
	if !ok {
		return fmt.Errorf("requests: tmdb client cannot resolve certifications")
	}
	cert, err := client.GetCertification(ctx, tmdbMediaType(input.MediaType), input.TMDBID)
	if err != nil {
		return err
	}
	if !access.RatingAllowed(cert, ceiling) {
		return ErrForbidden
	}
	return nil
}

// filterPageByCeiling drops results whose certification exceeds the ceiling,
// failing closed on missing or unrecognized certifications. TMDB's own
// certification.lte pre-filter (applied on browse paths) is not trusted for
// this: it ranks "NR" below "G" and matches titles when any one of several
// US cert entries qualifies, both of which leak over-ceiling titles.
// TotalPages/TotalResults are intentionally left as TMDB reported them —
// recomputing them would require scanning every page, and short pages are
// benign for the carousel/browse UIs.
func (s *Service) filterPageByCeiling(ctx context.Context, raw *tmdb.MediaPage, ceiling string) (*tmdb.MediaPage, error) {
	certs, err := s.hydrateCertifications(ctx, raw)
	if err != nil {
		return nil, err
	}
	filtered := *raw
	filtered.Results = make([]tmdb.MediaResult, 0, len(raw.Results))
	for _, item := range raw.Results {
		mediaType, err := normalizeMediaType(MediaType(item.MediaType))
		if err != nil || item.ID <= 0 {
			continue
		}
		if access.RatingAllowed(certs[certKey{mediaType: mediaType, id: item.ID}], ceiling) {
			filtered.Results = append(filtered.Results, item)
		}
	}
	return &filtered, nil
}

// allowedQualities returns the qualities a request may receive: 1080p always,
// plus 2160p when force-dual is on or the requester's entitlement ceiling allows 4K.
// allowedQualities returns the qualities the request should be fulfilled in.
// resolved is false when the requester's entitlement could not be looked up,
// so the answer is the HD-only fail-safe rather than the real policy.
func (s *Service) allowedQualities(ctx context.Context, req Request, settings Settings) (qualities []Quality, resolved bool) {
	out := []Quality{Quality1080p}
	ceiling, resolved := s.requesterCeiling(ctx, req.RequestedByUserID, req.RequestedByProfileID)
	// QualityAllowed treats an empty ceiling as "no cap" (the "Any" preset), so a
	// requester with unlimited playback quality correctly gets 4K. A raw
	// CompareQuality would rank "" as the LOWEST quality and wrongly drop 4K.
	if settings.ForceDualQuality || access.QualityAllowed(access.PlaybackQuality4K, ceiling) {
		out = append(out, Quality2160p)
	}
	return out, resolved
}

// fulfillContext caches the global fulfillment inputs for one reconcile cycle
// (or a single Approve/Retry) so integrations and settings are fetched once
// instead of per request. API keys need no cache here: the repository decrypts
// api_key_ref on read, so Integration.APIKeyRef already holds the literal key.
type fulfillContext struct {
	integrations []Integration
	settings     Settings
	routes       []Route
	// standard holds where Standard routing sends each media type, when
	// Standard is on and the servers allow it.
	standard []StandardDestination
	// standardOn is set when Standard routing is in effect.
	standardOn bool

	// mu guards features, which caches the features each router capability
	// declares (see routerFeatures).
	mu       sync.Mutex
	features map[routerCapabilityKey]RouterFeatures
}

// routesFor returns the media type's routing rules; none means the router
// plugin routes the media type itself. Under Standard the rules are paused
// and the media type's one server decides.
func (fc *fulfillContext) routesFor(mediaType MediaType) []Route {
	if fc.standardOn {
		return standardRoutes(fc.integrations, fc.standard, mediaType)
	}
	var out []Route
	for _, route := range fc.routes {
		if route.MediaType == mediaType {
			out = append(out, route)
		}
	}
	return out
}

func (s *Service) newFulfillContext(ctx context.Context) (*fulfillContext, error) {
	// The mode is read before the servers and the rules, in separate queries.
	// A switch to Advanced writes Everything else and the mode in one commit,
	// so reading the mode first sees either Standard, which ignores the rules
	// (and routes with the rules read after it when the servers read after it
	// no longer allow Standard), or Advanced with the rules it was committed
	// with. Reading the rules first could pair the old rules with Advanced.
	mode := RoutingAdvanced
	if store, ok := s.store.(RoutingModeStore); ok {
		routing, err := store.GetRoutingSettings(ctx)
		if err != nil {
			return nil, err
		}
		mode = routing.Mode
	}
	integrations, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	fc := &fulfillContext{integrations: integrations, settings: settings, routes: routes}
	if mode == RoutingStandard {
		// Standard with two servers of a kind (saved around a server
		// change) routes with the rules until an admin sorts it out.
		layout, blocker := standardLayout(integrations)
		fc.standard, fc.standardOn = layout, blocker == ""
	}
	return fc, nil
}

// resolveRouterConnections turns enabled request_router integrations that serve
// the given media type into ResolvedRouterConnections (api key resolved to
// plaintext, plugin_config attached), and returns the installation+capability to
// dispatch to.
//
// It filters by media type (so a series-only connection is never used for a
// movie request). Multi-
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
			slog.WarnContext(ctx, "requests: skipping router connection with no api key", "component", "requests", "connection_id", in.ID)
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

// Reasons a configured router connection cannot take a submission, recorded in
// the request's last_error.
const (
	msgRouterUnbound = "request backend connection is not bound to a plugin installation; re-save it in admin"
	msgRouterNoKey   = "request backend connection has no API key; add it in admin"
)

// unusableRouterMessage explains why no configured connection could take a
// submission, for the request's last_error.
func unusableRouterMessage(fc *fulfillContext, mediaType MediaType) string {
	for _, in := range fc.integrations {
		if !in.Enabled || in.CapabilityID == "" || !integrationSupportsMediaType(in, mediaType) {
			continue
		}
		if in.InstallationID == nil {
			// The migration left installation_id NULL on rows that predate the
			// plugin install and were never re-bound.
			return msgRouterUnbound
		}
		if strings.TrimSpace(in.APIKeyRef) == "" {
			return msgRouterNoKey
		}
	}
	return "no usable request backend connection"
}

// moreSeasonsRequestable reports whether a series already in the library can
// be requested for the seasons it is missing. The library fulfills such a
// request when no download server takes series. A download server fetches
// only the missing seasons when its router plugin declares supports_seasons;
// any other plugin would add the whole series again, refused by a download
// server that has it and every season downloaded by one that does not. So it
// is offered when every download server that takes series is bound to a
// plugin that takes seasons. A request made before a server that cannot was
// set up waits for the library (see submitApprovedRequest).
func (s *Service) moreSeasonsRequestable(ctx context.Context) (bool, error) {
	if s.router == nil {
		return true, nil
	}
	fc, err := s.newFulfillContext(ctx)
	if err != nil {
		return false, err
	}
	return s.allTakeSeasons(ctx, fc, seriesRouterConnections(fc))
}

// routerConfiguredFor reports whether any enabled router connection is meant to
// serve the media type, including a misconfigured one (no installation bound,
// no key). Only when none is does a request fall back to waiting for the
// library; a misconfigured connection surfaces as a submission failure instead.
func routerConfiguredFor(fc *fulfillContext, mediaType MediaType) bool {
	for _, in := range fc.integrations {
		if in.Enabled && in.CapabilityID != "" && integrationSupportsMediaType(in, mediaType) {
			return true
		}
	}
	return false
}

// skippedRouterConnection reports whether resolveRouterConnections leaves out
// a connection that would otherwise serve the media type, because its API key
// is missing.
func skippedRouterConnection(fc *fulfillContext, mediaType MediaType) bool {
	for _, in := range fc.integrations {
		if eligibleRouterConnection(in, mediaType) && strings.TrimSpace(in.APIKeyRef) == "" {
			return true
		}
	}
	return false
}

// eligibleRouterConnection reports whether a connection is a candidate fulfillment
// backend for the media type: enabled, bound to an installation, and naming a
// capability sub-id that serves the media type. resolveRouterConnections then
// resolves credentials for the ones it uses.
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
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	return s.discover(ctx, viewer, section, page, sectionBackfillBudgetSingle, ceiling)
}

// discover renders one section for an already-resolved ceiling. Callers own
// the ceiling lookup so a DiscoverAll fan-out resolves the viewer scope once,
// not once per section per page.
func (s *Service) discover(ctx context.Context, viewer Viewer, section string, page, backfillBudget int, ceiling string) (*DiscoverySection, error) {
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
	var raw *tmdb.MediaPage
	var err error
	var nextPage int
	if ceiling != "" {
		raw, nextPage, err = s.backfillSectionPage(ctx, section, page, backfillBudget, ceiling)
	} else {
		raw, err = s.tmdb.DiscoverSection(ctx, section, page)
	}
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPageWithCeiling(ctx, viewer, raw, ceiling)
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
		NextPage:     nextPage,
	}, nil
}

func (s *Service) DiscoverAll(ctx context.Context, viewer Viewer) ([]DiscoverySection, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	ctx = withPolicyCache(ctx)
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return nil, err
	}
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	sections := make([]DiscoverySection, len(discoverySectionOrder))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(externalIDHydrationConcurrency)
	for i, key := range discoverySectionOrder {
		i, key := i, key
		group.Go(func() error {
			section, err := s.discover(gctx, viewer, key, 1, sectionBackfillBudgetAggregate, ceiling)
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
	ctx = withPolicyCache(ctx)
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

	// Deep-linking a detail page must not bypass the discovery rating filter.
	// The guard uses the US-only enforcement certification (GetCertification,
	// cached), NOT raw.ContentRating: the display rating falls back to any
	// country's cert, and a foreign "PG"/"G" is the same string as the US
	// rating, so it would pass the US ladder. ErrNotFound rather than
	// ErrForbidden: a restricted profile shouldn't learn the title exists.
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	if ceiling != "" {
		client, ok := s.tmdb.(TMDBCertificationClient)
		if !ok {
			return nil, fmt.Errorf("requests: tmdb client cannot resolve certifications")
		}
		cert, err := client.GetCertification(ctx, tmdbMediaType(mediaType), tmdbID)
		if err != nil {
			return nil, err
		}
		if !access.RatingAllowed(cert, ceiling) {
			return nil, ErrNotFound
		}
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
	primaryFollowing, err := s.followedTitles(ctx, viewer, mediaType, primaryRequests)
	if err != nil {
		return nil, err
	}
	// A series counts as available only when every aired season is complete;
	// until then its missing seasons can be requested.
	available := primaryMatch.Available
	var seasons []RequestSeason
	if mediaType == MediaTypeSeries {
		counts, err := s.seasonCounts(ctx, primaryMatch)
		if err != nil {
			return nil, err
		}
		seasons = requestSeasons(raw, counts, primaryRequests[raw.ID])
		if active := primaryRequests[raw.ID]; active != nil && len(active.Seasons) > 0 && counts != nil {
			// Attach the season progress the request lists attach, so the
			// state can read partially available or available.
			withProgress := *active
			withProgress.LibraryContentID = primaryMatch.ContentID
			withProgress.SeasonProgress = seasonProgress(active.Seasons, counts)
			primaryRequests[raw.ID] = &withProgress
		}
		if available && seriesHasOpenSeason(raw, counts) {
			more, err := s.moreSeasonsRequestable(ctx)
			if err != nil {
				return nil, err
			}
			available = !more
		}
	}
	primaryState := requestStateFor(viewer, policy, available, primaryRequests[raw.ID])
	primaryState.Following = primaryRequests[raw.ID] != nil && primaryFollowing[raw.ID]
	if primaryState.Download, err = s.activeRequestDownload(ctx, primaryRequests[raw.ID]); err != nil {
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
		Request:             primaryState,
		Seasons:             seasons,
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
		enriched, err := s.enrichPageWithCeiling(ctx, viewer, recPage, ceiling)
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
	if err := s.ensureCreateAllowedByCeiling(ctx, viewer, normalized); err != nil {
		return nil, err
	}
	tvdbLookupFailed := s.enrichExternalIDs(ctx, &normalized)

	matches, err := s.lookupPresence(ctx, normalized.MediaType, []PresenceCandidate{createPresenceCandidate(normalized)})
	if err != nil {
		return nil, err
	}
	match := matches[normalized.TMDBID]
	if match.Available {
		if normalized.MediaType == MediaTypeMovie || normalized.WholeSeries {
			return nil, ErrAlreadyAvailable
		}
		more, err := s.moreSeasonsRequestable(ctx)
		if err != nil {
			return nil, err
		}
		if !more {
			return nil, ErrAlreadyAvailable
		}
	}

	active, err := s.store.ListActiveByTMDB(ctx, normalized.MediaType, []int{normalized.TMDBID})
	if err != nil {
		return nil, err
	}
	if active[normalized.TMDBID] != nil {
		return nil, ErrAlreadyRequested
	}

	// One TMDB detail read, after the cheap refusals, serves routing and the
	// stored title: the server's copy of the title and year wins over the
	// client's.
	detail := s.requestDetail(ctx, normalized.MediaType, normalized.TMDBID)
	if detail != nil {
		if title := strings.TrimSpace(detail.Title); title != "" {
			normalized.Title = title
		}
		if detail.Year > 0 {
			year := detail.Year
			normalized.Year = &year
		}
	}
	facts := s.routingFacts(ctx, detail)
	if normalized.MediaType == MediaTypeSeries && !normalized.WholeSeries {
		// A series partly in the library can still be requested for the
		// seasons it is missing.
		seasons, err := s.resolveRequestedSeasons(ctx, normalized.Seasons, detail, match)
		if err != nil {
			return nil, err
		}
		normalized.Seasons = seasons
	}

	policy, err := s.EffectivePolicy(ctx, viewer.UserID)
	if err != nil {
		return nil, err
	}
	if err := validateCreateAccess(policy); err != nil {
		return nil, err
	}

	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	// Auto-approval does not depend on a router: without one, an approved
	// request waits for the title to appear in the library.
	status := StatusPending
	if policy.AutoApprove {
		status = StatusApproved
	}
	record := CreateRequestRecord{
		ID:        id,
		Input:     normalized,
		Status:    status,
		Outcome:   OutcomeActive,
		IsAnime:   facts.Anime,
		Facts:     facts,
		Requester: viewer,
		Now:       s.now(),
		// Re-requesting a title that failed for this user (e.g. a transient
		// integration error) replaces their failed row.
		ReplaceFailed: true,
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
		s.notifyApproval(ctx, *req, ApprovalOriginPolicy)
		req.externalIDsResolved = true
		req.tvdbLookupFailed = tvdbLookupFailed
		return s.withLibraryContent(ctx, s.submitAfterCommit(ctx, *req, viewer)), nil
	}
	return s.withLibraryContent(ctx, req), nil
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
	if filter.View != "" && !filter.View.Valid() {
		return nil, fmt.Errorf("%w: unknown view %q", ErrInvalidInput, filter.View)
	}
	if filter.MediaType != "" {
		mediaType, err := normalizeMediaType(filter.MediaType)
		if err != nil {
			return nil, err
		}
		filter.MediaType = mediaType
	}
	if utf8.RuneCountInString(filter.Query) > maxAdminQueryLength {
		return nil, fmt.Errorf("%w: search is longer than %d characters", ErrInvalidInput, maxAdminQueryLength)
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

// maxAdminQueryLength bounds the admin queue's title search.
const maxAdminQueryLength = 200

// maxRequestEvents bounds a request's history as the admin queue reads it.
const maxRequestEvents = 200

// CountAdminViews counts the requests in each admin queue view.
func (s *Service) CountAdminViews(ctx context.Context, viewer Viewer) (AdminViewCounts, error) {
	if !viewer.IsAdmin {
		return AdminViewCounts{}, ErrForbidden
	}
	return s.store.CountAdminViews(ctx)
}

// ListRequestEvents returns a request's history, newest first, for admins.
func (s *Service) ListRequestEvents(ctx context.Context, viewer Viewer, id string) ([]RequestEvent, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	id = strings.TrimSpace(id)
	if _, err := s.store.GetRequest(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListEvents(ctx, id, maxRequestEvents)
}

// attachTargets loads and attaches the per-instance fulfillment targets for each
// request so callers (admin queue, detail view) can surface multi-target status.
// One query serves the whole page.
func (s *Service) attachTargets(ctx context.Context, reqs ...*Request) error {
	ids := make([]string, 0, len(reqs))
	for _, r := range reqs {
		if r != nil {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	byRequest, err := s.store.ListTargetsForRequests(ctx, ids)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		if r != nil {
			r.Targets = byRequest[r.ID]
		}
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

	// Season requests whose series is in the library, by series content ID,
	// so one query reads every series' season counts.
	seasonRequests := map[string][]*Request{}
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
				if req.MediaType == MediaTypeSeries && len(req.Seasons) > 0 {
					seasonRequests[match.ContentID] = append(seasonRequests[match.ContentID], req)
				}
			}
		}
	}
	resolver, ok := s.presence.(SeasonPresenceResolver)
	if !ok || len(seasonRequests) == 0 {
		return nil
	}
	bySeries, err := resolver.SeasonAvailability(ctx, slices.Collect(maps.Keys(seasonRequests)))
	if err != nil {
		return err
	}
	for series, reqs := range seasonRequests {
		for _, req := range reqs {
			req.SeasonProgress = seasonProgress(req.Seasons, bySeries[series])
		}
	}
	return nil
}

// withLibraryContent attaches the library match and season progress to a
// request a mutation returns, so its state reads as a detail or list read
// would. The mutation has committed, so a lookup failure is logged and the
// request returned without them rather than reported as a failed mutation.
func (s *Service) withLibraryContent(ctx context.Context, req *Request) *Request {
	if err := s.attachLibraryContent(ctx, req); err != nil {
		slog.WarnContext(ctx, "requests: attach library content to mutation response failed", "component", "requests",
			"request_id", req.ID, "err", err)
	}
	return req
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
	approved, err := s.store.SetStatus(ctx, strings.TrimSpace(id), guardPending, StatusApproved, viewer)
	if err != nil {
		return nil, err
	}
	s.notifyApproval(ctx, *approved, ApprovalOriginAdmin)
	return s.withLibraryContent(ctx, s.submitAfterCommit(ctx, *approved, viewer)), nil
}

// Decline rejects a request nothing has been sent for: a pending one, or an
// approved one still waiting for the library or backing off. Once a submission
// is in flight or a target exists, declining could leave the downstream
// service's state diverged from Silo's, so the guard refuses it.
func (s *Service) Decline(ctx context.Context, viewer Viewer, id, reason string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	declined, err := s.store.SetOutcome(ctx, strings.TrimSpace(id), guardWithdrawable, OutcomeDeclined, viewer, reason)
	if err != nil {
		return nil, err
	}
	s.notifyLifecycle(ctx, *declined, LifecycleNotifier.RequestDeclined)
	return declined, nil
}

// Cancel withdraws a request that has not been sent to a downstream service:
// pending, or approved but not yet sent (see guardWithdrawable). Owners can
// withdraw their own; admins can withdraw any. Once a submission is in flight
// or a target exists, the request stays in the pipeline until it completes or
// fails.
func (s *Service) Cancel(ctx context.Context, viewer Viewer, id, reason string) (*Request, error) {
	return s.cancel(ctx, viewer, id, reason, false)
}

// AdminCancel is Cancel for the admin queue, which may also close a failed
// request instead of retrying it. The v1 cancel keeps refusing failed
// requests.
func (s *Service) AdminCancel(ctx context.Context, viewer Viewer, id, reason string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	return s.cancel(ctx, viewer, id, reason, true)
}

func (s *Service) cancel(ctx context.Context, viewer Viewer, id, reason string, closeFailed bool) (*Request, error) {
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
	guard := guardWithdrawable
	if closeFailed && req.Outcome == OutcomeFailed {
		// Closing a failed request moves it out of the admin's failed view;
		// nothing more is sent for it.
		guard = guardFailed
	}
	withdrawn, err := s.store.SetOutcome(ctx, req.ID, guard, OutcomeCancelled, viewer, reason)
	if err != nil {
		return nil, err
	}
	return withdrawn, nil
}

func (s *Service) Retry(ctx context.Context, viewer Viewer, id string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	reopened, err := s.store.ReopenFailed(ctx, strings.TrimSpace(id), viewer)
	if err != nil {
		return nil, err
	}
	return s.withLibraryContent(ctx, s.submitAfterCommit(ctx, *reopened, viewer)), nil
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
	present, err := s.presentRequests(ctx, candidates)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Checked: len(candidates)}
	for _, req := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		change, err := s.reconcileRequest(ctx, *req, fc, present[req.ID])
		// Stamp every candidate, including ones that errored, so the next pass
		// starts with the requests this one did not reach.
		if markErr := s.store.MarkReconciled(ctx, req.ID); markErr != nil {
			slog.WarnContext(ctx, "request reconcile stamp failed", "component", "requests", "request_id", req.ID, "err", markErr)
		}
		if err != nil {
			slog.WarnContext(ctx, "request reconcile failed", "component", "requests",
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
		case reconcileDeferred:
			result.Deferred++
		case reconcileSkipped:
			result.Skipped++
		}
	}
	if err := s.completeWaitingFromLibrary(ctx, 2*limit, &result); err != nil {
		return result, err
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
	// Rating enforcement is active when the wiring can resolve both a
	// profile ceiling and per-title certifications; with either missing the
	// server behaves like an older version, and clients should know that.
	_, hasRatings := s.entitlements.(ContentRatingResolver)
	_, hasCerts := s.tmdb.(TMDBCertificationClient)
	status := FeatureStatus{
		RequestsEnabled:            settings.RequestsEnabled,
		RatingRestrictionsEnforced: hasRatings && hasCerts,
	}
	if settings.RequestsEnabled {
		if status.MissingSeasonsRequestable, err = s.moreSeasonsRequestable(ctx); err != nil {
			return FeatureStatus{}, err
		}
	}
	return status, nil
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

// ensureViewerRequestsAllowed enforces the viewer's resolved requests gate
// (account override on top of the access group). The account loader is
// required: without it the gate could only see the group layer, which would
// silently ignore a per-user deny, so a missing repository is a wiring error
// rather than a permissive fallback.
func (s *Service) ensureViewerRequestsAllowed(ctx context.Context, userID int) error {
	if s.users == nil {
		return fmt.Errorf("requests: user repository is not configured")
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return ErrForbidden
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, s.groupProvider)
	if err != nil {
		return ErrForbidden
	}
	if !effective.RequestsAllowed {
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
	if store, ok := s.store.(interface {
		UserExists(context.Context, int) (bool, error)
	}); ok {
		exists, err := store.UserExists(ctx, userID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
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
	if err := s.ensureRoutesKeepServerKind(ctx, in); err != nil {
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
			if strings.TrimSpace(in.BaseURL) != "" && !sameIntegrationBaseURL(in.BaseURL, stored.BaseURL) {
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
			if submittedBaseURL == "" {
				integration.BaseURL = stored.BaseURL
			}
			if strings.TrimSpace(integration.APIKeyRef) == "" && (submittedBaseURL == "" || sameIntegrationBaseURL(submittedBaseURL, stored.BaseURL)) {
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

	// Without a key the plugin could only fail; say so on the key field. The
	// address is passed as given: the v2 adapter normalizes it first, and the
	// frozen v1 route keeps sending what the client submitted.
	apiKey := strings.TrimSpace(integration.APIKeyRef)
	if apiKey == "" {
		return nil, probeValidation(&ValidationError{FieldErrors: map[string]string{fieldAPIKey: integrationKeyMissing}})
	}
	if s.router == nil || integration.InstallationID == nil {
		return nil, fmt.Errorf("no fulfillment backend configured")
	}
	conn := ResolvedRouterConnection{ID: integration.ID, BaseURL: integration.BaseURL, APIKey: apiKey, Config: integration.PluginConfig}
	options, err := s.router.ListConfigOptions(ctx, *integration.InstallationID, integration.CapabilityID, conn)
	if err != nil {
		return nil, classifyIntegrationError(err, integration.CapabilityID)
	}
	return options, nil
}

func (s *Service) EffectivePolicy(ctx context.Context, userID int) (EffectivePolicy, error) {
	cache, _ := ctx.Value(policyCacheKey{}).(*policyCache)
	if cache == nil {
		return s.resolvePolicy(ctx, userID)
	}
	// Held while resolving, so sections enriched concurrently wait for the
	// first rather than each reading the account, group and quota again.
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if policy, ok := cache.byUser[userID]; ok {
		return policy, nil
	}
	policy, err := s.resolvePolicy(ctx, userID)
	if err == nil {
		cache.byUser[userID] = policy
	}
	return policy, err
}

// policyCache shares a viewer's resolved policy across the page enrichments
// of one call (DiscoverAll's sections, a detail and its recommendations).
type policyCache struct {
	mu     sync.Mutex
	byUser map[int]EffectivePolicy
}

type policyCacheKey struct{}

func withPolicyCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(policyCacheKey{}).(*policyCache); ok {
		return ctx
	}
	return context.WithValue(ctx, policyCacheKey{}, &policyCache{byUser: map[int]EffectivePolicy{}})
}

func (s *Service) resolvePolicy(ctx context.Context, userID int) (EffectivePolicy, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return EffectivePolicy{}, err
	}
	limit, err := s.store.GetUserLimit(ctx, userID)
	if err != nil {
		return EffectivePolicy{}, err
	}
	viewerAccess, err := s.viewerRequestAccess(ctx, userID)
	if err != nil {
		return EffectivePolicy{}, err
	}

	policy := EffectivePolicy{
		RequestsEnabled: settings.RequestsEnabled,
		MaxRequests:     settings.GlobalMaxRequests,
		WindowDays:      settings.GlobalWindowDays,
		AutoApprove:     settings.GlobalAutoApprovalEnabled,
		Blocked:         !viewerAccess.allowed,
	}
	if policy.WindowDays <= 0 {
		policy.WindowDays = 7
	}
	// The account's own limits win, then its access group's, then the
	// server's; a layer set to inherit defers to the next.
	limitMode, maxRequests, windowDays := LimitModeInherit, (*int)(nil), (*int)(nil)
	approval := ApprovalModeInherit
	layers := []*UserLimit{}
	if g := viewerAccess.group; g != nil {
		layers = append(layers, &UserLimit{LimitMode: g.LimitMode, MaxRequests: g.MaxRequests, WindowDays: g.WindowDays, ApprovalMode: g.ApprovalMode})
	}
	if limit != nil {
		layers = append(layers, limit)
	}
	for _, layer := range layers {
		if layer.LimitMode != "" && layer.LimitMode != LimitModeInherit {
			limitMode, maxRequests, windowDays = layer.LimitMode, layer.MaxRequests, layer.WindowDays
		}
		if layer.ApprovalMode != "" && layer.ApprovalMode != ApprovalModeInherit {
			approval = layer.ApprovalMode
		}
	}
	switch limitMode {
	case LimitModeBlocked:
		policy.Blocked = true
	case LimitModeUnlimited:
		policy.Unlimited = true
	case LimitModeCustom:
		if maxRequests != nil {
			policy.MaxRequests = *maxRequests
		}
		if windowDays != nil && *windowDays > 0 {
			policy.WindowDays = *windowDays
		}
	}
	switch approval {
	case ApprovalModeBlocked:
		policy.Blocked = true
	case ApprovalModeManual:
		policy.AutoApprove = false
	case ApprovalModeAuto:
		policy.AutoApprove = true
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
	ceiling, err := s.viewerContentCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	return s.enrichPageWithCeiling(ctx, viewer, raw, ceiling)
}

// enrichPageWithCeiling is enrichPage for callers that already resolved the
// viewer's rating ceiling (discovery, browse, detail). The production
// resolver loads the user, profile, and policy on every call, so resolving
// once per request instead of again per page matters — DiscoverAll otherwise
// doubles to 12 resolutions per load.
func (s *Service) enrichPageWithCeiling(ctx context.Context, viewer Viewer, raw *tmdb.MediaPage, ceiling string) (*MediaPage, error) {
	if raw == nil {
		return &MediaPage{Results: []MediaResult{}}, nil
	}
	var err error
	if ceiling != "" {
		// Filtering before the presence/active-request lookups below means
		// those (and their external-ID hydration) only pay for surviving items.
		raw, err = s.filterPageByCeiling(ctx, raw, ceiling)
		if err != nil {
			return nil, err
		}
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
	following := map[MediaType]map[int]bool{}
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
		if following[mediaType], err = s.followedTitles(ctx, viewer, mediaType, requests); err != nil {
			return nil, err
		}
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
		state := requestStateFor(viewer, policy, match.Available, activeRequest)
		state.Following = activeRequest != nil && following[mediaType][item.ID]
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
			Request:          state,
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

// enrichExternalIDs fills missing IMDb and TVDB IDs from TMDB and, for a
// series still without a TVDB ID, from the metadata providers. It reports
// whether a series is left without a TVDB ID because a lookup (TMDB or a
// metadata provider) failed, rather than because none exists.
func (s *Service) enrichExternalIDs(ctx context.Context, input *CreateRequestInput) (tvdbLookupFailed bool) {
	if input == nil {
		return false
	}
	tmdbFailed := false
	// Only TMDB's own IMDb ID is trusted for the provider lookup. A
	// caller-supplied one may be stale or belong to another series, and a TVDB
	// match found by it can't always be checked against the TMDB ID.
	lookupIMDbID := ""
	if client, ok := s.tmdb.(TMDBExternalIDClient); ok {
		externalIDs, err := client.GetExternalIDs(ctx, tmdbMediaType(input.MediaType), input.TMDBID)
		tmdbFailed = err != nil
		// A cached answer without a TVDB ID may predate the admin adding one
		// on TMDB (the failure message asks them to), so ask TMDB again. If
		// that fails, keep the cached IDs and record the failure.
		if refresher, ok := s.tmdb.(TMDBExternalIDRefresher); ok && input.MediaType == MediaTypeSeries &&
			input.TVDBID == nil && err == nil && (externalIDs == nil || externalIDs.TVDBID <= 0) {
			if fresh, refreshErr := refresher.RefreshExternalIDs(ctx, tmdbMediaType(input.MediaType), input.TMDBID); refreshErr == nil {
				externalIDs = fresh
			} else {
				tmdbFailed = true
			}
		}
		if err == nil && externalIDs != nil {
			lookupIMDbID = strings.TrimSpace(externalIDs.IMDbID)
			if input.IMDbID == "" {
				input.IMDbID = strings.TrimSpace(externalIDs.IMDbID)
			}
			if input.TVDBID == nil && externalIDs.TVDBID > 0 {
				tvdbID := externalIDs.TVDBID
				input.TVDBID = &tvdbID
			}
		}
	}
	// Sonarr adds series by TVDB ID only, and TMDB often lacks the
	// cross-reference for new or regional series. Ask the metadata providers
	// (TVDB's remote-ID search) before giving up.
	if input.MediaType == MediaTypeSeries && input.TVDBID == nil && s.tvdbResolver != nil {
		tvdbID, err := s.tvdbResolver.ResolveSeriesTVDBID(ctx, input.TMDBID, lookupIMDbID)
		if tvdbID > 0 {
			input.TVDBID = &tvdbID
		} else if err != nil {
			slog.WarnContext(ctx, "requests: resolve series tvdb id via metadata providers failed", "component", "requests",
				"tmdb_id", input.TMDBID, "err", err)
			return true
		}
	}
	return tmdbFailed && input.MediaType == MediaTypeSeries && input.TVDBID == nil
}

// ensureSeriesTVDBID looks up a series request's missing TVDB ID again right
// before submission (approve, Retry, reconcile), so an ID added on TMDB or TVDB
// since the request was created is used, and records it on the request.
func (s *Service) ensureSeriesTVDBID(ctx context.Context, req *Request) error {
	if req.MediaType != MediaTypeSeries || (req.TVDBID != nil && *req.TVDBID > 0) || req.externalIDsResolved {
		return nil
	}
	input := CreateRequestInput{MediaType: req.MediaType, TMDBID: req.TMDBID, IMDbID: strings.TrimSpace(req.IMDbID)}
	req.tvdbLookupFailed = s.enrichExternalIDs(ctx, &input)
	if input.TVDBID == nil {
		return nil
	}
	// Save before submitting: a backend that accepted the series under an ID
	// the request row doesn't carry would leave the two out of step.
	// Submit the ID the row holds: a concurrent submission may have saved a
	// different one first, and SetExternalIDs keeps it.
	saved, err := s.store.SetExternalIDs(ctx, req.ID, *input.TVDBID, input.IMDbID)
	if err != nil {
		return err
	}
	req.TVDBID = &saved
	if req.IMDbID == "" {
		req.IMDbID = input.IMDbID
	}
	return nil
}

// missingTVDBIDMessage replaces a backend's "TVDB ID required" error (for
// example "sonarr: tvdb_id is required") once every lookup has come up empty.
const missingTVDBIDMessage = "No TVDB ID found for this series. TMDB has none, and the metadata providers found no match on TVDB, " +
	"so the request backend can't add it. Add the TVDB ID on TMDB (or the TMDB or IMDb ID on TVDB), then retry."

// tvdbLookupFailedMessage replaces the same error when TMDB or a metadata
// provider failed during the lookup, so the admin isn't sent to fix IDs that
// may exist.
const tvdbLookupFailedMessage = "Couldn't look up a TVDB ID for this series because TMDB or a metadata provider failed during the lookup, " +
	"so the request backend can't add it yet. Check TMDB and the metadata providers, then retry."

// missingTVDBIDError matches backend errors about a missing TVDB ID, such as
// "sonarr: tvdb_id is required", and not other TVDB failures such as a
// missing API key.
var missingTVDBIDError = regexp.MustCompile(`(?i)tvdb[ _-]?id\b.*\b(required|missing)\b|\b(missing|no)\b.*\btvdb[ _-]?id`)

// explainSubmissionFailure turns a backend failure caused by a missing TVDB ID
// into an explanation the admin can act on. Other failures pass through.
func explainSubmissionFailure(req Request, msg string) string {
	if req.MediaType != MediaTypeSeries || (req.TVDBID != nil && *req.TVDBID > 0) || !missingTVDBIDError.MatchString(msg) {
		return msg
	}
	if req.tvdbLookupFailed {
		return tvdbLookupFailedMessage
	}
	return missingTVDBIDMessage
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

// submitLease bounds how long one server's submission claim keeps the others
// out. It must outlast a router call; if the claiming server dies mid-call, a
// reconcile pass after the lease picks the request up.
const submitLease = 10 * time.Minute

// maxSubmitAttempts is how many claimed submissions may fail before the request
// is marked failed for an admin to retry. With submitBackoff that is about six
// hours of retries, enough to ride out a restarting or briefly offline service.
const maxSubmitAttempts = 10

// submitBackoff is the wait after the given number of failed attempts: 5
// minutes doubling to a one-hour cap. The reconcile pass runs every 5 minutes,
// so shorter waits would not be honored anyway.
func submitBackoff(attempts int) time.Duration {
	d := 5 * time.Minute
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// submitAfterCommit submits a request whose approval is already committed. The
// approval stands whatever happens next, so a submission error is logged and
// the committed request returned: answering an error would tell the caller the
// approval failed, and the reconcile pass retries the submission anyway.
func (s *Service) submitAfterCommit(ctx context.Context, req Request, actor Viewer) *Request {
	submitted, err := s.submitApprovedRequest(ctx, req, actor, nil)
	if err != nil {
		slog.WarnContext(ctx, "requests: submission after approval failed; reconcile will retry", "component", "requests",
			"request_id", req.ID, "err", err)
		return &req
	}
	return submitted
}

// submitApprovedRequest sends an approved request to the router plugin. Only
// the caller that claims the submission sends it, so concurrent approvals and
// reconcile passes on any server cannot double-submit. A failed attempt is
// recorded on the request and retried with backoff until maxSubmitAttempts,
// after which the request is marked failed.
func (s *Service) submitApprovedRequest(ctx context.Context, req Request, actor Viewer, fc *fulfillContext) (*Request, error) {
	if req.Outcome != OutcomeActive || req.Status != StatusApproved {
		return &req, nil
	}
	if fc == nil {
		built, err := s.newFulfillContext(ctx)
		if err != nil {
			return nil, err
		}
		fc = built
	}
	if s.router == nil || !routerConfiguredFor(fc, req.MediaType) {
		// No router serves this media type: the request stays approved and
		// the reconcile pass completes it when the title reaches the library.
		return &req, nil
	}
	missingSeasons := false
	if req.MediaType == MediaTypeSeries && len(req.Seasons) > 0 {
		// A season request for a series already in the library goes only to
		// a server whose plugin takes seasons; any other would add the whole
		// series (see moreSeasonsRequestable). Otherwise it waits for the
		// library, even when the server was set up after it was made.
		matches, err := s.lookupPresence(ctx, req.MediaType, []PresenceCandidate{requestPresenceCandidate(req)})
		if err != nil {
			return nil, err
		}
		if match := matches[req.TMDBID]; match.Available {
			// Seasons that reached the library since the request was made
			// need no download: the reconcile pass completes the request.
			fulfilled, _, err := s.requestFulfilled(ctx, req, match)
			if err != nil {
				return nil, err
			}
			if fulfilled {
				return &req, nil
			}
			deliverable, err := s.missingSeasonsDeliverable(ctx, fc, req)
			if err != nil {
				return nil, err
			}
			if !deliverable {
				return &req, nil
			}
			missingSeasons = true
		}
	}
	claimed, ok, err := s.store.ClaimSubmission(ctx, req.ID, submitLease)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Another caller holds the claim, or a failed attempt's backoff has
		// not elapsed; a later reconcile pass submits it.
		return &req, nil
	}
	// The claim reloads the row; keep this call's external-ID lookup result.
	claimed.externalIDsResolved = req.externalIDsResolved
	claimed.tvdbLookupFailed = req.tvdbLookupFailed
	submitted, submitErr := s.submitClaimed(ctx, *claimed, actor, fc, missingSeasons)
	if submitErr == nil {
		return submitted, nil
	}
	if claimed.SubmitAttempts >= maxSubmitAttempts {
		return s.markSubmissionFailed(ctx, *claimed, actor, submitErr)
	}
	deferred, err := s.store.DeferSubmission(ctx, claimed.ID, claimLease(*claimed), submitBackoff(claimed.SubmitAttempts), submitErr.Error())
	if err != nil {
		if errors.Is(err, ErrInvalidState) {
			// The attempt created targets before failing, which moved the
			// request past approved and the per-target state now owns it; or
			// this attempt outlived its lease and another claim holds the
			// request now.
			return s.store.GetRequest(ctx, claimed.ID)
		}
		return nil, fmt.Errorf("submit request: %w; schedule retry: %w", submitErr, err)
	}
	slog.WarnContext(ctx, "requests: submission failed; will retry", "component", "requests",
		"request_id", claimed.ID,
		"attempt", claimed.SubmitAttempts,
		"retry_in", submitBackoff(claimed.SubmitAttempts),
		"err", submitErr,
	)
	return deferred, nil
}

// submitClaimed does the submission work for a request whose claim the caller
// holds. missingSeasons marks a request for seasons of a series already in the
// library, which only a router that takes seasons may receive.
func (s *Service) submitClaimed(ctx context.Context, req Request, actor Viewer, fc *fulfillContext, missingSeasons bool) (*Request, error) {
	// Resolve before planSubmission drops failed targets, so a failed save
	// leaves their error records in place.
	if err := s.ensureSeriesTVDBID(ctx, &req); err != nil {
		return nil, err
	}
	if routes := fc.routesFor(req.MediaType); len(routes) > 0 {
		return s.submitRouted(ctx, req, actor, fc, routes, missingSeasons)
	}
	conns, installationID, capabilityID, err := s.resolveRouterConnections(ctx, fc, req.MediaType)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		// A connection is configured for the media type (routerConfiguredFor)
		// but none is usable. That is an admin-fixable setup problem, so it is
		// returned as a submission error: the request keeps its approval and
		// retries with backoff, and goes through once the connection is fixed.
		return nil, errors.New(unusableRouterMessage(fc, req.MediaType))
	}
	if missingSeasons {
		// missingSeasonsDeliverable checked every series connection; this
		// guards the plugin actually chosen.
		ok, err := s.routerSupportsSeasons(ctx, fc, installationID, capabilityID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errMissingSeasonsUnsupported("The request backend")
		}
	}
	allowed, resolved := s.allowedQualities(ctx, req, fc.settings)
	if !fc.settings.ForceDualQuality {
		allowed = filterUnconfiguredOptionalQualities(allowed, conns)
	}
	plan, done, err := s.planSubmission(ctx, req, actor, allowed, resolved && !skippedRouterConnection(fc, req.MediaType))
	if done != nil || err != nil {
		return done, err
	}
	s.populateRequesterIdentity(ctx, &req)
	targets, msg, err := s.router.Fulfill(ctx, installationID, capabilityID, req, plan.want, conns)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		if msg == "" {
			msg = "fulfillment backend created no targets"
		}
		return s.markSubmissionFailed(ctx, req, actor, errors.New(explainSubmissionFailure(req, msg)))
	}
	return s.recordTargets(ctx, req, actor, plan, targets, connectionKindByID(conns), nil, nil)
}

// submitRouted sends each wanted tier to the server the routing rules chose
// for it, one plugin call per tier with only that server, so the plugin
// cannot pick another.
func (s *Service) submitRouted(ctx context.Context, req Request, actor Viewer, fc *fulfillContext, routes []Route, missingSeasons bool) (*Request, error) {
	if err := s.ensureRoutingFacts(ctx, &req, routes); err != nil {
		return nil, err
	}
	allowed, resolved := s.allowedQualities(ctx, req, fc.settings)
	decisions := decideRoutes(routes, req, allowed)
	allowed = slices.DeleteFunc(allowed, func(q Quality) bool {
		decision, routed := decisions[q]
		switch {
		case routed && decision.Skip:
			// A matching route skips the tier: no copy, even with force-dual.
			return true
		case !routed && q == Quality2160p && !fc.settings.ForceDualQuality:
			// No route gives this title a 4K destination: it does not get a
			// 4K copy, the same as when no 4K server is configured.
			return true
		}
		return false
	})
	plan, done, err := s.planSubmission(ctx, req, actor, allowed, resolved)
	if done != nil || err != nil {
		return done, err
	}
	s.populateRequesterIdentity(ctx, &req)
	var targets []RouterTarget
	connKind := map[string]string{}
	failures := map[Quality]string{}
	for _, q := range plan.want {
		decision, ok := decisions[q]
		if !ok {
			failures[q] = unroutedMessage(routes, req.MediaType, q)
			continue
		}
		conn, installationID, capabilityID, err := routedConnection(fc, decision, req.MediaType, q)
		if err != nil {
			if len(targets) == 0 {
				// The chosen server is gone, disabled or not set up: an
				// admin-fixable problem. Nothing reached a server yet, so the
				// submission retries with backoff and goes through once the
				// server or the route is fixed.
				return nil, err
			}
			failures[q] = err.Error()
			continue
		}
		if missingSeasons {
			// Routing facts read after the claim can choose a server
			// missingSeasonsDeliverable did not check.
			ok, err := s.routerSupportsSeasons(ctx, fc, installationID, capabilityID)
			if err == nil && !ok {
				err = errMissingSeasonsUnsupported(fmt.Sprintf("%q (route %q)", integrationName(fc, conn.ID), decision.RouteName))
			}
			if err != nil {
				if len(targets) == 0 {
					return nil, err
				}
				failures[q] = err.Error()
				continue
			}
		}
		maps.Copy(connKind, connectionKindByID([]ResolvedRouterConnection{conn}))
		got, msg, err := s.router.Fulfill(ctx, installationID, capabilityID, req, []Quality{q}, []ResolvedRouterConnection{conn})
		if err != nil {
			if len(targets) == 0 {
				// Nothing reached a server yet: retry the whole submission.
				return nil, err
			}
			failures[q] = err.Error()
			continue
		}
		// The call asked for this tier only. A target labeled with the other
		// tier would sit on this tier's server and could win the other tier's
		// slot in recordTargets over that tier's real target, so drop it.
		var tier []RouterTarget
		for _, t := range got {
			if t.Quality != q {
				slog.WarnContext(ctx, "requests: plugin returned a target for another quality; skipping", "component", "requests",
					"request_id", req.ID, "requested_quality", string(q), "quality", string(t.Quality))
				continue
			}
			// The plugin was handed only this server, so a target it returns
			// without a connection is on it; recording that keeps the target
			// checked through the plugin that owns the server.
			if t.ConnectionID == "" {
				t.ConnectionID = conn.ID
			}
			tier = append(tier, t)
		}
		if len(tier) == 0 && msg != "" {
			failures[q] = msg
		}
		targets = append(targets, tier...)
	}
	return s.recordTargets(ctx, req, actor, plan, targets, connKind, decisions, failures)
}

// unroutedMessage says why a tier went nowhere. Under Standard it can only be
// HD, when the media type's one server is marked 4K.
func unroutedMessage(routes []Route, mediaType MediaType, q Quality) string {
	if isStandardRouting(routes, mediaType) {
		return fmt.Sprintf("no server takes %s %s: the only one is marked 4K", qualityLabel(q), mediaTypePlural(mediaType))
	}
	return "no routing rule sends " + qualityLabel(q) + " for this title"
}

// qualityLabel names a tier in messages.
func qualityLabel(q Quality) string {
	if q == Quality2160p {
		return "4K"
	}
	return "HD"
}

// unratedRecheck is how long a title with no US rating goes before routing
// asks TMDB again.
const unratedRecheck = 24 * time.Hour

// TMDBCertificationsClient reads every country's certifications, for the
// routing rating's fallback to a title's own country.
type TMDBCertificationsClient interface {
	GetCertifications(ctx context.Context, mediaType string, id int) (map[string][]string, error)
}

// routingRatingOf reads a captured request's rating again: its US rating, or
// its own country's when it has none. A client without the per-country read
// answers with the US rating alone.
func (s *Service) routingRatingOf(ctx context.Context, req Request) (string, error) {
	if certs, ok := s.tmdb.(TMDBCertificationsClient); ok {
		all, err := certs.GetCertifications(ctx, string(req.MediaType), req.TMDBID)
		if err != nil {
			return "", err
		}
		us := tmdb.USCertificationFrom(string(req.MediaType), all)
		return routingRating(us, all, req.RoutingFacts.OriginCountries), nil
	}
	one, ok := s.tmdb.(TMDBCertificationClient)
	if !ok {
		return "", errors.New("no certification client")
	}
	return one.GetCertification(ctx, string(req.MediaType), req.TMDBID)
}

// ensureRoutingFacts fetches the routing facts of a request created before
// they were captured, or while TMDB was unreachable, and stores them. Routing
// without them could send a title to the wrong server, so a TMDB failure is a
// submission error and the submission retries. A request captured before its
// rating was is given one, only when an enabled route checks ratings.
func (s *Service) ensureRoutingFacts(ctx context.Context, req *Request, routes []Route) error {
	if req.RoutingFacts.Captured() {
		// A title TMDB had not rated yet (unreleased) is asked again a day
		// later, so a rating route can still match it once it is rated.
		stored := req.RoutingFacts.ContentRating
		known := stored != nil && (*stored != "" || s.now().Sub(*req.RoutingFacts.CapturedAt) < unratedRecheck)
		if known || !routesCheckRating(routes, req.MediaType) {
			return nil
		}
		rating, err := s.routingRatingOf(ctx, *req)
		if err != nil {
			return fmt.Errorf("could not read the title's rating from TMDB to route it: %w", err)
		}
		facts := req.RoutingFacts
		now := s.now()
		facts.ContentRating, facts.CapturedAt = &rating, &now
		updated, err := s.store.SetRoutingFacts(ctx, req.ID, facts)
		if err != nil {
			return err
		}
		req.RoutingFacts = updated.RoutingFacts
		return nil
	}
	detail := s.requestDetail(ctx, req.MediaType, req.TMDBID)
	if detail == nil {
		if isStandardRouting(routes, req.MediaType) {
			// Standard's only condition is anime, which the request's stored
			// anime flag already answers, so it is sent without the facts.
			req.RoutingFacts.Anime = req.IsAnime
			return nil
		}
		if !routesUseConditions(routes, req.MediaType) {
			// Only Everything else decides: the facts would
			// not change where the request goes, so it is sent without them.
			return nil
		}
		return errors.New("could not read the title's details from TMDB to route it")
	}
	updated, err := s.store.SetRoutingFacts(ctx, req.ID, s.routingFacts(ctx, detail))
	if err != nil {
		return err
	}
	req.RoutingFacts, req.IsAnime = updated.RoutingFacts, updated.IsAnime
	return nil
}

// submissionPlan is what a submission still has to send.
type submissionPlan struct {
	// healthy holds the qualities that already have a live or finished target.
	healthy map[Quality]bool
	// want is the qualities to send now.
	want []Quality
}

// planSubmission compares the qualities a request should have with its
// targets. It drops failed targets for qualities no longer wanted (4K turned
// off, the requester lost 4K, the 4K destination removed), which would keep the
// request failed forever, but only when allowed is certain: an entitlement
// lookup error or a skipped connection also shrinks it, and a transient error
// must not discard a failure an admin still needs to see. It also drops the
// failed targets of the qualities it is about to resend. done is set when
// nothing is left to send.
func (s *Service) planSubmission(ctx context.Context, req Request, actor Viewer, allowed []Quality, certain bool) (submissionPlan, *Request, error) {
	existing, err := s.store.ListTargets(ctx, req.ID)
	if err != nil {
		return submissionPlan{}, nil, err
	}
	plan := submissionPlan{healthy: map[Quality]bool{}}
	for _, t := range existing {
		if t.Status != StatusFailed {
			plan.healthy[t.Quality] = true
		}
	}
	if certain {
		for _, t := range existing {
			if t.Status == StatusFailed && !slices.Contains(allowed, t.Quality) {
				if err := s.store.DeleteTarget(ctx, t.ID); err != nil && !errors.Is(err, ErrNotFound) {
					return submissionPlan{}, nil, err
				}
			}
		}
	}
	for _, q := range allowed {
		if !plan.healthy[q] {
			plan.want = append(plan.want, q)
		}
	}
	if len(plan.want) == 0 {
		// Nothing left to send: let the remaining targets decide the status so
		// the request does not sit in approved.
		updated, err := s.store.RecomputeStatus(ctx, req.ID, actor)
		if errors.Is(err, ErrInvalidState) {
			updated, err = s.store.GetRequest(ctx, req.ID)
		}
		if err != nil {
			return submissionPlan{}, nil, err
		}
		return submissionPlan{}, updated, nil
	}
	for _, t := range existing {
		if t.Status == StatusFailed && slices.Contains(plan.want, t.Quality) {
			if err := s.store.DeleteTarget(ctx, t.ID); err != nil {
				return submissionPlan{}, nil, err
			}
		}
	}
	return plan, nil, nil
}

// msgNoTargetForQuality is recorded on a wanted quality the plugin returned no
// target for.
const msgNoTargetForQuality = "fulfillment backend returned no target for this quality"

// recordTargets stores what the plugin returned for a submission. The plugin
// is an out-of-process trust boundary: every returned target is validated
// against the DB CHECK constraints (quality, status), and a quality duplicated
// in the batch or already holding a healthy target is skipped, so a
// misbehaving plugin can't violate UNIQUE(request_id, quality) and wedge the
// request. Any wanted quality left without a target is recorded as a failed
// target rather than silently dropped, so it stays visible and Retry
// re-attempts it; failures carries the reason when one is known. decisions,
// when routing chose the servers, stamps each target with its route.
func (s *Service) recordTargets(ctx context.Context, req Request, actor Viewer, plan submissionPlan, targets []RouterTarget,
	connKind map[string]string, decisions map[Quality]RouteDecision, failures map[Quality]string) (*Request, error) {
	validQuality := map[Quality]bool{Quality1080p: true, Quality2160p: true}
	validStatus := map[Status]bool{StatusQueued: true, StatusDownloading: true, StatusCompleted: true, StatusFailed: true}
	returned := map[Quality]bool{}
	var record []Target
	for _, rt := range targets {
		if !validQuality[rt.Quality] {
			slog.WarnContext(ctx, "requests: plugin returned unknown quality; skipping", "component", "requests", "request_id", req.ID, "quality", string(rt.Quality))
			continue
		}
		if returned[rt.Quality] || plan.healthy[rt.Quality] {
			continue // dup-in-batch, or a healthy target already exists for this quality
		}
		if rt.ConnectionID != "" {
			if _, ok := connKind[rt.ConnectionID]; !ok {
				slog.WarnContext(ctx, "requests: plugin returned unknown connection id; skipping target", "component", "requests", "request_id", req.ID, "connection_id", rt.ConnectionID)
				continue
			}
		}
		returned[rt.Quality] = true
		decision := decisions[rt.Quality]
		status := rt.Status
		if status == "" || !validStatus[status] {
			status = StatusQueued // coerce unknown/empty status to the DB-valid default
		}
		message := rt.Message
		if status == StatusFailed {
			message = explainSubmissionFailure(req, message)
		}
		record = append(record, Target{
			IntegrationID: rt.ConnectionID, IntegrationKind: connKind[rt.ConnectionID],
			Quality: rt.Quality, IsAnime: req.IsAnime, Status: status,
			ExternalID: rt.ExternalID, ExternalStatus: rt.ExternalStatus, LastError: message,
			RouteID: decision.RouteID, RouteName: decision.RouteName,
		})
	}
	for _, q := range plan.want {
		if returned[q] {
			continue
		}
		msg := explainSubmissionFailure(req, failures[q])
		if msg == "" {
			msg = msgNoTargetForQuality
		}
		decision := decisions[q]
		record = append(record, Target{
			IntegrationID: decision.IntegrationID, Quality: q, IsAnime: req.IsAnime,
			Status: StatusFailed, LastError: msg, RouteID: decision.RouteID, RouteName: decision.RouteName,
		})
	}
	recorded, err := s.store.RecordSubmission(ctx, req.ID, claimLease(req), record, actor)
	if errors.Is(err, ErrInvalidState) {
		// The router call outlived this claim's lease, and meanwhile the
		// request was withdrawn, completed from the library, or claimed
		// again. That state stands; the downstream service may still hold
		// what this call added.
		slog.WarnContext(ctx, "requests: submission outlived its claim; result dropped", "component", "requests",
			"request_id", req.ID, "targets", len(record))
		return s.store.GetRequest(ctx, req.ID)
	}
	return recorded, err
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

// claimLease returns the lease a claimed request holds, which fences the
// writes that end the claim.
func claimLease(claimed Request) time.Time {
	if claimed.SubmitLeaseUntil == nil {
		return time.Time{}
	}
	return *claimed.SubmitLeaseUntil
}

// markSubmissionFailed ends a claimed submission as failed. claimed is the
// request as ClaimSubmission returned it; its lease fences the write.
func (s *Service) markSubmissionFailed(ctx context.Context, claimed Request, actor Viewer, submitErr error) (*Request, error) {
	failed, err := s.store.FailSubmission(ctx, claimed.ID, claimLease(claimed), actor, submitErr.Error())
	if err != nil {
		if errors.Is(err, ErrInvalidState) {
			// The attempt created targets before failing, or it outlived its
			// lease and another claim holds the request now.
			return s.store.GetRequest(ctx, claimed.ID)
		}
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
	reconcileDeferred    reconcileChange = "deferred"
)

// completeWaitingFromLibrary completes pending and recently failed requests
// whose title has reached the library. Nothing is in flight for them, so they
// run in their own rotation, apart from the requests that need router calls,
// and one batched presence lookup per media type covers the whole batch.
func (s *Service) completeWaitingFromLibrary(ctx context.Context, limit int, result *ReconcileResult) error {
	waiting, err := s.store.ListLibraryWaitCandidates(ctx, limit)
	if err != nil {
		return err
	}
	present, err := s.presentRequests(ctx, waiting)
	if err != nil {
		return err
	}
	result.Checked += len(waiting)
	for _, req := range waiting {
		if err := ctx.Err(); err != nil {
			return err
		}
		if present[req.ID] {
			if _, err := s.store.MarkAvailable(ctx, req.ID, Viewer{}); err == nil {
				result.Completed++
			} else if !errors.Is(err, ErrInvalidState) {
				slog.WarnContext(ctx, "request library completion failed", "component", "requests", "request_id", req.ID, "err", err)
				result.Errors++
			}
		}
		if err := s.store.MarkReconciled(ctx, req.ID); err != nil {
			slog.WarnContext(ctx, "request reconcile stamp failed", "component", "requests", "request_id", req.ID, "err", err)
		}
	}
	return nil
}

// presentRequests reports which requests are fulfilled by the library, with
// one presence lookup per media type and one season lookup for every season
// request: the title is in, or, for a season request, every requested season
// is complete.
func (s *Service) presentRequests(ctx context.Context, reqs []*Request) (map[string]bool, error) {
	byType := map[MediaType][]*Request{}
	for _, req := range reqs {
		if req != nil && req.TMDBID > 0 {
			byType[req.MediaType] = append(byType[req.MediaType], req)
		}
	}
	out := make(map[string]bool, len(reqs))
	// Season requests whose series is in the library, by series content ID.
	seasonRequests := map[string][]*Request{}
	for mediaType, group := range byType {
		candidates := make([]PresenceCandidate, 0, len(group))
		for _, req := range group {
			candidates = append(candidates, requestPresenceCandidate(*req))
		}
		matches, err := s.lookupPresence(ctx, mediaType, candidates)
		if err != nil {
			return nil, err
		}
		for _, req := range group {
			match := matches[req.TMDBID]
			if req.MediaType != MediaTypeSeries || len(req.Seasons) == 0 {
				out[req.ID] = match.Available
				continue
			}
			out[req.ID] = false
			if match.Available && match.ContentID != "" {
				seasonRequests[match.ContentID] = append(seasonRequests[match.ContentID], req)
			}
		}
	}
	resolver, ok := s.presence.(SeasonPresenceResolver)
	if !ok || len(seasonRequests) == 0 {
		return out, nil
	}
	bySeries, err := resolver.SeasonAvailability(ctx, slices.Collect(maps.Keys(seasonRequests)))
	if err != nil {
		return nil, err
	}
	for series, group := range seasonRequests {
		for _, req := range group {
			progress := seasonProgress(req.Seasons, bySeries[series])
			out[req.ID] = seasonsDelivered(progress, req.Status == StatusCompleted)
		}
	}
	return out, nil
}

// reconcileRequest moves one in-flight request forward. present reports
// whether its title is already in the library.
func (s *Service) reconcileRequest(ctx context.Context, req Request, fc *fulfillContext, present bool) (reconcileChange, error) {
	if present {
		// The presence check is quality-agnostic (TMDB id only), so it must not
		// force-complete a request whose targets are still in flight — that would
		// orphan in-progress downloads. Only take the shortcut for requests with
		// no live target (pending, failed, waiting for the library without a
		// router, or legacy); otherwise let per-target reconcile + aggregate drive
		// completion.
		live, err := s.liveTargets(ctx, req.ID)
		if err != nil {
			return reconcileUnchanged, err
		}
		if len(live) == 0 {
			if req.Status == StatusCompleted {
				return reconcileUnchanged, nil
			}
			if _, err := s.store.MarkAvailable(ctx, req.ID, Viewer{}); err != nil {
				if errors.Is(err, ErrInvalidState) {
					// Another actor moved it first, or a submission holds the
					// claim; a later pass completes it.
					return reconcileUnchanged, nil
				}
				return reconcileUnchanged, err
			}
			return reconcileCompleted, nil
		}
		updated, retired, err := s.retireStalledTargets(ctx, req, live)
		if err != nil {
			return reconcileUnchanged, err
		}
		if retired && updated != nil && updated.Status == StatusCompleted {
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
		case updated.Status == StatusApproved && updated.SubmitAttempts > req.SubmitAttempts:
			// This pass made an attempt and it failed; a request still in
			// backoff comes back unchanged and counts as skipped.
			return reconcileDeferred, nil
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
	statuses, checkErr := s.checkTargetStatuses(ctx, req, targets, fc)
	change, err := s.applyTargetStatuses(ctx, targets, statuses)
	if err != nil {
		return reconcileUnchanged, err
	}
	return change, checkErr
}

// applyTargetStatuses writes the statuses a router reported for the given
// targets, matched by quality and connection, and the download progress of
// each target still queued or downloading afterwards. A target that reports
// no progress has any it had cleared; one that had none is not written, so an
// idle target costs no write per pass. A target with progress that got no
// status back (its server was skipped or failed, or the call did) keeps it
// until it goes stale; see settleUnansweredDownload. Both reconcile and the
// download refresh pass apply statuses here.
func (s *Service) applyTargetStatuses(ctx context.Context, targets []Target, statuses []RouterTargetStatus) (reconcileChange, error) {
	change := reconcileUnchanged
	answered := make([]bool, len(targets))
	for _, st := range statuses {
		// Match the returned status to the live target by (quality, connection).
		var target *Target
		for i := range targets {
			if targets[i].Quality == st.Quality && targets[i].IntegrationID == st.ConnectionID {
				target = &targets[i]
				answered[i] = true
				break
			}
		}
		if target == nil || target.Status == StatusCompleted || target.Status == StatusFailed {
			continue
		}
		status := target.Status
		if newStatus := st.Status; newStatus != "" && newStatus != target.Status {
			if _, err := s.store.UpdateTargetStatus(ctx, target.ID, newStatus, "", st.ExternalStatus, st.Message, Viewer{}); err != nil {
				return reconcileUnchanged, err
			}
			status = newStatus
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
		} else if st.ExternalStatus != "" && st.ExternalStatus != target.ExternalStatus {
			// The server's own state moved without changing the target's
			// status (say, a download went from importing to stalled). Keep
			// the raw status in step with the progress shown beside it.
			if err := s.store.UpdateTargetExternalStatus(ctx, target.ID, st.ExternalStatus); err != nil {
				return reconcileUnchanged, err
			}
		}
		if (status == StatusQueued || status == StatusDownloading) && (st.Progress != nil || target.Download != nil) {
			if err := s.store.UpdateTargetDownload(ctx, target.ID, st.Progress); err != nil {
				return reconcileUnchanged, err
			}
		}
	}
	for i, target := range targets {
		if answered[i] {
			continue
		}
		if err := s.settleUnansweredDownload(ctx, target); err != nil {
			return reconcileUnchanged, err
		}
	}
	return change, nil
}

// checkTargetStatuses asks each live target's plugin for its status. Targets
// are grouped by the installation and capability that own their server, so a
// target sent through one plugin is never checked through another (routing
// can send a request's tiers through different plugins, and an admin can
// rebind a server). A target the plugin returned without a connection is
// checked through the plugin that routes the media type without rules, with
// all its connections, as before routing. A target whose server is gone,
// disabled or unusable is skipped; the library presence check retires it if
// the media arrives.
func (s *Service) checkTargetStatuses(ctx context.Context, req Request, targets []Target, fc *fulfillContext) ([]RouterTargetStatus, error) {
	type owner struct {
		installationID int
		capabilityID   string
	}
	type group struct {
		refs  []RouterTargetRef
		conns []ResolvedRouterConnection
		seen  map[string]bool
	}
	groups := map[owner]*group{}
	var order []owner
	groupFor := func(key owner) *group {
		g := groups[key]
		if g == nil {
			g = &group{seen: map[string]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		return g
	}
	addConn := func(g *group, conn ResolvedRouterConnection) {
		if !g.seen[conn.ID] {
			g.seen[conn.ID] = true
			g.conns = append(g.conns, conn)
		}
	}
	for _, t := range targets {
		if t.Status != StatusQueued && t.Status != StatusDownloading {
			continue
		}
		ref := RouterTargetRef{Quality: t.Quality, ConnectionID: t.IntegrationID, ExternalID: t.ExternalID}
		if t.IntegrationID == "" {
			conns, installationID, capabilityID, err := s.resolveRouterConnections(ctx, fc, req.MediaType)
			if err != nil || len(conns) == 0 {
				continue
			}
			g := groupFor(owner{installationID, capabilityID})
			g.refs = append(g.refs, ref)
			for _, conn := range conns {
				addConn(g, conn)
			}
			continue
		}
		in := integrationByID(fc, t.IntegrationID)
		if !statusCheckable(in) {
			continue
		}
		g := groupFor(owner{*in.InstallationID, in.CapabilityID})
		g.refs = append(g.refs, ref)
		addConn(g, ResolvedRouterConnection{ID: in.ID, BaseURL: in.BaseURL, APIKey: strings.TrimSpace(in.APIKeyRef), Config: in.PluginConfig})
	}
	// One plugin being down must not hide the statuses another reported, so
	// every group is asked and the errors are returned alongside them.
	var out []RouterTargetStatus
	var errs []error
	for _, key := range order {
		g := groups[key]
		statuses, err := s.router.CheckStatus(ctx, key.installationID, key.capabilityID, req, g.refs, g.conns)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		keepProgress, err := s.keepsReportedProgress(ctx, fc, key.installationID, key.capabilityID, statuses)
		if err != nil {
			errs = append(errs, err)
		}
		// A plugin that omits connection_id from its statuses omits it from
		// its targets too, and a routed target was recorded on its server
		// anyway. When every target in the group is on one server, a status
		// without a connection is that server's.
		server := soleRefConnection(g.refs)
		for _, st := range statuses {
			if st.ConnectionID == "" {
				st.ConnectionID = server
			}
			if !keepProgress {
				st.Progress = nil
			}
			out = append(out, st)
		}
	}
	return out, errors.Join(errs...)
}

// soleRefConnection returns the one connection all refs are on, or "" when
// they are on several or any is on none.
func soleRefConnection(refs []RouterTargetRef) string {
	server := ""
	for _, ref := range refs {
		if ref.ConnectionID == "" || (server != "" && ref.ConnectionID != server) {
			return ""
		}
		server = ref.ConnectionID
	}
	return server
}

// statusCheckable reports whether a target's server can be asked for the
// target's status: it still exists, is enabled, is bound to a router
// capability, and has an API key.
func statusCheckable(in *Integration) bool {
	return in != nil && in.Enabled && in.InstallationID != nil && in.CapabilityID != "" && strings.TrimSpace(in.APIKeyRef) != ""
}

func integrationByID(fc *fulfillContext, id string) *Integration {
	if id == "" {
		return nil
	}
	for i := range fc.integrations {
		if fc.integrations[i].ID == id {
			return &fc.integrations[i]
		}
	}
	return nil
}

// liveTargets returns the request's non-terminal (queued or downloading)
// fulfillment targets.
func (s *Service) liveTargets(ctx context.Context, requestID string) ([]Target, error) {
	targets, err := s.store.ListTargets(ctx, requestID)
	if err != nil {
		return nil, err
	}
	var live []Target
	for _, t := range targets {
		if t.Status == StatusQueued || t.Status == StatusDownloading {
			live = append(live, t)
		}
	}
	return live, nil
}

// stalledTargetHorizon is how long a queued target may go without a single
// status transition before presence-confirmed media is allowed to retire it.
// Reconciliation runs on a schedule measured in minutes and only writes on a
// real status change, so a target older than this has had many chances to move
// and has not.
const stalledTargetHorizon = 24 * time.Hour

// ExternalStatusPresenceConfirmed marks a target closed out by the presence
// backstop rather than by a router-reported completion. Exported so clients can
// distinguish "the arr said it finished" from "we found the media ourselves".
const ExternalStatusPresenceConfirmed = "presence_confirmed"

// retireStalledTargets is the backstop for a router that never reports
// completion. The presence shortcut in reconcileRequest stays disabled for as
// long as any target looks live, so a router that reports "queued" forever — a
// buggy plugin, a connection pointing at an instance that no longer tracks the
// item — pins the request open permanently even though the media is sitting in
// the library.
//
// Targets that are actively downloading are never retired: those are exactly
// the in-flight downloads the quality-agnostic presence check must not orphan.
// Only targets stuck in queued past the horizon are closed out, and the request
// status follows from the usual target aggregate. It returns the request as of
// the last retirement, if any.
func (s *Service) retireStalledTargets(ctx context.Context, req Request, live []Target) (*Request, bool, error) {
	cutoff := s.now().Add(-stalledTargetHorizon)
	var updated *Request
	retired := false
	for _, t := range live {
		if t.Status != StatusQueued || t.UpdatedAt.After(cutoff) {
			continue
		}
		next, err := s.store.UpdateTargetStatus(ctx, t.ID, StatusCompleted, "", ExternalStatusPresenceConfirmed, "", Viewer{})
		if err != nil {
			return updated, retired, err
		}
		updated, retired = next, true
		slog.WarnContext(ctx, "requests: retired stalled target on presence", "component", "requests",
			"request_id", req.ID,
			"target_id", t.ID,
			"media_type", req.MediaType,
			"tmdb_id", req.TMDBID,
			"quality", t.Quality,
			"integration_kind", t.IntegrationKind,
			"external_id", t.ExternalID,
			"external_status", t.ExternalStatus,
			"target_updated_at", t.UpdatedAt,
		)
	}
	return updated, retired, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func requestStateFor(viewer Viewer, policy EffectivePolicy, available bool, req *Request) RequestState {
	if req != nil {
		return activeRequestState(viewer, req)
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

// activeRequestState is the state of a title that already has an active
// request: not requestable, and the request is visible to its account and to
// admins.
func activeRequestState(viewer Viewer, req *Request) RequestState {
	state := RequestState{
		Status:      req.Status,
		Requestable: false,
		Reason:      "already_requested",
	}
	if viewer.IsAdmin || req.RequestedByUserID == viewer.UserID {
		state.RequestID = req.ID
	}
	state.State = req.State()
	state.RequestedByViewer = req.requestedBy(viewer)
	return state
}

// validateCreateAccess applies the policy rules a create decides up front. The
// quota is not one of them: the store checks it under the requester's lock, so
// concurrent creates cannot both take the last slot.
func validateCreateAccess(policy EffectivePolicy) error {
	switch {
	case !policy.RequestsEnabled:
		return ErrRequestsDisabled
	case policy.Blocked:
		return ErrUserBlocked
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
	if input.TVDBID != nil && *input.TVDBID <= 0 {
		input.TVDBID = nil
	}
	if input.TMDBID <= 0 {
		return CreateRequestInput{}, fmt.Errorf("%w: tmdb_id is required", ErrInvalidInput)
	}
	if input.Title == "" {
		return CreateRequestInput{}, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	if len(input.Seasons) > 0 && mediaType != MediaTypeSeries {
		return CreateRequestInput{}, fmt.Errorf("%w: only a series request names seasons", ErrInvalidInput)
	}
	for _, season := range input.Seasons {
		if season <= 0 {
			// Refused, never dropped: an emptied list would mean every
			// missing season.
			return CreateRequestInput{}, &ValidationError{FieldErrors: map[string]string{"seasons": "Season numbers start at 1."}}
		}
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

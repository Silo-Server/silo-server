package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// Route administration. Every media type has one fallback route, with the
// fixed ID FallbackRouteID(mediaType): saving it creates it, it takes no
// conditions, and it cannot be deleted. The other routes are ordered rules
// that must narrow (at least one condition) and must do something (a
// destination, or skip 4K).

// maxRoutesPerMediaType bounds the rules one media type can have, keeping the
// route list a small bounded collection.
const maxRoutesPerMediaType = 100

// FallbackRouteID is the ID of a media type's fallback route.
func FallbackRouteID(mediaType MediaType) string { return "fallback-" + string(mediaType) }

// The field names of a route's HD and 4K destinations, as the API and its
// field errors name them.
const (
	fieldHD  = "hd"
	fieldUHD = "uhd"
)

// routingOwnedConfigKeys are the plugin config keys routing sets itself; a
// route cannot override them.
var routingOwnedConfigKeys = []string{
	configServiceKind, configIsDefault, configIsDefault4K, configIs4K,
	configAnimeEnabled, "anime_root_folder", "anime_quality_profile_id", "anime_tags",
}

var (
	languageCode = regexp.MustCompile(`^[a-z]{2,3}$`)
	countryCode  = regexp.MustCompile(`^[A-Z]{2}$`)
)

// RoutePreview is how the routes would send one title right now.
type RoutePreview struct {
	Facts RoutingFacts
	Tiers []RoutePreviewTier
	// Rules explains the decision: every route of the media type in
	// evaluation order, the conditions it failed, and what it did per tier.
	Rules []RouteTrace
}

// RoutePreviewTier is one quality tier's outcome. RouteID is empty when no
// route sends the tier anywhere; Reason then says why.
type RoutePreviewTier struct {
	Quality         Quality
	RouteID         string
	RouteName       string
	IntegrationID   string
	IntegrationName string
	Overrides       map[string]any
	Reason          string
}

func (s *Service) routeStore() (RouteStore, error) {
	store, ok := s.store.(RouteStore)
	if !ok {
		return nil, fmt.Errorf("request store does not support route administration")
	}
	return store, nil
}

// RouteStore is implemented by the PostgreSQL repository.
type RouteStore interface {
	GetRoute(ctx context.Context, id string) (*Route, error)
	SaveRouteConditional(ctx context.Context, route Route, expected int64) (*Route, error)
	DeleteRouteConditional(ctx context.Context, id string, expected int64) error
	ReorderRoutes(ctx context.Context, mediaType MediaType, ids []string) error
}

// unsavedFallback is a media type's fallback before its first save: revision
// zero, no destinations. Administration shows it so it can be edited; routing
// never sees it.
func unsavedFallback(mediaType MediaType) Route {
	return Route{ID: FallbackRouteID(mediaType), MediaType: mediaType, Position: 1000, Name: fallbackRouteName, Enabled: true, IsFallback: true}
}

// ListRoutesAdmin returns every route, in evaluation order per media type,
// with each media type's fallback (unsaved if it never was).
func (s *Service) ListRoutesAdmin(ctx context.Context, viewer Viewer) ([]Route, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Route
	for _, mediaType := range []MediaType{MediaTypeMovie, MediaTypeSeries} {
		var ofType []Route
		hasFallback := false
		for _, route := range routes {
			if route.MediaType == mediaType {
				ofType = append(ofType, route)
				hasFallback = hasFallback || route.IsFallback
			}
		}
		if !hasFallback {
			ofType = append(ofType, unsavedFallback(mediaType))
		}
		out = append(out, orderRoutes(ofType)...)
	}
	return out, nil
}

// GetRoute returns one route; a fallback that was never saved comes back
// unsaved (revision zero).
func (s *Service) GetRoute(ctx context.Context, viewer Viewer, id string) (*Route, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.routeStore()
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	route, err := store.GetRoute(ctx, id)
	if errors.Is(err, ErrNotFound) {
		for _, mediaType := range []MediaType{MediaTypeMovie, MediaTypeSeries} {
			if id == FallbackRouteID(mediaType) {
				fallback := unsavedFallback(mediaType)
				return &fallback, nil
			}
		}
	}
	return route, err
}

// CreateRoute adds a rule after the media type's existing rules.
func (s *Service) CreateRoute(ctx context.Context, viewer Viewer, route Route) (*Route, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.routeStore()
	if err != nil {
		return nil, err
	}
	id, err := idgen.NextID()
	if err != nil {
		return nil, err
	}
	route.ID = id
	route.IsFallback = false
	if err := s.validateRoute(ctx, &route); err != nil {
		return nil, err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	// The first rule switches the media type from the plugin's routing to
	// Silo's, where a title no rule matches goes to the fallback. Without a
	// fallback HD server those titles would have nowhere to go.
	fallbackReady := false
	rules := 0
	for _, existing := range routes {
		if existing.MediaType != route.MediaType {
			continue
		}
		if existing.IsFallback {
			fallbackReady = existing.HD.IntegrationID != ""
			continue
		}
		rules++
		if existing.Position >= route.Position {
			route.Position = existing.Position + 1
		}
	}
	if rules >= maxRoutesPerMediaType {
		return nil, &ValidationError{FormError: fmt.Sprintf("A media type can have at most %d rules.", maxRoutesPerMediaType)}
	}
	if !fallbackReady {
		return nil, &ValidationError{FormError: "Choose where everything else goes before adding rules."}
	}
	return store.SaveRouteConditional(ctx, route, 0)
}

// UpdateRouteConditional replaces a route. The fallback route is created on
// its first save (expected 0). Position is kept; ReorderRoutes changes it.
func (s *Service) UpdateRouteConditional(ctx context.Context, viewer Viewer, route Route, expected int64) (*Route, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.routeStore()
	if err != nil {
		return nil, err
	}
	route.IsFallback = route.ID == FallbackRouteID(MediaTypeMovie) || route.ID == FallbackRouteID(MediaTypeSeries)
	current, err := store.GetRoute(ctx, route.ID)
	switch {
	case errors.Is(err, ErrNotFound) && route.IsFallback:
		route.MediaType = MediaType(strings.TrimPrefix(route.ID, "fallback-"))
		route.Position = 1000
	case err != nil:
		return nil, err
	default:
		route.MediaType = current.MediaType
		route.Position = current.Position
	}
	if err := s.validateRoute(ctx, &route); err != nil {
		return nil, err
	}
	return store.SaveRouteConditional(ctx, route, expected)
}

func (s *Service) DeleteRouteConditional(ctx context.Context, viewer Viewer, id string, expected int64) error {
	if !viewer.IsAdmin {
		return ErrForbidden
	}
	store, err := s.routeStore()
	if err != nil {
		return err
	}
	current, err := store.GetRoute(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if current.IsFallback {
		return &ValidationError{FormError: "Everything else can't be deleted; change where it sends requests instead."}
	}
	return store.DeleteRouteConditional(ctx, current.ID, expected)
}

// ReorderRoutes sets the evaluation order of a media type's rules and returns
// its routes in the new order. ids must list every rule of the media type
// exactly once; the fallback always stays last and is not listed.
func (s *Service) ReorderRoutes(ctx context.Context, viewer Viewer, mediaType MediaType, ids []string) ([]Route, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.routeStore()
	if err != nil {
		return nil, err
	}
	mediaType, err = normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	var want []string
	for _, route := range routes {
		if route.MediaType == mediaType && !route.IsFallback {
			want = append(want, route.ID)
		}
	}
	got := slices.Clone(ids)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		return nil, &ValidationError{FormError: "The order must list every rule for this media type exactly once; reload and try again."}
	}
	if err := store.ReorderRoutes(ctx, mediaType, ids); err != nil {
		return nil, err
	}
	all, err := s.ListRoutesAdmin(ctx, viewer)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(r Route) bool { return r.MediaType != mediaType }), nil
}

// PreviewRoute shows how the routes would send a title for a requester now,
// from TMDB's current facts. Both tiers are shown regardless of the
// requester's 4K entitlement.
func (s *Service) PreviewRoute(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID, requesterUserID int) (*RoutePreview, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	if tmdbID <= 0 {
		return nil, fmt.Errorf("%w: tmdb id is required", ErrInvalidInput)
	}
	if s.tmdb == nil {
		return nil, ErrIntegrationUnreachable
	}
	detail, err := s.tmdb.GetMediaDetail(ctx, tmdbMediaType(mediaType), tmdbID)
	switch {
	case errors.Is(err, tmdb.ErrNotFound) || (err == nil && detail == nil):
		return nil, ErrNotFound
	case err != nil:
		// TMDB is down or refusing: a dependency failure, worth retrying.
		return nil, fmt.Errorf("%w: %w", ErrIntegrationUnreachable, err)
	}
	fc, err := s.newFulfillContext(ctx)
	if err != nil {
		return nil, err
	}
	req := Request{MediaType: mediaType, TMDBID: tmdbID, RequestedByUserID: requesterUserID, RoutingFacts: s.routingFacts(ctx, detail)}
	routes := fc.routesFor(mediaType)
	qualities := []Quality{Quality1080p, Quality2160p}
	decisions, traces := traceRoutes(routes, req, qualities)
	preview := &RoutePreview{Facts: req.RoutingFacts, Rules: traces}
	for _, q := range qualities {
		tier := RoutePreviewTier{Quality: q}
		decision, ok := decisions[q]
		switch {
		case len(routes) == 0:
			tier.Reason = "No routes are set up for this media type, so the request plugin picks the server."
		case !ok && isStandardRouting(routes, mediaType):
			tier.Reason = fmt.Sprintf("No server takes %s %s: the only one is marked 4K.", qualityLabel(q), mediaTypePlural(mediaType))
		case !ok:
			tier.Reason = "No rule sends " + qualityLabel(q) + " for this title."
		case decision.Skip && q == Quality1080p:
			tier.RouteID, tier.RouteName = decision.RouteID, decision.RouteName
			tier.Reason = decision.RouteName + " sends no HD version"
			if isStandardRouting(routes, mediaType) {
				tier.Reason = "The only server is marked 4K, so there is no HD version"
			}
			// HD is skipped only because 4K goes out; a requester without 4K
			// gets neither unless every request also asks for 4K.
			if fc.settings.ForceDualQuality {
				tier.Reason += "."
			} else {
				tier.Reason += "; requests from users without 4K fail."
			}
		case decision.Skip && isStandardRouting(routes, mediaType):
			tier.RouteID, tier.RouteName = decision.RouteID, decision.RouteName
			tier.Reason = "No server is marked 4K, so there is no 4K version."
		case decision.Skip:
			tier.RouteID, tier.RouteName = decision.RouteID, decision.RouteName
			tier.Reason = decision.RouteName + " sends no 4K version."
		default:
			tier.RouteID, tier.RouteName = decision.RouteID, decision.RouteName
			tier.IntegrationID, tier.Overrides = decision.IntegrationID, decision.Overrides
			in := integrationByID(fc, decision.IntegrationID)
			if in != nil {
				tier.IntegrationName = in.Name
			}
			tier.Reason = routedServerProblem(in, mediaType)
		}
		preview.Tiers = append(preview.Tiers, tier)
	}
	return preview, nil
}

// routedServerProblem explains why a tier routed to the server would fail
// when sent, in the cases routedConnection refuses it, and is "" when the
// server can take it.
func routedServerProblem(in *Integration, mediaType MediaType) string {
	const fails = ", so this tier would fail."
	switch {
	case in == nil:
		return "The server this rule sends to no longer exists" + fails
	case !in.Enabled:
		return in.Name + " is disabled" + fails
	case in.InstallationID == nil || in.CapabilityID == "":
		return in.Name + " is not bound to a plugin installation (re-save it)" + fails
	case strings.TrimSpace(in.APIKeyRef) == "":
		return in.Name + " has no API key" + fails
	case !integrationSupportsMediaType(*in, mediaType):
		return in.Name + " does not take " + mediaTypePlural(mediaType) + fails
	}
	if kind := serverKindMismatch(*in, mediaType); kind != "" {
		return fmt.Sprintf("%s is a %s server%s", in.Name, kind, fails)
	}
	return ""
}

// serverKindMismatch returns a server's type when it cannot take the media
// type (a Sonarr server for movies, a Radarr server for series), and "" when
// it can or does not say.
func serverKindMismatch(in Integration, mediaType MediaType) string {
	kind, _ := in.PluginConfig[configServiceKind].(string)
	want := map[MediaType]string{MediaTypeMovie: kindRadarr, MediaTypeSeries: kindSonarr}[mediaType]
	if kind == "" || want == "" || kind == want {
		return ""
	}
	return kind
}

// ensureRoutesKeepServerKind refuses to switch a server to a type, or to media
// types, the routes sending to it cannot use: their requests would fail when
// sent. Under Advanced it also refuses a 4K switch change that would leave a
// route sending the wrong version to the server. Standard pauses the stored
// routes, which the admin cannot edit there, and places servers by their 4K
// switch itself, so the switch stays free under Standard; turning Advanced on
// clears the destinations that no longer fit (seedAdvancedFromStandard).
func (s *Service) ensureRoutesKeepServerKind(ctx context.Context, in Integration) error {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return err
	}
	checkTier := true
	if modes, ok := s.store.(RoutingModeStore); ok {
		settings, err := modes.GetRoutingSettings(ctx)
		if err != nil {
			return err
		}
		checkTier = settings.Mode != RoutingStandard
	}
	fields := routeKindConflicts(in, routes)
	if checkTier {
		current, err := s.store.GetIntegration(ctx, in.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if current == nil || is4KServer(*current) != is4KServer(in) {
			if msg := tierConflict(in, routes); msg != "" {
				fields["plugin_config."+configIs4K] = msg
			}
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{FieldErrors: fields}
}

// routeKindConflicts explains, by field, why the routes sending to a server
// keep its type and media types where they are: they send it requests it
// would no longer take. It is empty when none do. The repository checks again
// under the server's row lock (ensureRoutesStillFit), since a route save can
// commit between this check and the server save.
func routeKindConflicts(in Integration, routes []Route) map[string]string {
	var wrongKind, unsupported []string
	for _, r := range routes {
		if r.HD.IntegrationID != in.ID && r.UHD.IntegrationID != in.ID {
			continue
		}
		if serverKindMismatch(in, r.MediaType) != "" {
			wrongKind = append(wrongKind, r.Name)
		}
		if !integrationSupportsMediaType(in, r.MediaType) {
			unsupported = append(unsupported, r.Name)
		}
	}
	fields := map[string]string{}
	if len(wrongKind) > 0 {
		fields["plugin_config."+configServiceKind] = "Routing sends requests of the other media type to this server (" +
			strings.Join(wrongKind, ", ") + "); change those routes first."
	}
	if len(unsupported) > 0 {
		fields["supported_media_types"] = "Routing sends a media type this server would no longer take to it (" +
			strings.Join(unsupported, ", ") + "); change those routes first."
	}
	return fields
}

// tierConflict explains why routes keep a server's 4K switch where it is: they
// send it the version the server would no longer take. It is empty when none
// do. It is checked only when the switch changes, so a route saved before the
// rule existed does not block unrelated edits to its server. The repository
// checks again under the routing-mode lock, since a switch to Advanced can
// commit between this check and the save.
func tierConflict(in Integration, routes []Route) string {
	var names []string
	for _, r := range routes {
		if (r.HD.IntegrationID == in.ID && tierMismatch(in, false) != "") ||
			(r.UHD.IntegrationID == in.ID && tierMismatch(in, true) != "") {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	if is4KServer(in) {
		return "Routing sends HD versions to this server (" + strings.Join(names, ", ") +
			`), so it can't be marked 4K; change those routes first.`
	}
	return "Routing sends 4K versions to this server (" + strings.Join(names, ", ") +
		`), so "4K server" has to stay on; change those routes first.`
}

// validateRoute normalizes a route and checks it against the configured
// servers, answering a ValidationError keyed by field.
func (s *Service) validateRoute(ctx context.Context, route *Route) error {
	fields := map[string]string{}
	route.Name = strings.TrimSpace(route.Name)
	if route.IsFallback && route.Name == "" {
		route.Name = fallbackRouteName
	}
	switch {
	case route.Name == "":
		fields["name"] = "Give the rule a name."
	case utf8.RuneCountInString(route.Name) > 100:
		fields["name"] = "Keep the name under 100 characters."
	}
	mediaType, err := normalizeMediaType(route.MediaType)
	if err != nil {
		fields["media_type"] = "Choose movies or series."
	}
	route.MediaType = mediaType
	route.Conditions = normalizeConditions(route.Conditions)
	validateConditions(route.Conditions, fields)

	integrations, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return err
	}
	for field, dest := range map[string]*RouteDestination{fieldHD: &route.HD, fieldUHD: &route.UHD} {
		validateDestination(field, dest, route.MediaType, integrations, fields)
	}
	if route.IsFallback {
		route.Enabled = true
		route.SkipUHD = false
		if !conditionsEmpty(route.Conditions) {
			fields["conditions"] = "Everything else takes every request; it can't have conditions."
		}
		// A saved fallback moves the media type to Silo's routing; without an
		// HD server, every title no rule matches would fail.
		if route.HD.IntegrationID == "" && fields["hd.integration_id"] == "" {
			fields["hd.integration_id"] = "Choose the server that gets everything else."
		}
	} else {
		if conditionsEmpty(route.Conditions) {
			fields["conditions"] = "Add at least one condition. Requests no rule matches go to Everything else."
		}
		if route.HD.IntegrationID == "" && route.UHD.IntegrationID == "" && !route.SkipUHD {
			fields[fieldHD] = "Choose where the HD and 4K versions go, or don't send a 4K version."
		}
		if route.SkipUHD && route.UHD.IntegrationID != "" {
			fields[fieldUHD] = "A rule can't both send 4K versions somewhere and skip them."
		}
	}
	if len(fields) > 0 {
		return &ValidationError{FieldErrors: fields}
	}
	return nil
}

// tierMismatch explains why a Radarr or Sonarr can't take the HD or 4K
// version: 4K versions go only to servers marked 4K, and HD versions only to
// the others. It is empty when the server fits. A server of another plugin
// (Seerr) has no 4K switch of ours and handles both versions itself, so it
// fits either.
func tierMismatch(in Integration, uhd bool) string {
	if selfRouted(in) {
		return ""
	}
	switch marked := is4KServer(in); {
	case uhd && !marked:
		return in.Name + ` isn't marked 4K. Turn on "4K server" in its settings to send 4K versions to it.`
	case !uhd && marked:
		return in.Name + " is marked 4K; it can only take the 4K version."
	}
	return ""
}

// destinationMismatch explains why a server can't take a route's HD or 4K
// version: it is the other type of server, doesn't take the media type, or is
// on the other side of the 4K switch. It is empty when the server fits.
func destinationMismatch(in Integration, mediaType MediaType, uhd bool) string {
	if kind, _ := in.PluginConfig[configServiceKind].(string); kind != "" {
		wantKind := map[MediaType]string{MediaTypeMovie: kindRadarr, MediaTypeSeries: kindSonarr}[mediaType]
		if wantKind != "" && kind != wantKind {
			return fmt.Sprintf("%s is a %s server; %s need %s.", in.Name, kindLabel(kind), mediaTypePlural(mediaType), kindLabel(wantKind))
		}
	}
	if mediaType != "" && !integrationSupportsMediaType(in, mediaType) {
		return fmt.Sprintf("%s does not take %s.", in.Name, mediaTypePlural(mediaType))
	}
	return tierMismatch(in, uhd)
}

func validateDestination(field string, dest *RouteDestination, mediaType MediaType, integrations []Integration, fields map[string]string) {
	dest.IntegrationID = strings.TrimSpace(dest.IntegrationID)
	if dest.IntegrationID == "" {
		dest.Overrides = nil
		return
	}
	var in *Integration
	for i := range integrations {
		if integrations[i].ID == dest.IntegrationID {
			in = &integrations[i]
		}
	}
	if in == nil {
		fields[field+".integration_id"] = "That server no longer exists."
		return
	}
	if msg := destinationMismatch(*in, mediaType, field == fieldUHD); msg != "" {
		fields[field+".integration_id"] = msg
	}
	for _, key := range routingOwnedConfigKeys {
		if _, ok := dest.Overrides[key]; ok {
			fields[field+".overrides."+key] = "Routing sets " + key + " itself."
		}
	}
	if len(dest.Overrides) == 0 {
		dest.Overrides = nil
	}
}

func mediaTypePlural(mediaType MediaType) string {
	if mediaType == MediaTypeSeries {
		return string(MediaTypeSeries)
	}
	return "movies"
}

func normalizeConditions(c RouteConditions) RouteConditions {
	lower := func(values []string) []string {
		var out []string
		for _, v := range values {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	upper := func(values []string) []string {
		var out []string
		for _, v := range values {
			if v = strings.ToUpper(strings.TrimSpace(v)); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	ids := func(values []int) []int {
		out := slices.Clone(values)
		slices.Sort(out)
		return slices.Compact(out)
	}
	c.GenreIDs, c.KeywordIDs = ids(c.GenreIDs), ids(c.KeywordIDs)
	c.NetworkIDs, c.CompanyIDs = ids(c.NetworkIDs), ids(c.CompanyIDs)
	c.RequesterUserIDs = ids(c.RequesterUserIDs)
	c.OriginalLanguages, c.OriginCountries = lower(c.OriginalLanguages), upper(c.OriginCountries)
	c.ExcludeGenreIDs, c.ExcludeKeywordIDs = ids(c.ExcludeGenreIDs), ids(c.ExcludeKeywordIDs)
	c.ExcludeNetworkIDs, c.ExcludeCompanyIDs = ids(c.ExcludeNetworkIDs), ids(c.ExcludeCompanyIDs)
	c.ExcludeRequesterUserIDs = ids(c.ExcludeRequesterUserIDs)
	c.ExcludeOriginalLanguages, c.ExcludeOriginCountries = lower(c.ExcludeOriginalLanguages), upper(c.ExcludeOriginCountries)
	c.MaxContentRating = strings.TrimSpace(c.MaxContentRating)
	return c
}

func validateConditions(c RouteConditions, fields map[string]string) {
	for field, values := range map[string][]int{
		condGenreIDs: c.GenreIDs, condKeywordIDs: c.KeywordIDs, condNetworkIDs: c.NetworkIDs,
		condCompanyIDs: c.CompanyIDs, condRequesterUserIDs: c.RequesterUserIDs,
		condExcludeGenreIDs: c.ExcludeGenreIDs, condExcludeKeywordIDs: c.ExcludeKeywordIDs,
		condExcludeNetworkIDs: c.ExcludeNetworkIDs, condExcludeCompanyIDs: c.ExcludeCompanyIDs,
		condExcludeRequesterUserIDs: c.ExcludeRequesterUserIDs,
	} {
		for _, v := range values {
			if v <= 0 {
				fields["conditions."+field] = "IDs must be positive."
				break
			}
		}
	}
	for field, values := range map[string][]string{condOriginalLanguages: c.OriginalLanguages, condExcludeOriginalLanguages: c.ExcludeOriginalLanguages} {
		for _, v := range values {
			if !languageCode.MatchString(v) {
				fields["conditions."+field] = "Use ISO 639-1 language codes such as ja or en."
				break
			}
		}
	}
	for field, values := range map[string][]string{condOriginCountries: c.OriginCountries, condExcludeOriginCountries: c.ExcludeOriginCountries} {
		for _, v := range values {
			if !countryCode.MatchString(v) {
				fields["conditions."+field] = "Use ISO 3166-1 country codes such as JP or US."
				break
			}
		}
	}
	// A value both wanted and excluded makes the rule match nothing.
	overlaps := slices.ContainsFunc(c.GenreIDs, func(v int) bool { return slices.Contains(c.ExcludeGenreIDs, v) }) ||
		slices.ContainsFunc(c.KeywordIDs, func(v int) bool { return slices.Contains(c.ExcludeKeywordIDs, v) }) ||
		slices.ContainsFunc(c.NetworkIDs, func(v int) bool { return slices.Contains(c.ExcludeNetworkIDs, v) }) ||
		slices.ContainsFunc(c.CompanyIDs, func(v int) bool { return slices.Contains(c.ExcludeCompanyIDs, v) }) ||
		slices.ContainsFunc(c.RequesterUserIDs, func(v int) bool { return slices.Contains(c.ExcludeRequesterUserIDs, v) }) ||
		slices.ContainsFunc(c.OriginalLanguages, func(v string) bool { return slices.Contains(c.ExcludeOriginalLanguages, v) }) ||
		slices.ContainsFunc(c.OriginCountries, func(v string) bool { return slices.Contains(c.ExcludeOriginCountries, v) })
	if overlaps {
		fields["conditions"] = "Remove the values that are in both \u201cis any of\u201d and \u201cis none of\u201d."
	}
	if c.MaxContentRating != "" {
		if _, ok := ratingAge(c.MaxContentRating); !ok {
			fields["conditions."+condMaxContentRating] = "Choose a rating such as G, PG, PG-13 or TV-Y7."
		}
	}
	for field, year := range map[string]int{"year_from": c.YearFrom, "year_to": c.YearTo} {
		if year != 0 && (year < 1870 || year > 2200) {
			fields["conditions."+field] = "Use a year between 1870 and 2200."
		}
	}
	if c.YearFrom != 0 && c.YearTo != 0 && c.YearFrom > c.YearTo {
		fields["conditions.year_to"] = "The end year comes before the start year."
	}
}

func conditionsEmpty(c RouteConditions) bool {
	lists := len(c.GenreIDs) + len(c.KeywordIDs) + len(c.OriginalLanguages) + len(c.OriginCountries) +
		len(c.NetworkIDs) + len(c.CompanyIDs) + len(c.RequesterUserIDs) +
		len(c.ExcludeGenreIDs) + len(c.ExcludeKeywordIDs) + len(c.ExcludeOriginalLanguages) +
		len(c.ExcludeOriginCountries) + len(c.ExcludeNetworkIDs) + len(c.ExcludeCompanyIDs) +
		len(c.ExcludeRequesterUserIDs)
	return c.Anime == nil && lists == 0 && c.YearFrom == 0 && c.YearTo == 0 && c.MaxContentRating == ""
}

// kindLabel names a server kind as admins see it.
func kindLabel(kind string) string {
	switch kind {
	case kindRadarr:
		return "Radarr"
	case kindSonarr:
		return "Sonarr"
	}
	return kind
}

func (r *Repository) GetRoute(ctx context.Context, id string) (*Route, error) {
	route, err := scanRoute(r.pool.QueryRow(ctx, `SELECT `+routeColumns+` FROM request_routes WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &route, nil
}

// SaveRouteConditional inserts or replaces a route. expected is the revision
// the editor read: zero creates, -1 overwrites whatever is there.
func (r *Repository) SaveRouteConditional(ctx context.Context, route Route, expected int64) (*Route, error) {
	conditions, err := json.Marshal(route.Conditions)
	if err != nil {
		return nil, err
	}
	hdOverrides, err := json.Marshal(nonNilOverrides(route.HD.Overrides))
	if err != nil {
		return nil, err
	}
	uhdOverrides, err := json.Marshal(nonNilOverrides(route.UHD.Overrides))
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Only a create (expected 0) or the fallback's first save may find no
	// row; a rule deleted under an editor must not come back.
	allowMissing := expected == 0 || route.IsFallback
	// Servers before the route row, the order a server save that turns
	// Advanced on takes them in.
	if err := ensureDestinationsFit(ctx, tx, route); err != nil {
		return nil, err
	}
	if err := lockRevision(ctx, tx, `SELECT revision FROM request_routes WHERE id = $1 FOR UPDATE`, []any{route.ID}, expected, allowMissing); err != nil {
		return nil, err
	}
	// A missing row cannot be locked, so two first saves of the fallback can
	// both get here; the revision predicate lets only one of them win.
	saved, err := scanRoute(tx.QueryRow(ctx, `
		INSERT INTO request_routes (id, media_type, position, name, enabled, is_fallback, conditions,
			hd_integration_id, hd_overrides, uhd_integration_id, uhd_overrides, skip_uhd)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, nullif($10, ''), $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, enabled = EXCLUDED.enabled, conditions = EXCLUDED.conditions,
			hd_integration_id = EXCLUDED.hd_integration_id, hd_overrides = EXCLUDED.hd_overrides,
			uhd_integration_id = EXCLUDED.uhd_integration_id, uhd_overrides = EXCLUDED.uhd_overrides,
			skip_uhd = EXCLUDED.skip_uhd, updated_at = now()
		WHERE $13::bigint = -1 OR request_routes.revision = $13::bigint
		RETURNING `+routeColumns,
		route.ID, route.MediaType, route.Position, route.Name, route.Enabled, route.IsFallback, conditions,
		route.HD.IntegrationID, hdOverrides, route.UHD.IntegrationID, uhdOverrides, route.SkipUHD, expected))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStaleRevision
	}
	if err != nil {
		return nil, fmt.Errorf("save request route: %w", err)
	}
	return &saved, tx.Commit(ctx)
}

// ensureDestinationsFit checks the route's servers again inside the save,
// holding their rows FOR SHARE: a change to a server's type, media types or 4K
// switch committed since validateRoute is seen here, and one still in flight
// waits for this save and then finds the route (ensureRoutesStillFit).
func ensureDestinationsFit(ctx context.Context, tx pgx.Tx, route Route) error {
	ids := make([]string, 0, 2)
	for _, id := range []string{route.HD.IntegrationID, route.UHD.IntegrationID} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id, name, supported_media_types, plugin_config FROM request_integrations WHERE id = ANY($1) ORDER BY id FOR SHARE`, ids)
	if err != nil {
		return fmt.Errorf("lock route servers: %w", err)
	}
	defer rows.Close()
	fields := map[string]string{}
	for rows.Next() {
		var in Integration
		var raw []byte
		if err := rows.Scan(&in.ID, &in.Name, &in.SupportedMediaTypes, &raw); err != nil {
			return fmt.Errorf("scan route server: %w", err)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in.PluginConfig); err != nil {
				return fmt.Errorf("decode route server %s config: %w", in.ID, err)
			}
		}
		for field, uhd := range map[string]bool{fieldHD: false, fieldUHD: true} {
			dest := route.HD
			if uhd {
				dest = route.UHD
			}
			if dest.IntegrationID == in.ID {
				if msg := destinationMismatch(in, route.MediaType, uhd); msg != "" {
					fields[field+".integration_id"] = msg
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(fields) > 0 {
		return &ValidationError{FieldErrors: fields}
	}
	return nil
}

func nonNilOverrides(overrides map[string]any) map[string]any {
	if overrides == nil {
		return map[string]any{}
	}
	return overrides
}

func (r *Repository) DeleteRouteConditional(ctx context.Context, id string, expected int64) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockRevision(ctx, tx, `SELECT revision FROM request_routes WHERE id = $1 FOR UPDATE`, []any{id}, expected, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM request_routes WHERE id = $1 AND NOT is_fallback`, id); err != nil {
		return fmt.Errorf("delete request route: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) ReorderRoutes(ctx context.Context, mediaType MediaType, ids []string) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE request_routes r SET position = o.position - 1, updated_at = now()
		FROM unnest($2::text[]) WITH ORDINALITY AS o(id, position)
		WHERE r.id = o.id AND r.media_type = $1 AND NOT r.is_fallback
	`, mediaType, ids); err != nil {
		return fmt.Errorf("reorder request routes: %w", err)
	}
	return nil
}

// maxRouteTitleResults bounds the admin title search.
const maxRouteTitleResults = 10

// SearchRouteTitles finds titles to try the routing rules on. It is the
// admins' own search: it works while requests are turned off and applies no
// viewer's rating ceiling.
func (s *Service) SearchRouteTitles(ctx context.Context, viewer Viewer, mediaType MediaType, query string) ([]tmdb.MediaResult, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: search text is required", ErrInvalidInput)
	}
	if s.tmdb == nil {
		return nil, ErrIntegrationUnreachable
	}
	page, err := s.tmdb.SearchMedia(ctx, string(mediaType), query, 1)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIntegrationUnreachable, err)
	}
	out := []tmdb.MediaResult{}
	if page == nil {
		return out, nil
	}
	for _, result := range page.Results {
		if len(out) == maxRouteTitleResults {
			break
		}
		if resultType, err := normalizeMediaType(MediaType(result.MediaType)); err == nil && resultType == mediaType && result.ID > 0 {
			out = append(out, result)
		}
	}
	return out, nil
}

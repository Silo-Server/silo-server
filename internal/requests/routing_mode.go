package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Routing mode: Standard sends each media type to its one server, and its 4K
// copies to its one 4K server, with each server's own settings; the routing
// rules are kept but paused. Advanced routes with the rules. Standard needs at
// most one enabled server of each kind per media type (a normal one and a 4K
// one), both of one service when either is a plugin that routes itself (Seerr),
// so adding or enabling a server that breaks that turns Advanced on.

// RoutingMode is how requests find their server.
type RoutingMode string

const (
	RoutingStandard RoutingMode = "standard"
	RoutingAdvanced RoutingMode = "advanced"
)

// RoutingSettings is the stored routing mode.
type RoutingSettings struct {
	Mode      RoutingMode
	Revision  int64
	UpdatedAt time.Time
}

// StandardDestination is where Standard sends a media type: its one normal
// server and its one 4K server, either of which may be missing.
type StandardDestination struct {
	MediaType        MediaType
	HDIntegrationID  string
	UHDIntegrationID string
}

// RoutingOverview is the routing mode with what Standard would do: where it
// sends each media type, or why it cannot be used.
type RoutingOverview struct {
	RoutingSettings
	Standard []StandardDestination
	// StandardBlocker says why Standard cannot be used; empty when it can.
	StandardBlocker string
}

// RoutingModeStore reads and writes the routing mode. The PostgreSQL
// repository implements it.
type RoutingModeStore interface {
	GetRoutingSettings(ctx context.Context) (RoutingSettings, error)
	// UpdateRoutingModeConditional sets the mode when the revision still
	// matches expected (-1 to overwrite). Standard is refused with a
	// ValidationError when the servers do not allow it.
	UpdateRoutingModeConditional(ctx context.Context, mode RoutingMode, expected int64) (RoutingSettings, error)
}

var standardMediaTypes = []MediaType{MediaTypeMovie, MediaTypeSeries}

// serverServes reports whether a server takes a media type: a Radarr or
// Sonarr by its kind, any other by the media types it lists.
func serverServes(in Integration, mediaType MediaType) bool {
	if kind, _ := in.PluginConfig[configServiceKind].(string); kind != "" {
		return kind == map[MediaType]string{MediaTypeMovie: kindRadarr, MediaTypeSeries: kindSonarr}[mediaType]
	}
	return integrationSupportsMediaType(in, mediaType)
}

// is4KServer reports whether a server is marked as the 4K one.
func is4KServer(in Integration) bool {
	for _, key := range []string{configIs4K, configIsDefault4K} {
		switch flagged := in.PluginConfig[key].(type) {
		case bool:
			if flagged {
				return true
			}
		case string:
			if flagged == "true" {
				return true
			}
		}
	}
	return false
}

// selfRouted reports whether a server is a plugin that picks its own server
// (Seerr) rather than a Radarr or Sonarr.
func selfRouted(in Integration) bool {
	kind, _ := in.PluginConfig[configServiceKind].(string)
	return kind == ""
}

// splitServices reports whether a media type's normal and 4K servers cannot
// both be used by Standard: when either is a plugin that routes itself, the
// plugin is handed the whole request with no rule, so both tiers must be its
// connections. Two Radarrs or Sonarrs are routed tier by tier and may belong
// to different plugins.
func splitServices(hd, uhd Integration) bool {
	if !selfRouted(hd) && !selfRouted(uhd) {
		return false
	}
	return selfRouted(hd) != selfRouted(uhd) ||
		hd.InstallationID == nil || uhd.InstallationID == nil ||
		*hd.InstallationID != *uhd.InstallationID || hd.CapabilityID != uhd.CapabilityID
}

// standardLayout works out where Standard sends each media type from the
// enabled servers, and why Standard cannot be used when a media type has more
// than one normal or more than one 4K server, or its normal and 4K servers are
// different services and one of them picks its own server.
func standardLayout(integrations []Integration) ([]StandardDestination, string) {
	var out []StandardDestination
	var problems []string
	split := false
	for _, mediaType := range standardMediaTypes {
		var hd, uhd []Integration
		for _, in := range integrations {
			if !in.Enabled || !serverServes(in, mediaType) {
				continue
			}
			if is4KServer(in) {
				uhd = append(uhd, in)
			} else {
				hd = append(hd, in)
			}
		}
		noun := mediaTypePlural(mediaType)
		if len(hd) > 1 {
			problems = append(problems, fmt.Sprintf("%s can go to more than one server (%s)", capitalize(noun), serverNames(hd)))
		}
		if len(uhd) > 1 {
			problems = append(problems, fmt.Sprintf("more than one 4K server takes %s (%s)", noun, serverNames(uhd)))
		}
		if len(hd) > 1 || len(uhd) > 1 || len(hd)+len(uhd) == 0 {
			continue
		}
		if len(hd) == 1 && len(uhd) == 1 && splitServices(hd[0], uhd[0]) {
			split = true
			problems = append(problems, fmt.Sprintf("%s go to %s and their 4K versions to %s, which are different request services", noun, hd[0].Name, uhd[0].Name))
			continue
		}
		dest := StandardDestination{MediaType: mediaType}
		if len(hd) == 1 {
			dest.HDIntegrationID = hd[0].ID
		}
		if len(uhd) == 1 {
			dest.UHDIntegrationID = uhd[0].ID
		}
		out = append(out, dest)
	}
	if len(problems) > 0 {
		rule := ". Standard sends each request to one server, plus one server marked 4K"
		if split {
			rule += ", through one request service. Switch to Advanced routing to send them to different services"
		}
		return nil, capitalize(strings.Join(problems, "; ")) + rule + "."
	}
	return out, ""
}

func serverNames(servers []Integration) string {
	names := make([]string, 0, len(servers))
	for _, in := range servers {
		names = append(names, in.Name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// standardRouteID names the route Standard routes a media type with. It is
// not stored; targets record it as the route that sent them.
func standardRouteID(mediaType MediaType) string { return "standard-" + string(mediaType) }

// standardRouteName is how targets and the preview name Standard's route.
const standardRouteName = "Standard"

// standardRoutes is Standard's routing for a media type as one fallback
// route: its normal server for HD and its 4K server for 4K, with no
// overrides; for series, an anime route ahead of it sets Sonarr's anime
// series type. A media type whose server is not a Radarr or Sonarr gets none, so
// that plugin keeps routing it itself.
func standardRoutes(integrations []Integration, layout []StandardDestination, mediaType MediaType) []Route {
	for _, dest := range layout {
		if dest.MediaType != mediaType {
			continue
		}
		for _, id := range []string{dest.HDIntegrationID, dest.UHDIntegrationID} {
			for _, in := range integrations {
				if in.ID == id && selfRouted(in) {
					return nil
				}
			}
		}
		routes := []Route{{
			ID: standardRouteID(mediaType), MediaType: mediaType, Position: 1000, Name: standardRouteName,
			Enabled: true, IsFallback: true,
			HD:  RouteDestination{IntegrationID: dest.HDIntegrationID},
			UHD: RouteDestination{IntegrationID: dest.UHDIntegrationID},
		}}
		if mediaType == MediaTypeSeries {
			// Anime goes to the same servers with Sonarr's anime series type,
			// which numbers episodes the way anime releases do; Seerr does
			// the same. Other settings stay the server's own.
			anime := func(id string) RouteDestination {
				if id == "" {
					return RouteDestination{}
				}
				return RouteDestination{IntegrationID: id, Overrides: map[string]any{configSeriesType: seriesTypeAnime}}
			}
			routes = append([]Route{{
				ID: standardRouteID(mediaType) + "-anime", MediaType: mediaType, Position: 0, Name: standardRouteName,
				Enabled: true, Conditions: RouteConditions{Anime: new(true)},
				HD: anime(dest.HDIntegrationID), UHD: anime(dest.UHDIntegrationID),
			}}, routes...)
		}
		return routes
	}
	return nil
}

// isStandardRouting reports whether routes are Standard's for the media type.
func isStandardRouting(routes []Route, mediaType MediaType) bool {
	return len(routes) > 0 && routes[len(routes)-1].ID == standardRouteID(mediaType)
}

func (s *Service) routingModeStore() (RoutingModeStore, error) {
	store, ok := s.store.(RoutingModeStore)
	if !ok {
		return nil, fmt.Errorf("request store does not support routing modes")
	}
	return store, nil
}

// GetRoutingOverview returns the routing mode and what Standard would do.
func (s *Service) GetRoutingOverview(ctx context.Context, v Viewer) (*RoutingOverview, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.routingModeStore()
	if err != nil {
		return nil, err
	}
	settings, err := store.GetRoutingSettings(ctx)
	if err != nil {
		return nil, err
	}
	integrations, err := s.store.ListIntegrations(ctx)
	if err != nil {
		return nil, err
	}
	layout, blocker := standardLayout(integrations)
	return &RoutingOverview{RoutingSettings: settings, Standard: layout, StandardBlocker: blocker}, nil
}

// UpdateRoutingModeConditional switches between Standard and Advanced.
func (s *Service) UpdateRoutingModeConditional(ctx context.Context, v Viewer, mode RoutingMode, expected int64) (*RoutingOverview, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	if mode != RoutingStandard && mode != RoutingAdvanced {
		return nil, fmt.Errorf("%w: invalid routing mode", ErrInvalidInput)
	}
	store, err := s.routingModeStore()
	if err != nil {
		return nil, err
	}
	if _, err := store.UpdateRoutingModeConditional(ctx, mode, expected); err != nil {
		return nil, err
	}
	return s.GetRoutingOverview(ctx, v)
}

// lockRoutingMode orders changes to the routing mode and to the servers, so a
// server added while Standard is turned on cannot leave Standard on with two
// servers of a kind.
func lockRoutingMode(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('request-routing-mode'))`)
	return err
}

func (r *Repository) GetRoutingSettings(ctx context.Context) (RoutingSettings, error) {
	return scanRoutingSettings(r.pool.QueryRow(ctx, `SELECT mode, revision, updated_at FROM request_routing WHERE id`))
}

func scanRoutingSettings(row pgx.Row) (RoutingSettings, error) {
	var out RoutingSettings
	err := row.Scan(&out.Mode, &out.Revision, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The migration inserts the row; a database without it routes with
		// the rules, as before Standard existed.
		return RoutingSettings{Mode: RoutingAdvanced}, nil
	}
	if err != nil {
		return RoutingSettings{}, fmt.Errorf("get request routing mode: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateRoutingModeConditional(ctx context.Context, mode RoutingMode, expected int64) (RoutingSettings, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return RoutingSettings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockRoutingMode(ctx, tx); err != nil {
		return RoutingSettings{}, err
	}
	if err := lockRevision(ctx, tx, `SELECT revision FROM request_routing WHERE id FOR UPDATE`, nil, expected, true); err != nil {
		return RoutingSettings{}, err
	}
	integrations, err := r.listIntegrations(ctx, tx)
	if err != nil {
		return RoutingSettings{}, err
	}
	layout, blocker := standardLayout(integrations)
	if mode == RoutingStandard && blocker != "" {
		return RoutingSettings{}, &ValidationError{FieldErrors: map[string]string{"mode": blocker}}
	}
	current, err := scanRoutingSettings(tx.QueryRow(ctx, `SELECT mode, revision, updated_at FROM request_routing WHERE id`))
	if err != nil {
		return RoutingSettings{}, err
	}
	if current.Mode == RoutingStandard && mode == RoutingAdvanced {
		if err := seedAdvancedFromStandard(ctx, tx, layout, integrations); err != nil {
			return RoutingSettings{}, err
		}
	}
	out, err := setRoutingMode(ctx, tx, mode)
	if err != nil {
		return RoutingSettings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RoutingSettings{}, err
	}
	return out, nil
}

func setRoutingMode(ctx context.Context, tx pgx.Tx, mode RoutingMode) (RoutingSettings, error) {
	return scanRoutingSettings(tx.QueryRow(ctx, `
		INSERT INTO request_routing (id, mode, updated_at) VALUES (true, $1, now())
		ON CONFLICT (id) DO UPDATE SET mode = EXCLUDED.mode, updated_at = now()
		RETURNING mode, revision, updated_at`, mode))
}

// seedAdvancedFromStandard gives Everything else the servers Standard was
// using, where it has none, so turning Advanced on sends requests where they
// went before. That includes a 4K server an admin once cleared from
// Everything else: Standard was sending 4K copies there since. Only Radarr and
// Sonarr servers that still take the media type, as saved now, are used; a
// media type another plugin (Seerr) routed itself stays with that plugin (see
// ensureSelfRoutedOwnerKept).
// Each tier is carried on its own, so a server that no longer fits one tier
// does not drop the other tier's server.
//
// A Radarr or Sonarr whose 4K switch changed under Standard can still be a
// paused route's destination for the other version (see
// ensureRoutesKeepServerKind). Those destinations are cleared first, so that
// version falls through to Everything else, which then gets Standard's server.
func seedAdvancedFromStandard(ctx context.Context, tx pgx.Tx, layout []StandardDestination, integrations []Integration) error {
	marked, unmarked := []string{}, []string{}
	for _, in := range integrations {
		switch {
		case selfRouted(in):
		case is4KServer(in):
			marked = append(marked, in.ID)
		default:
			unmarked = append(unmarked, in.ID)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE request_routes SET
			hd_integration_id = CASE WHEN hd_integration_id = ANY($1) THEN NULL ELSE hd_integration_id END,
			hd_overrides = CASE WHEN hd_integration_id = ANY($1) THEN '{}'::jsonb ELSE hd_overrides END,
			uhd_integration_id = CASE WHEN uhd_integration_id = ANY($2) THEN NULL ELSE uhd_integration_id END,
			uhd_overrides = CASE WHEN uhd_integration_id = ANY($2) THEN '{}'::jsonb ELSE uhd_overrides END
		WHERE hd_integration_id = ANY($1) OR uhd_integration_id = ANY($2)`, marked, unmarked); err != nil {
		return fmt.Errorf("clear routes to servers that changed tier: %w", err)
	}
	usable := func(id string, mediaType MediaType, fourK bool) *string {
		for _, in := range integrations {
			if in.ID != id {
				continue
			}
			kind, _ := in.PluginConfig[configServiceKind].(string)
			if kind != "" && in.Enabled && serverServes(in, mediaType) && is4KServer(in) == fourK {
				return &id
			}
			return nil
		}
		return nil
	}
	for _, dest := range layout {
		hd := usable(dest.HDIntegrationID, dest.MediaType, false)
		uhd := usable(dest.UHDIntegrationID, dest.MediaType, true)
		if hd == nil && uhd == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO request_routes (id, media_type, position, name, is_fallback, hd_integration_id, uhd_integration_id)
			VALUES ($1, $2, 1000, $3, true, $4, $5)
			ON CONFLICT (id) DO UPDATE SET
				hd_integration_id = coalesce(request_routes.hd_integration_id, EXCLUDED.hd_integration_id),
				uhd_integration_id = coalesce(request_routes.uhd_integration_id, EXCLUDED.uhd_integration_id)
			WHERE request_routes.hd_integration_id IS NULL
			   OR (request_routes.uhd_integration_id IS NULL AND EXCLUDED.uhd_integration_id IS NOT NULL AND NOT request_routes.skip_uhd)`,
			FallbackRouteID(dest.MediaType), dest.MediaType, fallbackRouteName, hd, uhd); err != nil {
			return fmt.Errorf("carry standard routing into everything else: %w", err)
		}
	}
	return nil
}

// standardBeforeSave reads, in a transaction that is about to add or change a
// server, whether Standard is on and where it sends requests. Pass the result
// to advanceIfStandardBroken after the save.
func (r *Repository) standardBeforeSave(ctx context.Context, tx pgx.Tx) (layout []StandardDestination, standard bool, err error) {
	if err := lockRoutingMode(ctx, tx); err != nil {
		return nil, false, err
	}
	current, err := scanRoutingSettings(tx.QueryRow(ctx, `SELECT mode, revision, updated_at FROM request_routing WHERE id FOR UPDATE`))
	if err != nil || current.Mode != RoutingStandard {
		return nil, false, err
	}
	integrations, err := r.listIntegrations(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	layout, _ = standardLayout(integrations)
	return layout, true, nil
}

// ensureRoutesStillFit refuses, under the routing-mode lock taken by
// standardBeforeSave and the server's row lock, a server update that leaves a
// route sending it requests it would no longer take: the other media type, or
// under Advanced the other version. The service checks the same before the
// save; this catches a route saved, or Advanced turned on, in between.
func ensureRoutesStillFit(ctx context.Context, tx pgx.Tx, in Integration, advanced bool) error {
	var raw []byte
	// FOR UPDATE before reading the routes: a route save holds its servers
	// FOR SHARE (ensureDestinationsFit), so one in flight commits first and
	// its route is read below.
	err := tx.QueryRow(ctx, `SELECT plugin_config FROM request_integrations WHERE id = $1 FOR UPDATE`, in.ID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read request integration %s: %w", in.ID, err)
	}
	stored := Integration{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &stored.PluginConfig); err != nil {
			return fmt.Errorf("decode request integration %s config: %w", in.ID, err)
		}
	}
	routes, err := listRoutes(ctx, tx)
	if err != nil {
		return err
	}
	fields := routeKindConflicts(in, routes)
	if advanced && is4KServer(stored) != is4KServer(in) {
		if msg := tierConflict(in, routes); msg != "" {
			fields["plugin_config."+configIs4K] = msg
		}
	}
	if len(fields) > 0 {
		return &ValidationError{FieldErrors: fields}
	}
	return nil
}

// advanceIfStandardBroken turns Advanced on when a saved server leaves a media
// type with two servers of a kind, and gives Everything else the servers
// Standard was using before, so requests keep going where they went. It
// refuses the save when that cannot be kept for a plugin that routes itself.
func (r *Repository) advanceIfStandardBroken(ctx context.Context, tx pgx.Tx, before []StandardDestination) error {
	integrations, err := r.listIntegrations(ctx, tx)
	if err != nil {
		return err
	}
	if _, blocker := standardLayout(integrations); blocker == "" {
		return nil
	}
	if err := seedAdvancedFromStandard(ctx, tx, before, integrations); err != nil {
		return err
	}
	if err := ensureSelfRoutedOwnerKept(ctx, tx, before, integrations); err != nil {
		return err
	}
	if _, err := setRoutingMode(ctx, tx, RoutingAdvanced); err != nil {
		return fmt.Errorf("turn advanced routing on: %w", err)
	}
	return nil
}

// ensureSelfRoutedOwnerKept refuses a save that turns Advanced on when a media
// type Standard sent to a plugin that picks its own server (Seerr) has no
// routing rule and would now also be taken by another request service: with no
// rule, the first of them by name would get its requests. Everything else is
// not seeded with such a plugin, because a rule sends each tier on its own and
// would change how the plugin handles 4K and anime; the admin sets it instead.
func ensureSelfRoutedOwnerKept(ctx context.Context, tx pgx.Tx, before []StandardDestination, integrations []Integration) error {
	type service struct {
		installation int
		capability   string
	}
	for _, dest := range before {
		owners := map[service]bool{}
		var ownerNames []string
		selfRouted := false
		for _, in := range integrations {
			if in.ID != dest.HDIntegrationID && in.ID != dest.UHDIntegrationID || in.InstallationID == nil {
				continue
			}
			if kind, _ := in.PluginConfig[configServiceKind].(string); kind == "" {
				selfRouted = true
			}
			owners[service{*in.InstallationID, in.CapabilityID}] = true
			ownerNames = append(ownerNames, in.Name)
		}
		if !selfRouted {
			continue
		}
		var routed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM request_routes WHERE media_type = $1)`, dest.MediaType).Scan(&routed); err != nil {
			return fmt.Errorf("check routing rules: %w", err)
		}
		if routed {
			continue
		}
		var rivals []Integration
		for _, in := range integrations {
			if eligibleRouterConnection(in, dest.MediaType) && !owners[service{*in.InstallationID, in.CapabilityID}] {
				rivals = append(rivals, in)
			}
		}
		if len(rivals) == 0 {
			continue
		}
		slices.Sort(ownerNames)
		noun := mediaTypePlural(dest.MediaType)
		return &ValidationError{FormError: fmt.Sprintf(
			"%s go to %s, which picks their server itself. With %s also taking %s, no routing rule would say which one gets them. Switch to Advanced routing and set Everything else for %s first.",
			capitalize(noun), strings.Join(ownerNames, ", "), serverNames(rivals), noun, noun)}
	}
	return nil
}

package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
)

// Requester options: a router plugin can let the requester choose some of a
// download server's settings for one request, such as a quality profile. The
// plugin lists the config fields in its manifest; the host offers the choices
// the plugin reports for the server the request would go to, checks a pick
// against them when the request is made, stores it, and at submission sets it
// in that server's config over the server's own value and a route's. The
// plugin reads its config as always.

// RouterRequesterOption is a config field a router capability lets requesters
// choose, as its manifest declares it.
type RouterRequesterOption struct {
	Field string
	// MediaType limits the option to one media type; empty means both.
	MediaType MediaType
	// Label names the choice to requesters.
	Label string
	// ChooserField is the config select that says who chooses the option
	// on a server; empty lets requesters choose on every server.
	ChooserField string
}

// RequesterOption is one setting a requester may choose for a title, with the
// values on offer.
type RequesterOption struct {
	Key   string
	Label string
	// Default is the value in effect when the requester picks nothing, when
	// it is one of the choices; otherwise the first choice.
	Default string
	Choices []RouterOption
}

// RequesterChoice is one pick, with the words it was offered in so it can be
// shown after the server's lists change.
type RequesterChoice struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Value      string `json:"value"`
	ValueLabel string `json:"value_label"`
	// ByAdmin marks a pick an admin made when approving. It applies when the
	// server leaves the option to the approver or to requesters.
	ByAdmin bool `json:"by_admin,omitempty"`
}

// RequesterChoices are a request's picks and the server they were offered on.
// They apply only to that server: a value is an id or a path of that server
// and may name something else on another.
type RequesterChoices struct {
	IntegrationID string            `json:"integration_id,omitempty"`
	Choices       []RequesterChoice `json:"choices,omitempty"`
}

// maxRequesterChoices bounds a request's picks. A plugin declares a handful
// of options at most.
const maxRequesterChoices = 16

// requesterOptionsTimeout bounds the plugin call that lists a server's
// choices, so a download server that does not answer cannot hold a request
// dialog open.
const requesterOptionsTimeout = 15 * time.Second

func encodeRequesterChoices(choices RequesterChoices) ([]byte, error) {
	raw, err := json.Marshal(choices)
	if err != nil {
		return nil, fmt.Errorf("encode requester choices: %w", err)
	}
	return raw, nil
}

func decodeRequesterChoices(raw []byte) (RequesterChoices, error) {
	var choices RequesterChoices
	if len(raw) == 0 {
		return choices, nil
	}
	if err := json.Unmarshal(raw, &choices); err != nil {
		return RequesterChoices{}, fmt.Errorf("decode requester choices: %w", err)
	}
	return choices, nil
}

// Who chooses an option on a server: the values of its chooser select.
const (
	chooserAdmin     = "admin"
	chooserRequester = "requester"
	chooserApprover  = "approver"
)

// chooserOf reads who chooses option on the server with config. Any value
// the host does not know, or none, leaves the admin's saved setting alone.
func chooserOf(option RouterRequesterOption, config map[string]any) string {
	if option.ChooserField == "" {
		return chooserRequester
	}
	switch value, _ := config[option.ChooserField].(string); strings.TrimSpace(value) {
	case chooserRequester:
		return chooserRequester
	case chooserApprover:
		return chooserApprover
	}
	return chooserAdmin
}

// offeredRequesterOptions keeps the declared options a server offers for a
// media type: to requesters those they choose, and to an approving admin
// those as well as the admin's own. An option on a key routing sets itself
// is never offered.
func offeredRequesterOptions(declared []RouterRequesterOption, mediaType MediaType, config map[string]any, approver bool) []RouterRequesterOption {
	var out []RouterRequesterOption
	for _, option := range declared {
		if option.Field == "" || slices.Contains(routingOwnedConfigKeys, option.Field) {
			continue
		}
		if option.MediaType != "" && option.MediaType != mediaType {
			continue
		}
		chooser := chooserOf(option, config)
		if chooser == chooserRequester || (approver && chooser == chooserApprover) {
			out = append(out, option)
		}
	}
	return out
}

// requesterDestination is the one server a request's HD copy (or its 4K copy,
// when it gets no HD one) would go to right now, with the route's overrides
// applied. ok is false when no single server can be named: nothing is
// configured, the plugin picks among several servers, or the routes cannot
// decide yet.
func (s *Service) requesterDestination(ctx context.Context, fc *fulfillContext, probe Request) (conn ResolvedRouterConnection, installationID int, capabilityID string, ok bool) {
	routes := fc.routesFor(probe.MediaType)
	if len(routes) == 0 {
		conns, installationID, capabilityID, err := s.resolveRouterConnections(ctx, fc, probe.MediaType)
		if err != nil || len(conns) != 1 {
			return ResolvedRouterConnection{}, 0, "", false
		}
		return conns[0], installationID, capabilityID, true
	}
	if !probe.RoutingFacts.Captured() && !isStandardRouting(routes, probe.MediaType) && routesUseConditions(routes, probe.MediaType) {
		// TMDB could not supply what the rules match on.
		return ResolvedRouterConnection{}, 0, "", false
	}
	allowed, _ := s.allowedQualities(ctx, probe, fc.settings)
	decisions := decideRoutes(routes, probe, allowed)
	for _, q := range []Quality{Quality1080p, Quality2160p} {
		decision, decided := decisions[q]
		if !decided || decision.Skip {
			continue
		}
		conn, installationID, capabilityID, err := s.routedConnection(ctx, fc, decision, probe.MediaType, q)
		if err != nil {
			return ResolvedRouterConnection{}, 0, "", false
		}
		return conn, installationID, capabilityID, true
	}
	return ResolvedRouterConnection{}, 0, "", false
}

// requesterOffer lists what may be chosen for probe, and the server that
// offers it: what the server offers requesters, or an approving admin. No options, without an error, means there
// is nothing to choose. An error means the server's choices could not be read.
func (s *Service) requesterOffer(ctx context.Context, probe Request, approver bool) (integrationID string, options []RequesterOption, err error) {
	if s.router == nil {
		return "", nil, nil
	}
	fc, err := s.newFulfillContext(ctx)
	if err != nil {
		return "", nil, err
	}
	conn, installationID, capabilityID, ok := s.requesterDestination(ctx, fc, probe)
	if !ok {
		return "", nil, nil
	}
	features, err := s.routerFeatures(ctx, fc, installationID, capabilityID)
	if err != nil {
		return "", nil, err
	}
	offered := offeredRequesterOptions(features.RequesterOptions, probe.MediaType, conn.Config, approver)
	if len(offered) == 0 {
		return "", nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requesterOptionsTimeout)
	defer cancel()
	lists, err := s.router.ListConfigOptions(ctx, installationID, capabilityID, conn)
	if err != nil {
		return "", nil, fmt.Errorf("list the download server's choices: %w", err)
	}
	for _, option := range offered {
		choices := lists[option.Field]
		if len(choices) == 0 {
			continue
		}
		current := fmt.Sprint(conn.Config[option.Field])
		if !slices.ContainsFunc(choices, func(c RouterOption) bool { return c.Value == current }) {
			current = choices[0].Value
		}
		label := option.Label
		if label == "" {
			label = option.Field
		}
		options = append(options, RequesterOption{Key: option.Field, Label: label, Default: current, Choices: choices})
	}
	return conn.ID, options, nil
}

// requestProbe stands in for a request that does not exist yet, so routing
// can say where it would go.
func requestProbe(viewer Viewer, mediaType MediaType, tmdbID int, facts RoutingFacts) Request {
	return Request{
		MediaType: mediaType, TMDBID: tmdbID,
		RequestedByUserID: viewer.UserID, RequestedByProfileID: viewer.ProfileID,
		RoutingFacts: facts, IsAnime: facts.Anime,
	}
}

// RequesterOptions returns the download server settings the viewer may choose
// when requesting a title. It is empty when the server the title would go to
// offers none, and also when its choices cannot be read: the request can
// still be made without a pick.
func (s *Service) RequesterOptions(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID int) ([]RequesterOption, error) {
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
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	if tmdbID <= 0 {
		return nil, fmt.Errorf("%w: tmdb_id is required", ErrInvalidInput)
	}
	facts := s.routingFacts(ctx, s.requestDetail(ctx, mediaType, tmdbID))
	_, options, err := s.requesterOffer(ctx, requestProbe(viewer, mediaType, tmdbID, facts), false)
	if err != nil {
		slog.WarnContext(ctx, "requests: could not list requester options", "component", "requests",
			"media_type", string(mediaType), "tmdb_id", tmdbID, "err", err)
		return nil, nil
	}
	return options, nil
}

// errRequesterOptions refuses a request whose picks cannot be honoured.
func errRequesterOptions(message string) error {
	return &ValidationError{FieldErrors: map[string]string{"options": message}}
}

// resolveRequesterChoices checks a new request's picks against what the
// server it would go to offers right now. A pick for an option that is not
// offered, or a value that is not one of its choices, refuses the request:
// the value reaches the download server as a setting, so only listed values
// may pass.
func (s *Service) resolveRequesterChoices(ctx context.Context, probe Request, picks map[string]string) (RequesterChoices, error) {
	return s.resolveChoices(ctx, probe, picks, false)
}

// resolveChoices checks picks against the requester's offer, or the approving
// admin's, and returns them in the words they were offered in.
func (s *Service) resolveChoices(ctx context.Context, probe Request, picks map[string]string, approver bool) (RequesterChoices, error) {
	if len(picks) == 0 {
		return RequesterChoices{}, nil
	}
	if len(picks) > maxRequesterChoices {
		return RequesterChoices{}, errRequesterOptions("Too many options.")
	}
	integrationID, options, err := s.requesterOffer(ctx, probe, approver)
	if err != nil {
		slog.WarnContext(ctx, "requests: could not check requester options", "component", "requests",
			"media_type", string(probe.MediaType), "tmdb_id", probe.TMDBID, "err", err)
		return RequesterChoices{}, errRequesterOptions("The download server's choices could not be read. Try again, or continue without choosing.")
	}
	out := RequesterChoices{IntegrationID: integrationID}
	for _, key := range slices.Sorted(maps.Keys(picks)) {
		i := slices.IndexFunc(options, func(o RequesterOption) bool { return o.Key == key })
		if i < 0 {
			return RequesterChoices{}, errRequesterOptions("This title has no such option. Reload and try again.")
		}
		option := options[i]
		j := slices.IndexFunc(option.Choices, func(c RouterOption) bool { return c.Value == picks[key] })
		if j < 0 {
			return RequesterChoices{}, errRequesterOptions(fmt.Sprintf("That %s is no longer offered. Reload and try again.", strings.ToLower(option.Label)))
		}
		out.Choices = append(out.Choices, RequesterChoice{
			Key: key, Label: option.Label, Value: option.Choices[j].Value, ValueLabel: option.Choices[j].Label,
			ByAdmin: approver,
		})
	}
	return out, nil
}

// ApproverOptions returns the download server settings an admin may choose
// when approving a pending request: the options the server leaves to the
// approver, and those it leaves to requesters. An option's default is
// the requester's pick, when there is one. It is empty when there is nothing
// to choose, and also when the server's choices cannot be read: the request
// can still be approved without a pick.
func (s *Service) ApproverOptions(ctx context.Context, viewer Viewer, id string) ([]RequesterOption, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	integrationID, options, err := s.requesterOffer(ctx, *req, true)
	if err != nil {
		slog.WarnContext(ctx, "requests: could not list approver options", "component", "requests",
			"request_id", req.ID, "err", err)
		return nil, nil
	}
	if req.RequesterChoices.IntegrationID != integrationID {
		return options, nil
	}
	for i := range options {
		for _, pick := range req.RequesterChoices.Choices {
			if pick.Key == options[i].Key && slices.ContainsFunc(options[i].Choices, func(c RouterOption) bool { return c.Value == pick.Value }) {
				options[i].Default = pick.Value
			}
		}
	}
	return options, nil
}

// ApproveWithOptions approves a pending request with the admin's picks from
// ApproverOptions. A pick replaces the requester's for the same option; the
// requester's other picks stay. No picks is Approve.
func (s *Service) ApproveWithOptions(ctx context.Context, viewer Viewer, id string, picks map[string]string) (*Request, error) {
	if !viewer.IsAdmin {
		return nil, ErrForbidden
	}
	if len(picks) == 0 {
		return s.Approve(ctx, viewer, id)
	}
	id = strings.TrimSpace(id)
	req, err := s.store.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	chosen, err := s.resolveChoices(ctx, *req, picks, true)
	if err != nil {
		return nil, err
	}
	if req.RequesterChoices.IntegrationID == chosen.IntegrationID {
		for _, earlier := range req.RequesterChoices.Choices {
			if _, replaced := picks[earlier.Key]; !replaced {
				chosen.Choices = append(chosen.Choices, earlier)
			}
		}
		slices.SortFunc(chosen.Choices, func(a, b RequesterChoice) int { return strings.Compare(a.Key, b.Key) })
	}
	if err := s.store.SetPendingRequesterChoices(ctx, id, chosen); err != nil {
		return nil, err
	}
	return s.Approve(ctx, viewer, id)
}

// applyRequesterChoices sets a request's picks in conn's config when conn is
// the server they were offered on and that server still offers the option.
// Picks for another server, or for an option the server no longer leaves to
// whoever picked, are left out, so the server's own settings apply. conn's
// config is copied before it is changed.
func (s *Service) applyRequesterChoices(ctx context.Context, fc *fulfillContext, req Request, conn *ResolvedRouterConnection, installationID int, capabilityID string) error {
	picks := req.RequesterChoices
	if len(picks.Choices) == 0 || picks.IntegrationID != conn.ID {
		return nil
	}
	features, err := s.routerFeatures(ctx, fc, installationID, capabilityID)
	if err != nil {
		return err
	}
	offered := offeredRequesterOptions(features.RequesterOptions, req.MediaType, conn.Config, false)
	toApprover := offeredRequesterOptions(features.RequesterOptions, req.MediaType, conn.Config, true)
	config := maps.Clone(conn.Config)
	if config == nil {
		config = map[string]any{}
	}
	for _, pick := range picks.Choices {
		allowed := offered
		if pick.ByAdmin {
			allowed = toApprover
		}
		if slices.ContainsFunc(allowed, func(o RouterRequesterOption) bool { return o.Field == pick.Key }) {
			config[pick.Key] = pick.Value
		}
	}
	conn.Config = config
	return nil
}

package nodecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

// RecipeSource reads the recipes the core stores for transcode node
// transports, keyed by transport id. A miss, or a recipe the store no longer
// stands behind, is (nil, false, nil); a failure to read the store is an
// error, which resolve answers with 503 so the node retries rather than
// giving up on a session that still exists. *noderecipe.Store implements it.
type RecipeSource interface {
	Lookup(ctx context.Context, transportID string) (*playback.RecipeCard, bool, error)
}

// DenySource reports a playback session's deny marker. *playback.StreamDeny
// implements it.
type DenySource interface {
	Denied(ctx context.Context, sessionID string) bool
}

// resolveRefusal is a refused resolve: the status and the reason the body
// carries.
type resolveRefusal struct {
	status int
	reason string
}

var (
	refuseNotFound      = &resolveRefusal{http.StatusNotFound, ResolveNotFound}
	refuseDenied        = &resolveRefusal{http.StatusGone, ResolveDenied}
	refuseStopped       = &resolveRefusal{http.StatusGone, ResolveStopped}
	refuseSourceChanged = &resolveRefusal{http.StatusGone, ResolveSourceChanged}
	refuseInvalidToken  = &resolveRefusal{http.StatusForbidden, ResolveInvalidToken}
	refuseWrongRoute    = &resolveRefusal{http.StatusForbidden, ResolveWrongRoute}
	// refuseUnavailable is a core that cannot verify tokens at all, or
	// cannot read the calling node's registered URL or the recipe store
	// right now.
	refuseUnavailable = &resolveRefusal{http.StatusServiceUnavailable, ""}
)

// HandleResolve serves POST /api/internal/nodes/self/resolve: the recipe and
// playback identity of one transport the calling node serves. It is the only
// place a stream token a node received is verified, and the only place a node
// learns of a deny marker; see resolveHLS and resolveRemux for what each
// purpose checks.
func (s *Server) HandleResolve(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req ResolveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes)).Decode(&req); err != nil || req.TransportID == "" {
		http.Error(w, "invalid resolve request", http.StatusBadRequest)
		return
	}
	var (
		resolution *Resolution
		refusal    *resolveRefusal
	)
	switch req.Purpose {
	case ResolvePurposeHLS:
		resolution, refusal = s.resolveHLS(r.Context(), id, req)
	case ResolvePurposeRemux:
		resolution, refusal = s.resolveRemux(r.Context(), id, req)
	default:
		http.Error(w, "unknown resolve purpose", http.StatusBadRequest)
		return
	}
	if refusal == refuseUnavailable {
		http.Error(w, "recipe resolution unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if refusal == nil {
		refusal = s.grantResolution(r.Context(), id, resolution)
	}
	switch {
	case refusal == refuseUnavailable:
		http.Error(w, "recipe resolution unavailable", http.StatusServiceUnavailable)
	case refusal != nil:
		w.WriteHeader(refusal.status)
		_ = json.NewEncoder(w).Encode(ResolveRefusal{Reason: refusal.reason})
	default:
		_ = json.NewEncoder(w).Encode(resolution)
	}
}

// denied reports whether the deny marker revokes the transport or, when it
// differs, the playback session it serves: the marker names the playback
// session, while the URL a node serves names the transport.
func (s *Server) denied(ctx context.Context, transportID, playbackSessionID string) bool {
	if s.opts.Deny == nil {
		return false
	}
	if s.opts.Deny.Denied(ctx, transportID) {
		return true
	}
	return playbackSessionID != "" && playbackSessionID != transportID && s.opts.Deny.Denied(ctx, playbackSessionID)
}

// verifyToken verifies a stream token a node passed through. A core without a
// stream secret verifies nothing: an HMAC key of "" is one anyone can sign
// with.
func (s *Server) verifyToken(token string) (*streamtoken.Claims, *resolveRefusal) {
	secret := ""
	if s.opts.StreamSecret != nil {
		secret = s.opts.StreamSecret()
	}
	if secret == "" {
		return nil, refuseUnavailable
	}
	claims, err := streamtoken.Verify(token, secret)
	if err != nil {
		return nil, refuseInvalidToken
	}
	return claims, nil
}

// resolveHLS finds the recipe a node rebuilds a lost HLS transcode from.
//
// A native token carries the full byte-affecting recipe. Two flows have none
// and rebuild from the recipe store instead: jellycompat, whose node-hop token
// is identity-only by design (see internal/noderecipe), and a
// header-authenticated (tokenless) attempt, where no client-visible URL
// carries a credential. The token was never the node route's authorization —
// the relay's node signature is — so its absence only removes a recipe
// source. A token that is invalid or names another transport is a forged or
// stale request and resolves nothing.
//
// A stored recipe, when the store holds one for the transport, wins over a
// complete token too. The core writes it, and rewrites it when it moves the
// transport to another executor (a move off an unreachable node), while the
// client keeps presenting the token minted for the first executor: the stored
// card names the node the transport runs on now. Whichever card is chosen
// must be bound to the authenticated node (see servesNode): transport ids are
// not secrets, and the recipe names a media path.
func (s *Server) resolveHLS(ctx context.Context, nodeID int, req ResolveRequest) (*Resolution, *resolveRefusal) {
	transportID := req.TransportID
	if s.denied(ctx, transportID, "") {
		return nil, refuseDenied
	}
	var card playback.RecipeCard
	var claims *streamtoken.Claims
	complete := false
	if req.StreamToken != "" {
		verified, refusal := s.verifyToken(req.StreamToken)
		if refusal == refuseUnavailable {
			return nil, refusal
		}
		if refusal != nil {
			slog.WarnContext(ctx, "resolve: invalid stream token", "component", "nodecontrol", "session", transportID, "playback_session_id", transportID)
			return nil, refuseNotFound
		}
		claims = verified
		card = playback.RecipeCardFromClaims(claims)
		if !recipeServesTransport(card, transportID) {
			return nil, refuseNotFound
		}
		if s.denied(ctx, transportID, claims.SessionID) {
			return nil, refuseDenied
		}
		complete = recipeIsComplete(card)
	}
	stored, refusal := s.storedHLSRecipe(ctx, transportID)
	if refusal != nil {
		return nil, refusal
	}
	switch {
	case stored != nil:
		card = *stored
		if s.denied(ctx, transportID, card.SessionID) {
			return nil, refuseDenied
		}
	case !complete:
		return nil, refuseNotFound
	}
	if refusal := s.servesNode(ctx, nodeID, card); refusal != nil {
		if refusal == refuseWrongRoute {
			slog.WarnContext(ctx, "resolve: recipe bound to another node", "component", "nodecontrol", "node_id", nodeID, "session", transportID)
		}
		return nil, refusal
	}
	resolution := newResolution(card, transportID)
	startedAt, source := sessionStart(card, claims)
	resolution.StartedAt, resolution.StartedAtSource = startedAt, string(source)
	return resolution, nil
}

// storedHLSRecipe reads the complete transcode recipe the store holds for
// transportID, or nil when it holds none (a miss, a revoked card, or one that
// does not describe the transport). A store that cannot be read is
// unavailable rather than a miss: the stored card may be the only one bound
// to the calling node.
func (s *Server) storedHLSRecipe(ctx context.Context, transportID string) (*playback.RecipeCard, *resolveRefusal) {
	if s.opts.Recipes == nil {
		return nil, nil
	}
	stored, ok, err := s.opts.Recipes.Lookup(ctx, transportID)
	if err != nil {
		slog.WarnContext(ctx, "resolve: cannot read the recipe store", "component", "nodecontrol", "session", transportID, "error", err)
		return nil, refuseUnavailable
	}
	if !ok || stored == nil || !recipeServesTransport(*stored, transportID) || !recipeIsComplete(*stored) {
		return nil, nil
	}
	return stored, nil
}

// sessionStart is when the playback session a rebuilt transcode serves
// started, so the node's session record keeps it across the rebuild: the
// original start the token or the stored recipe carries, else the token's
// issue time. A tokenless rebuild of a card with no original start has none.
func sessionStart(card playback.RecipeCard, claims *streamtoken.Claims) (time.Time, streamtoken.StartedAtSource) {
	if claims != nil && claims.OriginalStartedAtUnixNano != 0 {
		return claims.StartedAt()
	}
	if !card.OriginalStartedAt.IsZero() {
		return card.OriginalStartedAt.UTC(), streamtoken.StartedAtSourceClaim
	}
	return claims.StartedAt()
}

// servesNode checks that an HLS recipe card was started on node nodeID. The
// planner's execution node id binds it when the card carries one; a card
// without one (written before routing assignments carried the id) is bound by
// the node URL the core started it on, compared with the node's registered
// URL in the form the key registry compares node URLs. A card bound by
// neither is refused: every remote start records the URL it dialed, so an
// unbound card is not a remote transcode recipe any node may rebuild.
func (s *Server) servesNode(ctx context.Context, nodeID int, card playback.RecipeCard) *resolveRefusal {
	if card.RoutingExecutionNodeID > 0 {
		if card.RoutingExecutionNodeID != nodeID {
			return refuseWrongRoute
		}
		return nil
	}
	cardURL := nodepool.CanonicalNodeURL(card.TranscodeNodeURL)
	if cardURL == "" {
		return refuseWrongRoute
	}
	node, err := s.identity(ctx, nodeID)
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		return refuseWrongRoute
	}
	if err != nil {
		slog.WarnContext(ctx, "resolve: read node identity", "component", "nodecontrol", "node_id", nodeID, "error", err)
		return refuseUnavailable
	}
	if node.URL == "" || nodepool.CanonicalNodeURL(node.URL) != cardURL {
		return refuseWrongRoute
	}
	return nil
}

// resolveRemux authorizes one progressive remux request the node received.
// The node signature authenticated the relay; the token binds the media
// recipe and the exact transcode-execution/proxy-egress route, here to the
// authenticated node; the stored recipe is the durable authority a stop or a
// force reload withdraws, so a token that remains cryptographically valid
// cannot start FFmpeg after its session ended.
func (s *Server) resolveRemux(ctx context.Context, nodeID int, req ResolveRequest) (*Resolution, *resolveRefusal) {
	claims, refusal := s.verifyToken(strings.TrimSpace(req.StreamToken))
	if refusal != nil {
		return nil, refusal
	}
	transportID := claims.TranscodeTransportID
	if transportID == "" {
		transportID = claims.SessionID
	}
	if s.denied(ctx, transportID, claims.SessionID) {
		return nil, refuseDenied
	}
	if transportID == "" || transportID != req.TransportID ||
		claims.TranscodeNode == "" ||
		claims.RoutingWorkload != string(noderouting.WorkloadRemux) ||
		claims.RoutingExecution != string(noderouting.ExecutionTranscode) ||
		claims.RoutingEgress != string(noderouting.EgressProxy) ||
		!transcodeNodeRemuxPlayMethod(claims.PlayMethod) {
		return nil, refuseWrongRoute
	}
	// The planner binds a routed remux to the stable stream_nodes row, never
	// to a URL, which may differ from the node's own address in a
	// split-horizon deployment. A transcode-executed progressive route has
	// never shipped without the row id, so an id-less token fails closed.
	if claims.RoutingExecutionNodeID <= 0 || claims.RoutingExecutionNodeID != nodeID {
		return nil, refuseWrongRoute
	}
	if claims.PlayMethod == streamtoken.PlayMethodAudioDownmixRemux &&
		(!claims.TranscodeAudio || claims.TargetAudioChannels != 2 ||
			!playback.IsAudioToAACStereoDownmixV3(claims.SourceAudioChannels, claims.TargetCodecAudio, claims.TargetAudioChannels)) {
		return nil, refuseNotFound
	}
	resolution := newResolution(playback.RecipeCardFromClaims(claims), transportID)
	if claims.PlayMethod == streamtoken.PlayMethodThemeAAC {
		if !claims.TranscodeAudio || !claims.AudioOnly || claims.TargetCodecAudio != themesongs.CodecAAC || claims.ThemeID <= 0 {
			return nil, refuseNotFound
		}
		if !themesongs.Unchanged(claims.MediaPath, claims.ThemeSize, time.Unix(0, claims.ThemeModifiedUnixNano)) {
			return nil, refuseSourceChanged
		}
		resolution.Theme = &ResolvedTheme{ID: claims.ThemeID, Size: claims.ThemeSize, ModifiedUnixNano: claims.ThemeModifiedUnixNano}
	}
	active, err := s.remuxAuthorityActive(ctx, transportID, claims)
	if err != nil {
		slog.WarnContext(ctx, "resolve: cannot read the recipe store", "component", "nodecontrol", "session", transportID, "error", err)
		return nil, refuseUnavailable
	}
	if !active {
		return nil, refuseStopped
	}
	startedAt, source := claims.StartedAt()
	resolution.StartedAt, resolution.StartedAtSource = startedAt, string(source)
	return resolution, nil
}

// remuxAuthorityActive checks the durable transport record the core writes
// before it publishes a node route. Stop and force reload delete or revoke
// it, so a token that is still valid cannot start a new FFmpeg process after
// either. A failure to read the record is an error, not a stop.
func (s *Server) remuxAuthorityActive(ctx context.Context, transportID string, claims *streamtoken.Claims) (bool, error) {
	if s.opts.Recipes == nil {
		return false, nil
	}
	card, ok, err := s.opts.Recipes.Lookup(ctx, transportID)
	if err != nil || !ok || card == nil {
		return false, err
	}
	stored := card.ToClaims()
	return stored.SessionID == claims.SessionID &&
		stored.MediaPath == claims.MediaPath &&
		stored.PlayMethod == claims.PlayMethod &&
		stored.TranscodeNode == claims.TranscodeNode &&
		stored.TranscodeTransportID == transportID &&
		stored.RoutingWorkload == claims.RoutingWorkload &&
		stored.RoutingExecution == claims.RoutingExecution &&
		stored.RoutingExecutionNodeID == claims.RoutingExecutionNodeID &&
		stored.RoutingEgress == claims.RoutingEgress &&
		stored.RoutingEgressNodeID == claims.RoutingEgressNodeID, nil
}

// newResolution wraps card with the playback identity it carries. A card
// without a session id is a jellycompat transport, whose id is its playback
// session's.
func newResolution(card playback.RecipeCard, transportID string) *Resolution {
	playbackSessionID := card.SessionID
	if playbackSessionID == "" {
		playbackSessionID = transportID
	}
	return &Resolution{
		Recipe:            card,
		PlaybackSessionID: playbackSessionID,
		UserID:            card.UserID,
		ProfileID:         card.ProfileID,
		MediaFileID:       card.MediaFileID,
	}
}

// recipeServesTransport reports whether a recipe card describes the transcode
// a node serves under transportID — the id in the node-facing URL.
//
// The two writers key that id differently and both shapes are accepted. A
// native v3 remote transcode runs under a plan-scoped transport id (so a
// prepared successor can coexist with its predecessor), recorded on the card
// as TranscodeTransportID; jellycompat runs under the upstream playback
// session id and leaves the field empty, so SessionID is the transport id
// there. A card that matches neither is a forged, stale, or misrouted
// request. An empty PlayMethod counts as a transcode card (back-compat).
func recipeServesTransport(card playback.RecipeCard, transportID string) bool {
	expected := card.SessionID
	if card.TranscodeTransportID != "" {
		expected = card.TranscodeTransportID
	}
	return expected == transportID && card.IsTranscodeRecipe() && playback.ValidateCopyFMP4RecipeCard(card) == nil
}

// recipeIsComplete reports whether a recipe carries the encode parameters
// FFmpeg needs. An identity-only card (the jellycompat node-hop token) is not
// complete and has to be resolved against the recipe store.
func recipeIsComplete(card playback.RecipeCard) bool {
	return card.SegmentDuration > 0 && card.TargetCodecVideo != ""
}

func transcodeNodeRemuxPlayMethod(method string) bool {
	return method == string(playback.PlayRemux) || method == streamtoken.PlayMethodAudioDownmixRemux ||
		method == streamtoken.PlayMethodThemeAAC
}

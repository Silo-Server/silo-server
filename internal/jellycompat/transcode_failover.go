package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A Jellyfin-compatible transcode whose node dies mid-stream moves to another
// executor without the client's help. Jellyfin clients fetch a VOD playlist
// once, never replan, and the protocol has no server-to-client command that
// would make them fetch new URLs, so every URL the client holds has to keep
// working after the move.
//
// They do, because the move only changes where FFmpeg runs. The client's
// manifest and segment URLs name the compat play session (API egress) or the
// upstream session through a proxy token (proxy egress), never a node by
// itself, and the transport keeps its id. The move rewrites the transport's
// recipe to name a reachable executor, and that executor rebuilds the
// transcode from the recipe at the segment the client asks for next: a
// transcode node from the shared node recipe store, the same way it rebuilds
// after its own restart, or this process through the ordinary local
// reconstruction.
//
// Two relays notice a dead node:
//   - API egress: this server's own relay cannot connect, moves the transport,
//     and serves the request from the new executor.
//   - Proxy egress: the proxy cannot connect. It holds no session state, so it
//     publishes a noderecipe.MoveRequest; the API server that owns the session
//     moves the transport to another node behind the same proxy, and the proxy
//     follows the rewritten record (see internal/proxy).

// compatTranscodeMoveTimeout bounds one move, including the route resolution's
// capability reads. Every request for the session waits on the same move, so
// it is detached from the request that found the node dead.
const compatTranscodeMoveTimeout = 30 * time.Second

// compatMaxMoveHops bounds how many dead executors one request moves past.
const compatMaxMoveHops = 3

// compatMaxMoveRouteAttempts bounds how many route shapes a move tries when the
// resolver offers one the move cannot use.
const compatMaxMoveRouteAttempts = 8

var (
	errCompatMoveNoSession = errors.New("no Jellyfin-compatible transcode on this server for the transport")
	errCompatMoveNoRoute   = errors.New("no other executor can take the transcode")
)

// moveCompatTranscodeOffNode moves a remote Jellyfin-compatible transcode off
// deadNodeURL and returns where it runs now: a transcode node URL, or "" for
// this process. failedNodes are nodes the caller already found unreachable,
// which the move must not pick either. Concurrent callers for one session share
// a single move. When the transport no longer runs on deadNodeURL, nothing
// moves and its current executor is returned.
func (h *PlaybackHandler) moveCompatTranscodeOffNode(ctx context.Context, upstreamSessionID, deadNodeURL string, failedNodes ...string) (string, error) {
	if h == nil || upstreamSessionID == "" || deadNodeURL == "" {
		return "", errCompatMoveNoSession
	}
	detached := context.WithoutCancel(ctx)
	key := upstreamSessionID + "\x00" + nodepool.NormalizeNodeURL(deadNodeURL)
	results := h.compatMoves.DoChan(key, func() (any, error) {
		moveCtx, cancel := context.WithTimeout(detached, compatTranscodeMoveTimeout)
		defer cancel()
		return h.doMoveCompatTranscode(moveCtx, upstreamSessionID, deadNodeURL, failedNodes)
	})
	select {
	case result := <-results:
		if result.Err != nil {
			return "", result.Err
		}
		nodeURL, _ := result.Val.(string)
		return nodeURL, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (h *PlaybackHandler) doMoveCompatTranscode(ctx context.Context, upstreamSessionID, deadNodeURL string, failedNodes []string) (string, error) {
	if h.playbackStore == nil || h.sessionMgr == nil || h.fileResolver == nil {
		return "", errCompatMoveNoSession
	}
	playSession, ok := h.playbackStore.FindByUpstreamSessionID(upstreamSessionID)
	if !ok || playSession.Recipe == nil || playSession.RoutingAssignment == nil ||
		playSession.UpstreamPlayMethod != string(playback.PlayTranscode) || !playSession.Recipe.IsTranscodeRecipe() {
		// Only an HLS transcode (video encode or copy) rebuilds from its recipe.
		return "", errCompatMoveNoSession
	}
	if !sameCompatNode(playSession.Recipe.TranscodeNodeURL, deadNodeURL) {
		return playSession.Recipe.TranscodeNodeURL, nil
	}
	assignment := *playSession.RoutingAssignment
	if assignment.Execution != string(noderouting.ExecutionTranscode) {
		return "", fmt.Errorf("%w: route execution is %q", errCompatMoveNoSession, assignment.Execution)
	}
	upstream, err := h.sessionMgr.GetSession(upstreamSessionID)
	if err != nil || upstream == nil {
		return "", errCompatMoveNoSession
	}
	source := compatMoveSource(playSession)
	if source == nil {
		return "", fmt.Errorf("%w: no media source matches the running recipe", errCompatMoveNoSession)
	}
	file, err := h.fileResolver.GetByID(ctx, source.FileID)
	if err != nil {
		return "", fmt.Errorf("load media file: %w", err)
	}

	decision, err := h.resolveCompatMoveRoute(ctx, upstream, file, *source, *playSession.Recipe, deadNodeURL, failedNodes, assignment)
	if err != nil {
		return "", err
	}
	next := assignment
	nodeURL := ""
	if decision.Shape.Execution == noderouting.ExecutionTranscode {
		nodeURL = decision.Plan.TranscodeNode.URL
		next.ExecutionNodeID = h.compatTranscodeNodeID(nodeURL, decision.Plan.TranscodeNode)
		next.ExecutionNodeURL = nodeURL
	} else {
		next.Execution = string(noderouting.ExecutionAPI)
		next.ExecutionNodeID = 0
		next.ExecutionNodeURL = ""
	}

	current, err := h.commitCompatTranscodeMove(ctx, playSession.ID, upstreamSessionID, deadNodeURL, nodeURL, next)
	if err != nil || !sameCompatNode(current, nodeURL) {
		// Nothing was committed for this decision: the route changed under
		// the move, or the commit failed. Give back what Resolve reserved. The
		// planner keeps one reservation per session, so a route change that
		// committed during the move loses its reservation too; the next start
		// or move for the session reserves again.
		h.releaseCompatSessionReservation(upstreamSessionID)
		return current, err
	}
	executor := []any{"executor", string(noderouting.ExecutionTranscode), "to_node", logredact.SanitizeURL(nodeURL)}
	if nodeURL == "" {
		// A local route reserves nothing; drop the dead node's reservation.
		executor = []any{"executor", string(noderouting.ExecutionAPI)}
		h.releaseCompatSessionReservation(upstreamSessionID)
	}
	h.syncSessionsNow(ctx, "compat_transcode_moved")
	slog.WarnContext(ctx, "moved a Jellyfin-compatible transcode off an unreachable node", append([]any{"component", "jellycompat",
		"from_node", logredact.SanitizeURL(deadNodeURL), "egress", assignment.Egress, "playback_session_id", upstreamSessionID}, executor...)...)
	return nodeURL, nil
}

// compatMoveSource is the negotiated source the running recipe was built from.
func compatMoveSource(playSession *PlaybackSession) *PlaybackMediaSource {
	for i := range playSession.MediaSources {
		if compatRecipeMatchesSource(playSession.Recipe, playSession.MediaSources[i]) {
			source := playSession.MediaSources[i]
			return &source
		}
	}
	return nil
}

// resolveCompatMoveRoute picks the executor a moved transport runs on. The
// client keeps fetching from the same origin, so the egress cannot change: an
// API-egress transport may move to another node or to this process, and a
// proxy-egress transport only to another transcode node behind the same proxy.
// The new executor must run the frozen recipe, so the tone-map mode it was
// started with is required of the candidates, this process included.
func (h *PlaybackHandler) resolveCompatMoveRoute(
	ctx context.Context,
	upstream *playback.Session,
	file *models.MediaFile,
	source PlaybackMediaSource,
	recipe playback.RecipeCard,
	deadNodeURL string,
	failedNodes []string,
	assignment playback.NodeRoutingAssignment,
) (noderouting.Decision, error) {
	proxyEgress := assignment.Egress == string(noderouting.EgressProxy)
	if proxyEgress && assignment.EgressNodeID == 0 {
		return noderouting.Decision{}, fmt.Errorf("%w: the proxy route names no proxy", errCompatMoveNoRoute)
	}
	excludedNodes := map[string]struct{}{nodepool.NormalizeNodeURL(deadNodeURL): {}}
	for _, failed := range failedNodes {
		if failed != "" {
			excludedNodes[nodepool.NormalizeNodeURL(failed)] = struct{}{}
		}
	}
	excludedShapes := map[string]struct{}{}
	if !proxyEgress && recipe.ToneMapMode != "" {
		// The candidate filter checks transcode nodes only. A local rebuild
		// fails on a tone-map mode this process cannot run, so leave it out.
		local, err := h.localToneMapCapabilities(ctx)
		if err != nil || !local.Supports(recipe.ToneMapMode, recipe.ToneMapSourceKind) {
			excludedShapes["hls_video_api"] = struct{}{}
		}
	}
	policy := h.playbackRoutingPolicy()
	for range compatMaxMoveRouteAttempts {
		request, err := h.compatHLSResolveRequest(ctx, upstream, file, source, recipe.ToneMapMode, "",
			excludedNodes, excludedShapes, policy)
		if err != nil {
			return noderouting.Decision{}, err
		}
		// The session's current route is the dead one; nothing to stick to.
		request.CurrentTranscodeURL = ""
		if proxyEgress {
			request.ProxyEligible = func(node *nodepool.Node) bool {
				return node != nil && node.ID == assignment.EgressNodeID
			}
		} else {
			request.ProxyAllowed = false
		}
		decision, err := noderouting.Resolve(noderouting.AdaptSessionPlanner(h.NodePlanner), request)
		if err != nil {
			return noderouting.Decision{}, err
		}
		if !decision.Selected() {
			return noderouting.Decision{}, fmt.Errorf("%w: %s", errCompatMoveNoRoute, decision.Outcome)
		}
		if compatMoveRouteUsable(decision, proxyEgress, assignment) {
			return decision, nil
		}
		h.releaseCompatSessionReservation(upstream.ID)
		excludedShapes[decision.Shape.ID] = struct{}{}
	}
	return noderouting.Decision{}, errCompatMoveNoRoute
}

func compatMoveRouteUsable(decision noderouting.Decision, proxyEgress bool, assignment playback.NodeRoutingAssignment) bool {
	if proxyEgress {
		return decision.Shape.Egress == noderouting.EgressProxy &&
			decision.Shape.Execution == noderouting.ExecutionTranscode &&
			decision.Plan.TranscodeNode != nil &&
			decision.Plan.ProxyNode != nil && decision.Plan.ProxyNode.ID == assignment.EgressNodeID
	}
	if decision.Shape.Egress != noderouting.EgressAPI {
		return false
	}
	switch decision.Shape.Execution {
	case noderouting.ExecutionAPI:
		return true
	case noderouting.ExecutionTranscode:
		return decision.Plan.TranscodeNode != nil
	default:
		return false
	}
}

// commitCompatTranscodeMove re-points the transport at its new executor and
// returns the executor the session runs on afterwards. It holds the session
// lifecycle lock that a remote start's publication and a local start hold, and
// re-checks that the transport still runs on the dead node, so a concurrent
// audio switch or restart wins and the move commits nothing.
//
// The node recipe record is written first: it is what the new node rebuilds
// from and what a proxy follows, and its failure leaves everything else as it
// was. The compat store and the upstream session follow.
func (h *PlaybackHandler) commitCompatTranscodeMove(
	ctx context.Context,
	playSessionID, upstreamSessionID, deadNodeURL, nodeURL string,
	assignment playback.NodeRoutingAssignment,
) (string, error) {
	unlock := h.tm.LockSessionLifecycle(upstreamSessionID)
	defer unlock()

	playSession, ok := h.playbackStore.Get(playSessionID)
	if !ok || playSession.UpstreamSessionID != upstreamSessionID || playSession.Recipe == nil {
		return "", errCompatMoveNoSession
	}
	if !sameCompatNode(playSession.Recipe.TranscodeNodeURL, deadNodeURL) {
		return playSession.Recipe.TranscodeNodeURL, nil
	}
	if upstream, err := h.sessionMgr.GetSession(upstreamSessionID); err != nil || upstream == nil ||
		!sameCompatNode(upstream.TranscodeNodeURL, deadNodeURL) {
		return "", errCompatMoveNoSession
	}
	previous := *playSession.Recipe
	previousAssignment := playSession.RoutingAssignment
	next := previous
	next.TranscodeNodeURL = nodeURL
	next.RoutingExecution = assignment.Execution
	next.RoutingExecutionNodeID = assignment.ExecutionNodeID
	// These describe the dead executor's hardware. Every rebuild resolves them
	// again from the new executor's configuration and capabilities.
	next.HWAccel, next.EncoderHWAccel, next.HWDevice, next.ToneMapFilter = "", "", "", ""

	if err := h.writeMovedNodeRecipe(ctx, upstreamSessionID, nodeURL, next); err != nil {
		return "", err
	}
	if err := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		if current.Recipe == nil || !sameCompatNode(current.Recipe.TranscodeNodeURL, deadNodeURL) {
			return errCompatMoveNoSession
		}
		recipe := next
		current.Recipe = &recipe
		committed := assignment
		current.RoutingAssignment = &committed
		return nil
	}); err != nil {
		return "", fmt.Errorf("record moved transcode: %w",
			errors.Join(err, h.undoCompatTranscodeMove(ctx, playSessionID, upstreamSessionID, previous, next, previousAssignment)))
	}
	if err := h.sessionMgr.SetTranscodeNodeURL(upstreamSessionID, nodeURL); err != nil {
		slog.WarnContext(ctx, "record moved transcode on the upstream session", "component", "jellycompat",
			"error", logredact.SanitizeText(err.Error()), "playback_session_id", upstreamSessionID)
	}
	if setter, ok := h.sessionMgr.(nodeRoutingAssignmentSetter); ok {
		if err := setter.SetNodeRoutingAssignment(upstreamSessionID, assignment); err != nil {
			slog.WarnContext(ctx, "record moved transcode route on the upstream session", "component", "jellycompat",
				"error", logredact.SanitizeText(err.Error()), "playback_session_id", upstreamSessionID)
		}
	}
	h.retireCompatTranscodeOnDeadNode(ctx, playSessionID, upstreamSessionID, deadNodeURL, nodeURL)
	return nodeURL, nil
}

// writeMovedNodeRecipe points the transport's node recipe record at nodeURL,
// or deletes it for a move to this process.
func (h *PlaybackHandler) writeMovedNodeRecipe(ctx context.Context, upstreamSessionID, nodeURL string, next playback.RecipeCard) error {
	if h.RecipeNodeStore == nil {
		if nodeURL != "" {
			return errors.New("no node recipe store to move the transcode with")
		}
		return nil
	}
	storeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var err error
	if nodeURL != "" {
		err = h.RecipeNodeStore.Put(storeCtx, upstreamSessionID, next)
	} else {
		// A local transcode never reads the record, and a stale one naming
		// the dead node must not let that node rebuild the transport if it
		// comes back.
		err = h.RecipeNodeStore.Delete(storeCtx, upstreamSessionID)
	}
	if err != nil {
		return fmt.Errorf("rewrite node transcode recipe: %w", err)
	}
	return nil
}

// undoCompatTranscodeMove restores the node recipe record and the compat
// session after the compat store refused the move, and returns what it could
// not restore.
func (h *PlaybackHandler) undoCompatTranscodeMove(
	ctx context.Context,
	playSessionID, upstreamSessionID string,
	previous, next playback.RecipeCard,
	previousAssignment *playback.NodeRoutingAssignment,
) error {
	var err error
	if h.RecipeNodeStore != nil {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if restoreErr := h.RecipeNodeStore.Put(restoreCtx, upstreamSessionID, previous); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore node transcode recipe: %w", restoreErr))
		}
		cancel()
	}
	// A durable store can fail its database write after it already applied
	// the move to its live cache. Undo exactly that move, so the compat
	// session names the dead node again, like the record and the upstream
	// session, and a later move request is not ignored.
	if restoreErr := h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		if current.Recipe != nil && reflect.DeepEqual(*current.Recipe, next) {
			restored := previous
			current.Recipe = &restored
			if previousAssignment != nil {
				assignment := *previousAssignment
				current.RoutingAssignment = &assignment
			}
		}
		return nil
	}); restoreErr != nil {
		err = errors.Join(err, fmt.Errorf("restore compat transcode route: %w", restoreErr))
	}
	return err
}

// retireCompatTranscodeOnDeadNode asks the node the transport left to drop it,
// in case that node is only unreachable from the relay and still runs FFmpeg.
// The node keeps the transport's node recipe record when it names another node
// (noderecipe.Store.DeleteUnlessMoved).
//
// A node that cannot tell (an older binary, or a card without node row ids)
// deletes the record anyway. A remote move therefore writes the record again
// after a successful stop; the proxy keeps polling through the short gap, and the new
// node's rebuild retries on the client's next request. The rewrite uses the
// session's recipe as it is after the stop, since an audio or subtitle switch
// on the new node may have replaced the moved one in the meantime.
func (h *PlaybackHandler) retireCompatTranscodeOnDeadNode(
	ctx context.Context,
	playSessionID, upstreamSessionID, deadNodeURL, nodeURL string,
) {
	detached := context.WithoutCancel(ctx)
	go func() {
		stopCtx, cancel := context.WithTimeout(detached, compatTranscodeMoveTimeout)
		defer cancel()
		if err := h.tm.CancelRemoteTranscode(stopCtx, upstreamSessionID, deadNodeURL); err != nil {
			// Expected when the node is down. When it is only unreachable from
			// here, its idle reaper ends the transcode.
			slog.InfoContext(detached, "could not stop the transcode on the node it moved off", "component", "jellycompat",
				"node", logredact.SanitizeURL(deadNodeURL), "playback_session_id", upstreamSessionID,
				"error", logredact.SanitizeText(err.Error()))
			return
		}
		if nodeURL == "" || h.RecipeNodeStore == nil {
			return
		}
		unlock := h.tm.LockSessionLifecycle(upstreamSessionID)
		defer unlock()
		current, ok := h.playbackStore.Get(playSessionID)
		if !ok || current.UpstreamSessionID != upstreamSessionID || current.Recipe == nil ||
			!sameCompatNode(current.Recipe.TranscodeNodeURL, nodeURL) {
			return
		}
		if err := h.RecipeNodeStore.Put(stopCtx, upstreamSessionID, *current.Recipe); err != nil {
			slog.WarnContext(detached, "rewrite moved transcode recipe after stopping the old node", "component", "jellycompat",
				"error", logredact.SanitizeText(err.Error()), "playback_session_id", upstreamSessionID)
		}
	}()
}

// HandleTranscodeMoveRequest answers a proxy that cannot reach the transcode
// node of a transport this process owns. Events for transports owned by
// another API server, or by the native API, are ignored.
func (h *PlaybackHandler) HandleTranscodeMoveRequest(ctx context.Context, event cache.Event) {
	if h == nil || event.Type != cache.EventTranscodeMoveRequested || h.playbackStore == nil {
		return
	}
	request, ok := noderecipe.DecodeMoveRequest(event.Payload)
	if !ok {
		return
	}
	playSession, found := h.playbackStore.FindByUpstreamSessionID(request.TransportID)
	if !found || playSession.Recipe == nil || !sameCompatNode(playSession.Recipe.TranscodeNodeURL, request.NodeURL) {
		return
	}
	go func() {
		if _, err := h.moveCompatTranscodeOffNode(ctx, request.TransportID, request.NodeURL); err != nil {
			slog.WarnContext(ctx, "could not move a Jellyfin-compatible transcode a proxy could not reach", "component", "jellycompat",
				"node", logredact.SanitizeURL(request.NodeURL), "playback_session_id", request.TransportID,
				"error", logredact.SanitizeText(err.Error()))
		}
	}()
}

func sameCompatNode(a, b string) bool {
	return nodepool.NormalizeNodeURL(a) == nodepool.NormalizeNodeURL(b)
}

// subscribeTranscodeMoveRequests lets proxies ask this process to move a
// transcode it owns off a node they cannot reach.
func subscribeTranscodeMoveRequests(deps Dependencies, handler *PlaybackHandler) {
	if deps.EventBus == nil || handler == nil {
		return
	}
	ctx := deps.AppContext
	if ctx == nil {
		ctx = context.Background()
	}
	if err := deps.EventBus.Subscribe(ctx, cache.ChannelTranscodeMove, func(event cache.Event) {
		handler.HandleTranscodeMoveRequest(ctx, event)
	}); err != nil {
		slog.WarnContext(ctx, "subscribe to transcode move requests", "component", "jellycompat",
			"error", logredact.SanitizeText(err.Error()))
	}
}

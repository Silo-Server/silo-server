package handlers

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// routeNodeProbeTimeoutV3 bounds how long a replan waits to learn whether the
// session's current nodes still answer. A refused connection returns at once;
// the bound only matters for a node that stopped answering without closing
// its port.
const routeNodeProbeTimeoutV3 = 2 * time.Second

type unreachableRouteNodesKeyV3 struct{}

// withUnreachableRouteNodesV3 records nodes this request found unreachable, by
// internal URL, so route resolution keeps them out of the selection. The pool's
// health flag only changes at the next sweep, up to 30 seconds after the node
// went away, and until then it still lists the node as healthy.
func withUnreachableRouteNodesV3(ctx context.Context, urls map[string]struct{}) context.Context {
	if len(urls) == 0 {
		return ctx
	}
	return context.WithValue(ctx, unreachableRouteNodesKeyV3{}, urls)
}

func unreachableRouteNodesV3(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	urls, _ := ctx.Value(unreachableRouteNodesKeyV3{}).(map[string]struct{})
	return urls
}

// withoutUnreachableRouteNodesV3 drops the request's unreachable nodes from a
// list of node URLs, so capability reads don't wait on a node that cannot be
// selected.
func withoutUnreachableRouteNodesV3(ctx context.Context, nodeURLs []string) []string {
	unreachable := unreachableRouteNodesV3(ctx)
	if len(unreachable) == 0 {
		return nodeURLs
	}
	return slices.DeleteFunc(slices.Clone(nodeURLs), func(nodeURL string) bool {
		_, skip := unreachable[nodeURL]
		return skip
	})
}

// excludeUnreachableRouteNodesV3 narrows a node predicate by the request's
// unreachable nodes. A nil base accepts every node, as it does for the resolver.
func excludeUnreachableRouteNodesV3(ctx context.Context, base func(*nodepool.Node) bool) func(*nodepool.Node) bool {
	unreachable := unreachableRouteNodesV3(ctx)
	if len(unreachable) == 0 {
		return base
	}
	return func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		if _, skip := unreachable[node.URL]; skip {
			return false
		}
		return base == nil || base(node)
	}
}

// unreachableSessionNodesV3 probes the transcode node and the egress proxy the
// session currently uses and returns the ones that do not answer. A replan is
// how a client recovers from a dead route, so it cannot take the pool's word
// that the route it is leaving still works.
func (h *PlaybackHandler) unreachableSessionNodesV3(ctx context.Context, session *playback.Session) map[string]struct{} {
	if h == nil || session == nil {
		return nil
	}
	var nodeURLs []string
	if session.TranscodeNodeURL != "" {
		nodeURLs = append(nodeURLs, session.TranscodeNodeURL)
	}
	if noderouting.Egress(session.RoutingEgress) == noderouting.EgressProxy && session.RoutingEgressNodeURL != "" &&
		session.RoutingEgressNodeURL != session.TranscodeNodeURL {
		nodeURLs = append(nodeURLs, session.RoutingEgressNodeURL)
	}
	if len(nodeURLs) == 0 {
		return nil
	}
	probe := h.v3RouteNodeProbe
	if probe == nil {
		probe = probeRouteNodeV3
	}
	probeCtx, cancel := context.WithTimeout(ctx, routeNodeProbeTimeoutV3)
	defer cancel()
	answered := make([]bool, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answered[i] = probe(probeCtx, nodeURL)
		}()
	}
	wg.Wait()
	var unreachable map[string]struct{}
	for i, nodeURL := range nodeURLs {
		if answered[i] {
			continue
		}
		if unreachable == nil {
			unreachable = make(map[string]struct{}, len(nodeURLs))
		}
		unreachable[nodeURL] = struct{}{}
		slog.WarnContext(ctx, "protocol v3 replan found the session's node unreachable",
			logComponentKey, playbackLogValueV3,
			"playback_session_id", session.ID,
			"node", logredact.SanitizeURL(nodeURL),
		)
	}
	return unreachable
}

func probeRouteNodeV3(ctx context.Context, nodeURL string) bool {
	healthy, _, _, _, _, _ := nodepool.CheckNode(ctx, &nodepool.Node{URL: nodeURL})
	return healthy
}

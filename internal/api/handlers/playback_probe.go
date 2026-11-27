package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// PlaybackProbeRedirect chooses client egress with the playback routing policy.
// The discovery URL stays stable; each download gets a fresh short-lived token.
func (h *PlaybackHandler) PlaybackProbeRedirect(ctx context.Context, user int, size int) (string, error) {
	path := netaccess.PathFromContext(ctx)
	servesProbes, someLack := h.proxyTransportFeatureEligibilityV3(ctx, playback.TransportFeatureBandwidthProbeV1)
	planner := noderouting.AdaptSessionPlanner(h.NodePlanner)
	id := "probe-" + uuid.NewString()
	if releaser, ok := planner.(interface{ ReleaseSession(string) }); ok {
		defer releaser.ReleaseSession(id)
	}
	resolve := func(eligible func(*nodepool.Node) bool) (noderouting.Decision, error) {
		return noderouting.Resolve(planner, noderouting.ResolveRequest{Request: noderouting.Request{
			Workload: noderouting.WorkloadDirectPlay, Delivery: noderouting.DeliveryDirect, Policy: h.playbackRoutingPolicyForContextV3(ctx), ProxyAllowed: h.JWTSecret != "",
		}, SessionID: id, ProxyEligible: nodepool.ClientReachableVia(path, eligible)})
	}
	decision, err := resolve(servesProbes)
	if err != nil || !decision.Selected() {
		return "", playbackOperationError(http.StatusServiceUnavailable, "dependency_unavailable", "No media path is available for a connection speed test")
	}
	if decision.Plan.ProxyNode == nil {
		// Do not fall back to API measurement merely because the media proxies
		// predate probes. That would measure a path playback will not use.
		if someLack {
			older, err := resolve(func(n *nodepool.Node) bool { return !servesProbes(n) })
			if err == nil && older.Plan.ProxyNode != nil {
				return "", playbackOperationError(http.StatusServiceUnavailable, "dependency_unavailable", "The selected media proxy does not support connection speed tests")
			}
		}
		return "", nil
	}
	probeClaims := streamtoken.Claims{PlayMethod: streamtoken.PlayMethodBandwidthProbe, UserID: user}
	if authClaims := apimw.GetClaims(ctx); authClaims != nil && authClaims.IsDemo() {
		probeClaims.DemoAuthSessionID = authClaims.SessionID
	}
	token, err := streamtoken.Sign(probeClaims, h.JWTSecret, 2*time.Minute)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/stream/bandwidth-probe/%s?size=%d", decision.Plan.ProxyNode.ClientURLFor(path), token, size), nil
}

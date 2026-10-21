package handlers

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

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
	eligible := nodepool.ClientReachableVia(path, nil)
	planner := noderouting.AdaptSessionPlanner(h.NodePlanner)
	id := "probe-" + uuid.NewString()
	if releaser, ok := planner.(interface{ ReleaseSession(string) }); ok {
		defer releaser.ReleaseSession(id)
	}
	decision, err := noderouting.Resolve(planner, noderouting.ResolveRequest{Request: noderouting.Request{
		Workload: noderouting.WorkloadDirectPlay, Delivery: noderouting.DeliveryDirect, Policy: h.playbackRoutingPolicyForContextV3(ctx), ProxyAllowed: h.JWTSecret != "",
	}, SessionID: id, ProxyEligible: eligible})
	if err != nil || !decision.Selected() {
		return "", playbackOperationError(http.StatusServiceUnavailable, "dependency_unavailable", "No media path is available for a connection speed test")
	}
	if decision.Plan.ProxyNode == nil {
		return "", nil
	}
	// Do not fall back to API measurement merely because a selected media proxy
	// predates probes. That would measure a path playback will not use.
	entry := h.pooledNodeCapabilitiesV3(ctx, []string{decision.Plan.ProxyNode.URL})[decision.Plan.ProxyNode.URL]
	if !slices.Contains(entry.transportFeatures, playback.TransportFeatureBandwidthProbeV1) {
		return "", playbackOperationError(http.StatusServiceUnavailable, "dependency_unavailable", "The selected media proxy does not support connection speed tests")
	}
	token, err := streamtoken.Sign(streamtoken.Claims{PlayMethod: streamtoken.PlayMethodBandwidthProbe, UserID: user}, h.JWTSecret, 2*time.Minute)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/stream/bandwidth-probe/%s?size=%d", decision.Plan.ProxyNode.ClientURLFor(path), token, size), nil
}

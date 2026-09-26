package jellycompat

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const (
	compatLANProxyURL   = "http://10.0.0.9:8083"
	compatTailnetOrigin = "https://proxy-1.tail1234.ts.net"
)

func compatTailnetContext() context.Context {
	return netaccess.WithPath(context.Background(), netaccess.Path{Provider: "tailscale"})
}

// The Jellyfin-compatible redirect is built on the same accessor as the
// native API: a provider-path client is never redirected to a LAN origin.
func TestBuildProxyRedirectURLUsesTheAccessPathOrigin(t *testing.T) {
	h := &PlaybackHandler{JWTSecret: "test-secret"}
	file := &models.MediaFile{FilePath: "/media/movie.mkv"}
	tailnet := netaccess.Path{Provider: "tailscale"}
	lanOnly := &nodepool.Node{ID: 41, URL: compatLANProxyURL}
	connected := &nodepool.Node{ID: 41, URL: compatLANProxyURL, NetworkAccess: netaccess.NodeNetworkAccess{
		"tailscale": {State: netaccess.StateConnected, Origin: compatTailnetOrigin},
	}}

	for _, method := range []string{string(playback.PlayDirect), string(playback.PlayRemux), string(playback.PlayTranscode)} {
		t.Run(method, func(t *testing.T) {
			if got, err := h.buildProxyRedirectURL("play", "upstream", method, file, PlaybackMediaSource{}, nil, time.Time{}, "http://transcode-1", 0, lanOnly, tailnet); err == nil {
				t.Fatalf("LAN-only proxy produced %q for a tailnet client, want an error", got)
			}
			got, err := h.buildProxyRedirectURL("play", "upstream", method, file, PlaybackMediaSource{}, nil, time.Time{}, "http://transcode-1", 0, connected, tailnet)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got, compatTailnetOrigin+"/stream/") || strings.Contains(got, "10.0.0.9") {
				t.Fatalf("redirect = %q, want the tailnet origin and no LAN address", got)
			}

			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			claims, err := streamtoken.Verify(parts[2], h.JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			if claims.RoutingNetworkProvider == nil || *claims.RoutingNetworkProvider != "tailscale" {
				t.Fatalf("provider = %v", claims.RoutingNetworkProvider)
			}
			lan, err := h.buildProxyRedirectURL("play", "upstream", method, file, PlaybackMediaSource{}, nil, time.Time{}, "http://transcode-1", 0, connected, netaccess.Path{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(lan, compatLANProxyURL+"/stream/") {
				t.Fatalf("default-path redirect = %q, want the LAN origin", lan)
			}
		})
	}
}

// Route resolution excludes proxies the client cannot reach before reserving,
// so a tailnet client with only LAN proxies is routed to API egress under the
// default policy, refused under proxy_only, and given the proxy once it has a
// tailnet origin.
func TestResolveCompatIdentityRouteExcludesProxiesUnreachableOnTheAccessPath(t *testing.T) {
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{ID: 41, URL: compatLANProxyURL, Enabled: true, Healthy: true}})
	handler := &PlaybackHandler{JWTSecret: "secret", NodePlanner: nodepool.NewPlanner(proxies, nodepool.NewTranscodePool())}
	policy := config.DefaultPlaybackRoutingPolicy()

	decision := handler.resolveCompatIdentityRouteWithPolicy(compatTailnetContext(), "compat-tailnet-direct", string(playback.PlayDirect), 8_000, false, policy)
	if !decision.Selected() || decision.Shape.Egress != noderouting.EgressAPI || decision.Plan.ProxyNode != nil {
		t.Fatalf("tailnet decision = %#v, want API egress with no proxy", decision)
	}
	lan := handler.resolveCompatIdentityRouteWithPolicy(context.Background(), "compat-lan-direct", string(playback.PlayDirect), 8_000, false, policy)
	if !lan.Selected() || lan.Plan.ProxyNode == nil || lan.Plan.ProxyNode.ID != 41 {
		t.Fatalf("LAN decision = %#v, want the LAN proxy", lan)
	}

	hard := policy
	hard.DirectPlayEgress = config.PlaybackEgressProxyOnly
	refused := handler.resolveCompatIdentityRouteWithPolicy(compatTailnetContext(), "compat-tailnet-proxy-only", string(playback.PlayDirect), 8_000, false, hard)
	if refused.Selected() || refused.Outcome != noderouting.OutcomeCapacityUnavailable {
		t.Fatalf("proxy_only tailnet decision = %#v, want %s", refused, noderouting.OutcomeCapacityUnavailable)
	}

	proxies.SetNodes([]*nodepool.Node{{ID: 41, URL: compatLANProxyURL, Enabled: true, Healthy: true, NetworkAccess: netaccess.NodeNetworkAccess{
		"tailscale": {State: netaccess.StateConnected, Origin: compatTailnetOrigin},
	}}})
	reachable := handler.resolveCompatIdentityRouteWithPolicy(compatTailnetContext(), "compat-tailnet-reachable", string(playback.PlayDirect), 8_000, false, hard)
	if !reachable.Selected() || reachable.Plan.ProxyNode == nil || reachable.Plan.ProxyNode.ClientURLFor(netaccess.Path{Provider: "tailscale"}) != compatTailnetOrigin {
		t.Fatalf("reachable decision = %#v, want the tailnet-connected proxy", reachable)
	}
}

func TestCompatNetworkRouteIsMirroredAndRecoverable(t *testing.T) {
	for _, provider := range []string{"", "tailscale"} {
		manager := playback.NewSessionManager(0, 0)
		session, err := manager.StartSession(7, "profile", 42, playback.PlayDirect, false)
		if err != nil {
			t.Fatal(err)
		}
		store := NewPlaybackSessionStore(0, nil)
		store.Put(PlaybackSession{ID: "play", UpstreamSessionID: session.ID})
		handler := &PlaybackHandler{sessionMgr: manager, playbackStore: store}
		ctx := netaccess.WithPath(t.Context(), netaccess.Path{Provider: provider})
		if err := handler.recordNodeRoutingAssignment(ctx, "play", session.ID, playback.NodeRoutingAssignment{Workload: "remux", Execution: "transcode", ExecutionNodeID: 7, Egress: "proxy", EgressNodeID: 11}); err != nil {
			t.Fatal(err)
		}
		current, err := manager.GetSession(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.RoutingNetworkProvider == nil || *current.RoutingNetworkProvider != provider {
			t.Fatalf("native mirror = %v", current.RoutingNetworkProvider)
		}
		stored, _ := store.Get("play")
		card := handler.upstreamRecipeCard(stored, &Session{StreamAppUserID: 7, ProfileID: "profile"}, PlaybackMediaSource{FileID: 42}, "remux")
		if card.RoutingNetworkProvider == nil || *card.RoutingNetworkProvider != provider || card.RoutingExecutionNodeID != 7 || card.RoutingEgressNodeID != 11 {
			t.Fatalf("recovery route = %#v", card)
		}
	}
}

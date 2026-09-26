package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const (
	testLANProxyURL     = "http://10.0.0.9:8083"
	testTailnetOrigin   = "https://proxy-1.tail1234.ts.net"
	testTailnetProvider = "tailscale"
)

func tailnetRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/", nil).
		WithContext(netaccess.WithPath(context.Background(), netaccess.Path{Provider: testTailnetProvider}))
}

// lanOnlyProxyPlanner is a real planner over one healthy proxy that is only
// reachable on the LAN (no provider report), or, with connected true, one that
// also reports a connected tailnet origin.
func lanOnlyProxyPlanner(connected bool) (*nodepool.Planner, *nodepool.ProxyPool) {
	node := &nodepool.Node{ID: 41, URL: testLANProxyURL, Enabled: true, Healthy: true}
	if connected {
		node.NetworkAccess = netaccess.NodeNetworkAccess{testTailnetProvider: {State: netaccess.StateConnected, Origin: testTailnetOrigin}}
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{node})
	return nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), proxies
}

func assertAPIRelative(t *testing.T, rawURL string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		t.Fatalf("URL %q is not API-relative (err %v)", rawURL, err)
	}
	if strings.Contains(rawURL, "10.0.0.9") {
		t.Fatalf("URL %q leaked the LAN origin", rawURL)
	}
}

// A client that arrived through a network access provider cannot reach a
// proxy's LAN origin. The route resolver must never hand it one: with the only
// proxy unreachable on that path the direct-play route falls back to API
// egress, the URL is API-relative, and no proxy reservation is left behind.
func TestPrepareTransportV3ProviderPathNeverReceivesALANOrigin(t *testing.T) {
	for _, mode := range []struct {
		name string
		mode mediaAuthModeV3
	}{
		{"token mode", mediaAuthModeV3{}},
		{"authorized origins", authorizedOriginsModeV3()},
	} {
		t.Run(mode.name, func(t *testing.T) {
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			planner, proxies := lanOnlyProxyPlanner(false)
			handler.NodePlanner = planner
			grants := &recordingRecipeCardStoreV3{}
			handler.ProxyGrantStore = grants

			transport, transportErr := handler.prepareTransportV3(
				tailnetRequest(t),
				&playback.Session{ID: "session-tailnet-direct", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
				mode.mode)
			if transportErr != nil {
				t.Fatalf("prepare identity transport: %v", transportErr)
			}
			defer transport.rollback()

			assertAPIRelative(t, transport.url)
			if transport.routingEgress != noderouting.EgressAPI || transport.routingEgressID != 0 {
				t.Fatalf("egress = %q on node %d, want API egress", transport.routingEgress, transport.routingEgressID)
			}
			if len(grants.cards) != 0 {
				t.Fatalf("a proxy grant was written for a proxy the client cannot reach: %v", grants.cards)
			}
			// The same pool serves a LAN client with the LAN origin, so the
			// exclusion is a property of the request's path, not of the node.
			lanTransport, lanErr := handler.prepareTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-lan-direct", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
				mode.mode)
			if lanErr != nil {
				t.Fatalf("prepare LAN transport: %v", lanErr)
			}
			defer lanTransport.rollback()
			if !strings.HasPrefix(lanTransport.url, testLANProxyURL+"/") {
				t.Fatalf("LAN client url = %q, want the proxy's LAN origin", lanTransport.url)
			}
			if got := proxies.Nodes()[0].URL; got != testLANProxyURL {
				t.Fatalf("pool node mutated: %q", got)
			}
		})
	}
}

// Once the proxy reports a connected origin on the client's provider, that
// origin — and only that origin — is what the client is handed.
func TestPrepareTransportV3ProviderPathUsesTheProviderOrigin(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner, _ := lanOnlyProxyPlanner(true)
	handler.NodePlanner = planner
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

	for _, mode := range []struct {
		name string
		mode mediaAuthModeV3
		want string
	}{
		{"token mode", mediaAuthModeV3{}, testTailnetOrigin + "/stream/direct/"},
		{"authorized origins", authorizedOriginsModeV3(), testTailnetOrigin + "/stream/v3/session-tailnet-"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			transport, transportErr := handler.prepareTransportV3(
				tailnetRequest(t),
				&playback.Session{ID: "session-tailnet-" + strings.ReplaceAll(mode.name, " ", "-"), UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
				mode.mode)
			if transportErr != nil {
				t.Fatalf("prepare identity transport: %v", transportErr)
			}
			defer transport.rollback()
			if !strings.HasPrefix(transport.url, mode.want) || strings.Contains(transport.url, "10.0.0.9") {
				t.Fatalf("url = %q, want prefix %q and no LAN address", transport.url, mode.want)
			}
			var provider *string
			if mode.mode.headerAuth {
				for _, card := range handler.ProxyGrantStore.(*recordingRecipeCardStoreV3).cards {
					provider = card.RoutingNetworkProvider
				}
			} else {
				parsed, err := url.Parse(transport.url)
				if err != nil {
					t.Fatal(err)
				}
				claims, err := streamtoken.Verify(strings.TrimPrefix(parsed.Path, "/stream/direct/"), handler.JWTSecret)
				if err != nil {
					t.Fatal(err)
				}
				provider = claims.RoutingNetworkProvider
			}
			if provider == nil || *provider != testTailnetProvider {
				t.Fatalf("prepared recipe provider = %v", provider)
			}
			if transport.routingEgress != noderouting.EgressProxy || transport.routingEgressID != 41 {
				t.Fatalf("egress = %q on node %d, want proxy 41", transport.routingEgress, transport.routingEgressID)
			}
		})
	}
}

// proxy_only is a hard policy. A provider-path client with no reachable proxy
// gets the existing capacity-unavailable refusal rather than a LAN URL it
// cannot open or an API fallback the operator forbade.
func TestPrepareTransportV3ProviderPathProxyOnlyRefusesWithoutAReachableProxy(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner, _ := lanOnlyProxyPlanner(false)
	handler.NodePlanner = planner
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.DirectPlayEgress = config.PlaybackEgressProxyOnly

	_, transportErr := handler.prepareTransportWithPolicyV3(
		tailnetRequest(t),
		&playback.Session{ID: "session-tailnet-proxy-only", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
		mediaAuthModeV3{}, policy)
	if transportErr == nil || transportErr.reason != string(noderouting.OutcomeCapacityUnavailable) {
		t.Fatalf("transport error = %+v, want %s", transportErr, noderouting.OutcomeCapacityUnavailable)
	}
}

// The URL builders are the last line: even a caller that forgot the predicate
// cannot mint a LAN URL for a provider-path client.
func TestProxyURLBuildersRefuseALANOnlyProxyOnAProviderPath(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	file := v3HandlerFixtureFile(t)
	lanOnly := &nodepool.Node{ID: 41, URL: testLANProxyURL}
	tailnet := netaccess.Path{Provider: testTailnetProvider}
	session := &playback.Session{ID: "session-builders", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID, PlayMethod: playback.PlayDirect}
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, "", playback.TranscodeOpts{SessionID: session.ID, InputPath: file.FilePath})
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

	if got, byProxy := handler.identityStreamURLV3(session, file, lanOnly, tailnet); byProxy || got != handler.playbackStreamURL(session) {
		t.Fatalf("identityStreamURLV3 = %q (proxy %v), want the API-local route", got, byProxy)
	}
	if got, byProxy, _ := handler.identityGrantStreamURLV3(context.Background(), session, file, lanOnly, tailnet); byProxy || got != handler.playbackStreamURL(session) {
		t.Fatalf("identityGrantStreamURLV3 = %q (proxy %v), want the API-local route", got, byProxy)
	}
	if got := handler.buildProxyManifestURL(card, lanOnly, false, tailnet); !strings.HasPrefix(got, "/playback/transcode/session-builders/master.m3u8") {
		t.Fatalf("buildProxyManifestURL = %q, want the API-local manifest", got)
	}
	if got, byProxy, _ := handler.grantManifestURLV3(context.Background(), card, lanOnly, tailnet); byProxy || got != "/playback/transcode/session-builders/master.m3u8" {
		t.Fatalf("grantManifestURLV3 = %q (proxy %v), want the API-local manifest", got, byProxy)
	}

	connected := &nodepool.Node{ID: 41, URL: testLANProxyURL, NetworkAccess: netaccess.NodeNetworkAccess{testTailnetProvider: {State: netaccess.StateConnected, Origin: testTailnetOrigin}}}
	if got, byProxy := handler.identityStreamURLV3(session, file, connected, tailnet); !byProxy || !strings.HasPrefix(got, testTailnetOrigin+"/stream/direct/") {
		t.Fatalf("identityStreamURLV3 on a connected proxy = %q (proxy %v)", got, byProxy)
	}
	if got := handler.buildProxyManifestURL(card, connected, false, tailnet); !strings.HasPrefix(got, testTailnetOrigin+"/stream/transcode/") {
		t.Fatalf("buildProxyManifestURL on a connected proxy = %q", got)
	}
	// The default path is unchanged by the report: LAN clients keep the LAN URL.
	if got, byProxy := handler.identityStreamURLV3(session, file, connected, netaccess.Path{}); !byProxy || !strings.HasPrefix(got, testLANProxyURL+"/stream/direct/") {
		t.Fatalf("identityStreamURLV3 on the default path = %q (proxy %v)", got, byProxy)
	}
}

func TestV3SessionStateRecordsValidatedNetworkProvider(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	for _, provider := range []string{"", "tailscale"} {
		ctx := netaccess.WithPath(t.Context(), netaccess.Path{Provider: provider})
		state := handler.v3SessionStreamState(ctx, &playback.Session{}, nil, playback.PlannerResultV3{}, preparedTransportV3{}, mediaAuthModeV3{})
		if state.RoutingNetworkProvider == nil || *state.RoutingNetworkProvider != provider {
			t.Fatalf("provider = %v, want %q", state.RoutingNetworkProvider, provider)
		}
	}
}

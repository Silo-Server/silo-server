package auth

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// peerPlugin is a network identity auth plugin: it answers AuthenticatePeer
// from a fixed table of overlay peers and refuses every password.
type peerPlugin struct {
	mu    sync.Mutex
	peers map[string]*pluginv1.AuthenticateResponse
	asked []string
	err   error
}

func (p *peerPlugin) AuthenticatePeer(_ context.Context, req *pluginv1.AuthenticatePeerRequest) (*pluginv1.AuthenticateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, req.GetPeerAddress())
	if p.err != nil {
		return nil, p.err
	}
	if response, ok := p.peers[req.GetPeerAddress()]; ok {
		return response, nil
	}
	return &pluginv1.AuthenticateResponse{Denial: pluginv1.AuthDenial_AUTH_DENIAL_NOT_PERMITTED, DenialDetail: "unknown peer"}, nil
}

func (p *peerPlugin) askedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.asked)
}

func (p *peerPlugin) Authenticate(context.Context, *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	return &pluginv1.AuthenticateResponse{Denial: pluginv1.AuthDenial_AUTH_DENIAL_INVALID_CREDENTIALS}, nil
}

func (p *peerPlugin) InitAuthorize(context.Context, *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "network only")
}

func (p *peerPlugin) ExchangeCode(context.Context, *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "network only")
}

// overlayContext is a request context that came through installationID's
// overlay listener from peer.
func overlayContext(ctx context.Context, installationID int, peer string) context.Context {
	return netaccess.WithPath(ctx, netaccess.Path{Provider: "tailscale", InstallationID: installationID, Peer: netip.MustParseAddr(peer)})
}

func networkProviderSource(installationID int, provider *PluginProvider) staticProviderSource {
	return staticProviderSource{{
		Info:     LoginProviderInfo{ID: PluginProviderID(installationID, "tailscale"), DisplayName: "Tailscale", Mode: ProviderModeNetwork, InstallationID: installationID},
		Provider: provider,
	}}
}

func TestProviderModeForAuthModesNetwork(t *testing.T) {
	for _, modes := range [][]string{{"network"}, {"network", "password"}, {"oauth2", "network"}} {
		if got := ProviderModeForAuthModes(modes); got != ProviderModeNetwork {
			t.Fatalf("%v -> %q, want network", modes, got)
		}
	}
	if got := ProviderModeForAuthModes([]string{"oauth2"}); got != ProviderModeOAuth {
		t.Fatalf("oauth2 -> %q", got)
	}
}

func TestRequestPeer(t *testing.T) {
	ctx := t.Context()
	if peer, err := requestPeer(overlayContext(ctx, 5, "100.64.0.7"), 5); err != nil || peer != netip.MustParseAddr("100.64.0.7") {
		t.Fatalf("overlay peer = %v, %v", peer, err)
	}
	for name, c := range map[string]context.Context{
		"default path":         ctx,
		"another installation": overlayContext(ctx, 6, "100.64.0.7"),
		"overlay without peer": netaccess.WithPath(ctx, netaccess.Path{Provider: "tailscale", InstallationID: 5}),
		"provider name only":   netaccess.WithPath(ctx, netaccess.Path{Provider: "tailscale", Peer: netip.MustParseAddr("100.64.0.7")}),
	} {
		if _, err := requestPeer(c, 5); !errors.Is(err, ErrNetworkIdentityRequired) {
			t.Fatalf("%s: err = %v, want ErrNetworkIdentityRequired", name, err)
		}
	}
	if _, err := requestPeer(overlayContext(ctx, 5, "100.64.0.7"), 0); !errors.Is(err, ErrNetworkIdentityRequired) {
		t.Fatalf("installation 0: err = %v", err)
	}
}

// Discovery lists a network provider only to a request its overlay proxied,
// from a peer the plugin vouches for, with that person's name; it never makes
// it the default and never counts it as taking a password.
func TestDiscoverProvidersOffersNetworkProviderToOverlayPeers(t *testing.T) {
	plugin := &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{
		"100.64.0.7": {ExternalSubject: "controlplane.tailscale.com|42", DisplayName: "Alice Example", Username: "alice@example.test"},
	}}
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
		nil, nil, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.SetPluginProviderSource(networkProviderSource(5, provider))
	ctx := t.Context()

	discover := func(ctx context.Context) ProviderDiscovery {
		t.Helper()
		discovery, err := svc.DiscoverProviders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return discovery
	}
	got := discover(overlayContext(ctx, 5, "100.64.0.7"))
	if len(got.Providers) != 1 || got.PasswordLogin {
		t.Fatalf("discovery = %+v", got)
	}
	listed := got.Providers[0]
	if listed.Mode != ProviderModeNetwork || listed.Default || listed.NetworkIdentity == nil ||
		*listed.NetworkIdentity != (NetworkIdentityPreview{DisplayName: "Alice Example", Username: "alice@example.test"}) {
		t.Fatalf("network provider = %+v", listed)
	}
	// The answer is reused for the same peer.
	discover(overlayContext(ctx, 5, "100.64.0.7"))
	if plugin.askedCount() != 1 {
		t.Fatalf("plugin asked %d times, want 1", plugin.askedCount())
	}

	for name, c := range map[string]context.Context{
		"default path":         ctx,
		"another installation": overlayContext(ctx, 6, "100.64.0.7"),
		"refused peer":         overlayContext(ctx, 5, "100.64.0.8"),
	} {
		if got := discover(c); len(got.Providers) != 0 {
			t.Fatalf("%s: discovery = %+v, want no providers", name, got)
		}
	}
	// A refusal is cached like an identity.
	asked := plugin.askedCount()
	discover(overlayContext(ctx, 5, "100.64.0.8"))
	if plugin.askedCount() != asked {
		t.Fatal("a refused peer was asked about again within the cache window")
	}
	// A plugin that cannot answer hides the provider and is asked again.
	plugin.mu.Lock()
	plugin.err = status.Error(codes.Unavailable, "overlay down")
	plugin.mu.Unlock()
	for range 2 {
		if got := discover(overlayContext(ctx, 5, "100.64.0.9")); len(got.Providers) != 0 {
			t.Fatalf("unavailable plugin: discovery = %+v", got)
		}
	}
	if plugin.askedCount() != asked+2 {
		t.Fatalf("plugin asked %d times, want %d: an unavailable answer must not be cached", plugin.askedCount(), asked+2)
	}
}

// A plugin client without NetworkIdentityAuth cannot vouch for anyone.
func TestNetworkSignInWithoutPeerServiceIsUnavailable(t *testing.T) {
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
		nil, nil, func(context.Context) (pluginAuthClient, error) { return &directoryPlugin{}, nil })
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.SetPluginProviderSource(networkProviderSource(5, provider))
	if _, err := svc.NetworkSignIn(overlayContext(t.Context(), 5, "100.64.0.7"), NetworkSignInInput{InstallationID: 5}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if _, err := svc.NetworkSignIn(overlayContext(t.Context(), 5, "100.64.0.7"), NetworkSignInInput{InstallationID: 6}); !errors.Is(err, ErrUnknownAuthInstallation) {
		t.Fatalf("unknown installation = %v", err)
	}
}

// A network provider never takes a password: a login naming it, or one
// routed without a provider, never reaches it.
func TestPasswordLoginNeverReachesNetworkProvider(t *testing.T) {
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
		nil, nil, func(context.Context) (pluginAuthClient, error) {
			t.Fatal("the network plugin was loaded for a password login")
			return nil, nil
		})
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.SetPluginProviderSource(networkProviderSource(5, provider))
	if p := svc.passwordProviderByID(PluginProviderID(5, "tailscale")); p != nil {
		t.Fatalf("passwordProviderByID = %v, want nil", p)
	}
	if routed, err := svc.routePasswordLogin(t.Context(), "alice"); err != nil || routed != LocalProviderID {
		t.Fatalf("routePasswordLogin = %q, %v, want local", routed, err)
	}
	if _, _, err := svc.LoginWithProvider(t.Context(), PluginProviderID(5, "tailscale"), "alice", "pw", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login naming the network provider = %v", err)
	}
}

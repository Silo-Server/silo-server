package auth

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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
	// deadline is the context deadline of the latest call; zero without one.
	deadline time.Time
	// entered, when set, receives each call, which then waits for release.
	entered chan struct{}
	release chan struct{}
}

func (p *peerPlugin) AuthenticatePeer(ctx context.Context, req *pluginv1.AuthenticatePeerRequest) (*pluginv1.AuthenticateResponse, error) {
	if p.entered != nil {
		p.entered <- struct{}{}
		<-p.release
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, req.GetPeerAddress())
	p.deadline, _ = ctx.Deadline()
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
	// A registry rebuild (a binding or plugin configuration change) builds a
	// new provider, whose answers are not taken from the old one's cache.
	rebuilt := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
		nil, nil, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
	svc.SetPluginProviderSource(networkProviderSource(5, rebuilt))
	discover(overlayContext(ctx, 5, "100.64.0.8"))
	if plugin.askedCount() != asked+1 {
		t.Fatal("a rebuilt provider answered from the previous provider's cache")
	}
	asked = plugin.askedCount()
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

// Discovery bounds its lookup well below the client's default, loading the
// plugin client included, so a hung plugin or a stalled installation read
// cannot stall the provider list; sign-in keeps the default.
func TestNetworkPreviewBoundsThePluginCall(t *testing.T) {
	plugin := &peerPlugin{}
	var loadDeadline time.Time
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
		nil, nil, func(ctx context.Context) (pluginAuthClient, error) {
			loadDeadline, _ = ctx.Deadline()
			return plugin, nil
		})
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	start := time.Now()
	svc.networkPreview(overlayContext(t.Context(), 5, "100.64.0.7"), provider)
	plugin.mu.Lock()
	deadline := plugin.deadline
	plugin.mu.Unlock()
	for name, got := range map[string]time.Time{"plugin load": loadDeadline, "plugin call": deadline} {
		if got.IsZero() || got.After(start.Add(networkPreviewTimeout+time.Second)) {
			t.Fatalf("discovery %s deadline = %v, want within %v of %v", name, got, networkPreviewTimeout, start)
		}
	}
}

// Concurrent misses for one peer share one plugin call. Every caller joins
// the shared lookup while the first call is still in the plugin, so none can
// pass by finding its answer cached.
func TestNetworkPreviewSharesConcurrentLookups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const callers = 8
		plugin := &peerPlugin{
			peers: map[string]*pluginv1.AuthenticateResponse{
				"100.64.0.7": {ExternalSubject: "controlplane.tailscale.com|42", DisplayName: "Alice Example"},
			},
			entered: make(chan struct{}, callers),
			release: make(chan struct{}),
		}
		provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
			nil, nil, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
		svc := NewService(nil, nil, nil, nil, nil, nil, nil)
		ctx := overlayContext(t.Context(), 5, "100.64.0.7")
		previews := make(chan *NetworkIdentityPreview, callers)
		for range callers {
			go func() { previews <- svc.networkPreview(ctx, provider) }()
		}
		// Every caller has reached its blocked lookup before any answer can
		// enter the cache. Count actual plugin entries, then release them all.
		synctest.Wait()
		entered := len(plugin.entered)
		close(plugin.release)
		for range callers {
			if preview := <-previews; preview == nil || preview.DisplayName != "Alice Example" {
				t.Errorf("preview = %+v", preview)
			}
		}
		if entered != 1 || plugin.askedCount() != 1 {
			t.Fatalf("plugin entered %d times and answered %d times, want 1", entered, plugin.askedCount())
		}
	})
}

// A cache miss drops expired answers, including those of a provider instance
// a registry rebuild replaced.
func TestNetworkPreviewDropsExpiredAnswers(t *testing.T) {
	plugin := &peerPlugin{}
	newProvider := func() *PluginProvider {
		return NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
			nil, nil, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
	}
	replaced, current := newProvider(), newProvider()
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	stale := networkPreviewKey{provider: replaced, peer: netip.MustParseAddr("100.64.0.7")}
	fresh := networkPreviewKey{provider: replaced, peer: netip.MustParseAddr("100.64.0.8")}
	svc.previews.entries = map[networkPreviewKey]networkPreviewEntry{
		stale: {expires: time.Now().Add(-time.Second)},
		fresh: {expires: time.Now().Add(time.Minute)},
	}
	svc.networkPreview(overlayContext(t.Context(), 5, "100.64.0.9"), current)
	svc.previews.mu.Lock()
	defer svc.previews.mu.Unlock()
	if _, ok := svc.previews.entries[stale]; ok {
		t.Fatal("an expired answer survived a cache miss")
	}
	if _, ok := svc.previews.entries[fresh]; !ok {
		t.Fatal("a live answer was dropped")
	}
	if len(svc.previews.entries) != 2 {
		t.Fatalf("cache = %v, want the live answer and the new one", svc.previews.entries)
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

// A plugin that answers AuthenticatePeer with neither a response nor an
// error vouches for nobody; sign-in refuses it instead of panicking.
func TestNetworkSignInRefusesEmptyPeerAnswer(t *testing.T) {
	plugin := &peerPlugin{peers: map[string]*pluginv1.AuthenticateResponse{"100.64.0.7": nil}}
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "tailscale"},
		nil, nil, func(context.Context) (pluginAuthClient, error) { return plugin, nil })
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.SetPluginProviderSource(networkProviderSource(5, provider))
	if _, err := svc.NetworkSignIn(overlayContext(t.Context(), 5, "100.64.0.7"), NetworkSignInInput{InstallationID: 5}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
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

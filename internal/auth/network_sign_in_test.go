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

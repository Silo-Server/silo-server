package pluginhost_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type fakeInstanceState struct {
	values  map[string][]byte
	scopeOf map[string]int
	err     error
}

func (f *fakeInstanceState) ReadInstanceState(_ context.Context, installationID int, key string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	v, ok := f.values[key]
	if ok && f.scopeOf[key] != installationID {
		return nil, false, nil
	}
	return v, ok, nil
}

func (f *fakeInstanceState) WriteInstanceState(_ context.Context, installationID int, key string, value []byte) error {
	if f.err != nil {
		return f.err
	}
	if f.values == nil {
		f.values = map[string][]byte{}
		f.scopeOf = map[string]int{}
	}
	f.values[key] = value
	f.scopeOf[key] = installationID
	return nil
}

func apiHostInfo(listeners ...pluginhost.HostListener) pluginhost.HostInfoFunc {
	return func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{
			PublicBaseURL:       "https://silo.example/",
			PluginContentPrefix: "/api/v2/plugin-content",
			Role:                pluginhost.HostRoleAPI,
			Name:                "Living Room",
			Listeners:           listeners,
		}, nil
	}
}

func TestRuntimeHostServer_GetHostInfo_ReportsListenersAndIngressToken(t *testing.T) {
	broker := netaccess.NewBroker()
	token, err := broker.Issue(9, "tailscale")
	if err != nil {
		t.Fatal(err)
	}
	srv := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{
		HostInfo: apiHostInfo(
			pluginhost.HostListener{Name: pluginhost.ListenerAPI, Address: "127.0.0.1:8080", DefaultPort: pluginhost.DefaultPortAPI},
			pluginhost.HostListener{Name: pluginhost.ListenerJellyfin, Address: "127.0.0.1:8096", DefaultPort: pluginhost.DefaultPortJellyfin},
			pluginhost.HostListener{Name: pluginhost.ListenerABS, Address: "127.0.0.1:13378", DefaultPort: pluginhost.DefaultPortABS},
		),
		NetworkAccess:         broker,
		Logger:                hclog.NewNullLogger(),
		PluginID:              "silo.tailscale",
		InstallationID:        9,
		NetworkAccessProvider: "tailscale",
	})

	resp, err := srv.GetHostInfo(context.Background(), &pluginv1.GetHostInfoRequest{})
	if err != nil {
		t.Fatalf("GetHostInfo: %v", err)
	}
	if resp.GetHostRole() != "api" || resp.GetHostName() != "Living Room" || resp.GetNodeId() != 0 {
		t.Fatalf("role/name/node = %q/%q/%d", resp.GetHostRole(), resp.GetHostName(), resp.GetNodeId())
	}
	if resp.GetPublicBaseUrl() != "https://silo.example" {
		t.Fatalf("public_base_url = %q", resp.GetPublicBaseUrl())
	}
	if resp.GetInternalBaseUrl() != "http://127.0.0.1:8080" {
		t.Fatalf("internal_base_url = %q", resp.GetInternalBaseUrl())
	}
	if resp.GetPluginProxyBaseUrl() != "https://silo.example/api/v2/plugin-content/plugins/9" {
		t.Fatalf("plugin_proxy_base_url = %q", resp.GetPluginProxyBaseUrl())
	}
	if resp.GetIngressToken() != token {
		t.Fatalf("ingress_token = %q, want the registry's token", resp.GetIngressToken())
	}
	want := []struct {
		name, address string
		port          int32
	}{{"api", "127.0.0.1:8080", 443}, {"jellyfin", "127.0.0.1:8096", 8096}, {"abs", "127.0.0.1:13378", 13378}}
	if len(resp.GetListeners()) != len(want) {
		t.Fatalf("listeners = %v", resp.GetListeners())
	}
	for i, w := range want {
		got := resp.GetListeners()[i]
		if got.GetName() != w.name || got.GetAddress() != w.address || got.GetDefaultPort() != w.port {
			t.Fatalf("listener %d = %v, want %+v", i, got, w)
		}
	}
	if ingress, ok := broker.Registry.Lookup(resp.GetIngressToken()); !ok || ingress.Provider != "tailscale" || ingress.InstallationID != 9 {
		t.Fatalf("token does not resolve: %+v %v", ingress, ok)
	}
}

func TestRuntimeHostServer_GetHostInfo_NoTokenForNonProvidersOrTestRuns(t *testing.T) {
	broker := netaccess.NewBroker()
	if _, err := broker.Issue(9, "tailscale"); err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string]pluginhost.RuntimeHostOptions{
		"non-provider":    {HostInfo: apiHostInfo(), NetworkAccess: broker, InstallationID: 9},
		"config test-run": {HostInfo: apiHostInfo(), NetworkAccess: broker, InstallationID: -1, NetworkAccessProvider: "tailscale"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := pluginhost.NewRuntimeHostServerWithOptions(opts).GetHostInfo(context.Background(), &pluginv1.GetHostInfoRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetIngressToken() != "" {
				t.Fatalf("ingress token leaked: %q", resp.GetIngressToken())
			}
		})
	}
	_, err := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{}).GetHostInfo(context.Background(), &pluginv1.GetHostInfoRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("no host info configured: %v", err)
	}
}

func TestRuntimeHostServer_InstanceState_RoundTripAndLimits(t *testing.T) {
	state := &fakeInstanceState{}
	srv := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{InstanceState: state, InstallationID: 4})
	ctx := context.Background()

	read, err := srv.ReadInstanceState(ctx, &pluginv1.ReadInstanceStateRequest{Key: "_machinekey"})
	if err != nil || read.GetFound() {
		t.Fatalf("read before write = %v, %v", read, err)
	}
	if _, err := srv.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: "_machinekey", Value: []byte("k")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if state.scopeOf["_machinekey"] != 4 {
		t.Fatalf("store called with installation %d, want 4", state.scopeOf["_machinekey"])
	}
	read, err = srv.ReadInstanceState(ctx, &pluginv1.ReadInstanceStateRequest{Key: "_machinekey"})
	if err != nil || !read.GetFound() || string(read.GetValue()) != "k" {
		t.Fatalf("read = %v, %v", read, err)
	}

	longKey := string(make([]byte, pluginhost.InstanceStateMaxKeyBytes+1))
	if _, err := srv.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: longKey, Value: []byte("k")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("long key: %v", err)
	}
	if _, err := srv.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: "big", Value: make([]byte, pluginhost.InstanceStateMaxValueBytes+1)}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("big value: %v", err)
	}
	state.err = pluginhost.ErrInstanceStateTooManyKeys
	if _, err := srv.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: "k", Value: []byte("v")}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("too many keys: %v", err)
	}
	state.err = errors.New("db down")
	if _, err := srv.ReadInstanceState(ctx, &pluginv1.ReadInstanceStateRequest{Key: "k"}); err == nil || status.Code(err) != codes.Unknown {
		t.Fatalf("store error: %v", err)
	}

	testRun := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{InstanceState: &fakeInstanceState{}, InstallationID: -3})
	if _, err := testRun.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: "k", Value: []byte("v")}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("test-run write: %v", err)
	}
	if _, err := testRun.ReadInstanceState(ctx, &pluginv1.ReadInstanceStateRequest{Key: "k"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("test-run read: %v", err)
	}
}

func TestRuntimeHostServer_ReportNetworkAccessStatus(t *testing.T) {
	broker := netaccess.NewBroker()
	token, err := broker.Issue(9, "tailscale")
	if err != nil {
		t.Fatal(err)
	}
	srv := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{
		NetworkAccess: broker, InstallationID: 9, NetworkAccessProvider: "tailscale", IngressToken: token, Logger: hclog.NewNullLogger(),
	})
	ctx := context.Background()
	_, err = srv.ReportNetworkAccessStatus(ctx, &pluginv1.ReportNetworkAccessStatusRequest{Status: &pluginv1.NetworkAccessStatus{
		State: "connected", Hostname: "silo.tail1234.ts.net", Origin: "https://silo.tail1234.ts.net",
		Listeners: []*pluginv1.NetworkAccessListener{{Name: "api", Origin: "https://silo.tail1234.ts.net"}, {Name: "abs", Origin: "https://silo.tail1234.ts.net:13378"}},
		AuthUrl:   "https://login.tailscale.com/a/secret", ProviderVersion: "tsnet 1.0", DesiredConnected: true,
	}})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	got, ok := broker.Status.Get(9)
	if !ok || got.Provider != "tailscale" || !got.Connected() || got.AuthURL != "https://login.tailscale.com/a/secret" || len(got.Listeners) != 2 || !got.DesiredConnected {
		t.Fatalf("cached status = %+v, %v", got, ok)
	}
	origins := broker.Status.ConnectedOrigins()
	if len(origins) != 2 || origins[0] != "https://silo.tail1234.ts.net" || origins[1] != "https://silo.tail1234.ts.net:13378" {
		t.Fatalf("connected origins = %v", origins)
	}

	if _, err := srv.ReportNetworkAccessStatus(ctx, &pluginv1.ReportNetworkAccessStatusRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil status: %v", err)
	}
	// A push from a process whose token was revoked (it was stopped or
	// replaced while the RPC was in flight) is dropped, so it cannot write a
	// stale origin over the replacement's.
	broker.Revoke(9, token)
	if _, err := srv.ReportNetworkAccessStatus(ctx, &pluginv1.ReportNetworkAccessStatusRequest{Status: &pluginv1.NetworkAccessStatus{State: "connected", Origin: "https://stale.tail1234.ts.net"}}); err != nil {
		t.Fatalf("revoked push must be accepted quietly: %v", err)
	}
	if _, ok := broker.Status.Get(9); ok {
		t.Fatal("a revoked process repopulated the status cache")
	}
	replacement, _ := broker.Issue(9, "tailscale")
	broker.Report(netaccess.Status{InstallationID: 9, Provider: "tailscale", State: "connected", Origin: "https://fresh.tail1234.ts.net"})
	if _, err := srv.ReportNetworkAccessStatus(ctx, &pluginv1.ReportNetworkAccessStatusRequest{Status: &pluginv1.NetworkAccessStatus{State: "connected", Origin: "https://stale.tail1234.ts.net"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := broker.Status.Get(9); got.Origin != "https://fresh.tail1234.ts.net" {
		t.Fatalf("old process overwrote the replacement's status: %+v", got)
	}
	_ = replacement

	notProvider := pluginhost.NewRuntimeHostServerWithOptions(pluginhost.RuntimeHostOptions{NetworkAccess: broker, InstallationID: 10})
	if _, err := notProvider.ReportNetworkAccessStatus(ctx, &pluginv1.ReportNetworkAccessStatusRequest{Status: &pluginv1.NetworkAccessStatus{State: "connected"}}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-provider push: %v", err)
	}
	if _, ok := broker.Status.Get(10); ok {
		t.Fatal("non-provider status was cached")
	}
}

func TestNetworkAccessProviderSlug(t *testing.T) {
	manifest := &pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{
		{Type: capability.MetadataProvider, Id: "tmdb"},
		{Type: capability.NetworkAccessProvider, Id: "stub", NetworkAccessProvider: &pluginv1.NetworkAccessProviderDescriptor{Provider: "tailscale"}},
	}}
	if slug, ok := pluginhost.NetworkAccessProviderSlug(manifest); !ok || slug != "tailscale" {
		t.Fatalf("slug = %q, %v", slug, ok)
	}
	manifest.Capabilities[1].NetworkAccessProvider = nil
	if slug, ok := pluginhost.NetworkAccessProviderSlug(manifest); !ok || slug != "stub" {
		t.Fatalf("fallback slug = %q, %v", slug, ok)
	}
	if _, ok := pluginhost.NetworkAccessProviderSlug(&pluginv1.PluginManifest{Capabilities: manifest.Capabilities[:1]}); ok {
		t.Fatal("non-provider manifest reported a slug")
	}
	for in, want := range map[string]string{":8080": "127.0.0.1:8080", "0.0.0.0:8080": "127.0.0.1:8080", "[::]:8080": "127.0.0.1:8080", "10.0.0.5:9000": "10.0.0.5:9000", "": ""} {
		if got := pluginhost.LoopbackDialAddress(in); got != want {
			t.Fatalf("LoopbackDialAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

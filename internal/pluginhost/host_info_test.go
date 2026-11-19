package pluginhost_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type fakeInstanceState struct {
	values   map[string][]byte
	versions map[string]int64
	scopeOf  map[string]int
	err      error
}

func (f *fakeInstanceState) ReadInstanceState(_ context.Context, installationID int, key string) ([]byte, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	v, ok := f.values[key]
	if !ok || f.scopeOf[key] != installationID {
		return nil, 0, nil
	}
	return v, f.versions[key], nil
}

func (f *fakeInstanceState) WriteInstanceState(_ context.Context, installationID int, key string, value []byte, _ *int64) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.values == nil {
		f.values = map[string][]byte{}
		f.versions = map[string]int64{}
		f.scopeOf = map[string]int{}
	}
	f.values[key] = value
	f.versions[key]++
	f.scopeOf[key] = installationID
	return f.versions[key], nil
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

	// Versioned tells the SDK this host honors expected_version, so it is set
	// whether or not the key exists.
	read, err := srv.ReadInstanceState(ctx, &pluginv1.ReadInstanceStateRequest{Key: "_machinekey"})
	if err != nil || read.GetFound() || read.GetVersion() != 0 || !read.GetVersioned() {
		t.Fatalf("read before write = %v, %v", read, err)
	}
	written, err := srv.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: "_machinekey", Value: []byte("k")})
	if err != nil || written.GetVersion() != 1 {
		t.Fatalf("write = %v, %v", written, err)
	}
	if state.scopeOf["_machinekey"] != 4 {
		t.Fatalf("store called with installation %d, want 4", state.scopeOf["_machinekey"])
	}
	read, err = srv.ReadInstanceState(ctx, &pluginv1.ReadInstanceStateRequest{Key: "_machinekey"})
	if err != nil || !read.GetFound() || string(read.GetValue()) != "k" || read.GetVersion() != 1 || !read.GetVersioned() {
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
	state.err = pluginhost.ErrInstanceStateVersionConflict
	if _, err := srv.WriteInstanceState(ctx, &pluginv1.WriteInstanceStateRequest{Key: "k", Value: []byte("v"), ExpectedVersion: proto.Int64(3)}); status.Code(err) != codes.Aborted {
		t.Fatalf("version conflict: %v", err)
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

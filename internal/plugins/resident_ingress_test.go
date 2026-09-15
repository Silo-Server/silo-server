package plugins

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// Starting a network access provider issues its ingress token before the
// plugin can ask for it; stopping the process revokes it and forgets its
// status, so a request still carrying the old token is refused.
func TestHostStartIssuesAndStopRevokesIngressToken(t *testing.T) {
	bin := buildResidentFixture(t)
	manifest, err := LoadManifestFile(InstalledManifestPath(bin))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := protojson.Marshal(manifest)
	live := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(raw, live); err != nil {
		t.Fatal(err)
	}

	broker := netaccess.NewBroker()
	host := pluginhost.NewHost(pluginhost.Config{Logger: hclog.NewNullLogger(), NetworkAccess: broker})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 5, BinaryPath: bin, Manifest: live}); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(func() { _ = host.Stop(5) })

	token, ok := broker.IngressToken(5)
	if !ok || token == "" {
		t.Fatal("no ingress token issued for the running provider")
	}
	if ingress, ok := broker.Registry.Lookup(token); !ok || ingress.Provider != "stub" || ingress.InstallationID != 5 {
		t.Fatalf("token resolves to %+v, %v", ingress, ok)
	}
	broker.ReportFor(5, token, netaccess.Status{InstallationID: 5, Provider: "stub", State: netaccess.StateConnected, Origin: "https://stub.example"})

	if err := host.Stop(5); err != nil {
		t.Fatalf("host.Stop: %v", err)
	}
	if _, ok := broker.Registry.Lookup(token); ok {
		t.Fatal("token still valid after the process stopped")
	}
	if _, ok := broker.Status.Get(5); ok {
		t.Fatal("status survived the process stop")
	}

	// A restart rotates the token.
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 5, BinaryPath: bin, Manifest: live}); err != nil {
		t.Fatalf("host.Start again: %v", err)
	}
	rotated, ok := broker.IngressToken(5)
	if !ok || rotated == token {
		t.Fatalf("token after restart = %q (ok=%v), want a fresh one", rotated, ok)
	}
}

package pluginhost

import (
	"errors"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// makeTestClient constructs a Client with the given declared capabilities.
// It creates a lazy (non-connecting) gRPC ClientConn so that accessors which
// call c.rpc.<Capability>() do not panic — no real connection is made.
func makeTestClient(t *testing.T, capabilities []*pluginv1.CapabilityDescriptor) *Client {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	rpc := sdkruntime.NewClient(conn)
	manifest := &pluginv1.PluginManifest{Capabilities: capabilities}
	return newClient(0, rpc, manifest)
}

func TestClient_ScheduledTask_CapabilityGate(t *testing.T) {
	t.Run("absent capability returns error", func(t *testing.T) {
		c := makeTestClient(t, nil)
		_, err := c.ScheduledTask("missing")
		if err == nil {
			t.Fatal("expected error for missing scheduled_task.v1 capability, got nil")
		}
		if !errors.Is(err, ErrCapabilityNotFound) {
			t.Errorf("expected ErrCapabilityNotFound, got %v", err)
		}
	})

	t.Run("declared capability returns no error", func(t *testing.T) {
		c := makeTestClient(t, []*pluginv1.CapabilityDescriptor{
			{Type: "scheduled_task.v1", Id: "nightly"},
		})
		got, err := c.ScheduledTask("nightly")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil ScheduledTaskClient")
		}
	})

	t.Run("wrong id returns error", func(t *testing.T) {
		c := makeTestClient(t, []*pluginv1.CapabilityDescriptor{
			{Type: "scheduled_task.v1", Id: "nightly"},
		})
		_, err := c.ScheduledTask("weekly")
		if err == nil {
			t.Fatal("expected error for mismatched scheduled_task.v1 capability id, got nil")
		}
		if !errors.Is(err, ErrCapabilityNotFound) {
			t.Errorf("expected ErrCapabilityNotFound, got %v", err)
		}
	})
}

func TestClient_ScanSource_CapabilityGate(t *testing.T) {
	t.Run("absent capability returns error", func(t *testing.T) {
		c := makeTestClient(t, nil)
		_, err := c.ScanSource("missing")
		if err == nil {
			t.Fatal("expected error for missing scan_source.v1 capability, got nil")
		}
		if !errors.Is(err, ErrCapabilityNotFound) {
			t.Errorf("expected ErrCapabilityNotFound, got %v", err)
		}
	})

	t.Run("declared capability returns no error", func(t *testing.T) {
		c := makeTestClient(t, []*pluginv1.CapabilityDescriptor{
			{Type: "scan_source.v1", Id: "arr"},
		})
		got, err := c.ScanSource("arr")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil ScanSourceClient")
		}
	})

	t.Run("wrong id returns error", func(t *testing.T) {
		c := makeTestClient(t, []*pluginv1.CapabilityDescriptor{
			{Type: "scan_source.v1", Id: "arr"},
		})
		_, err := c.ScanSource("inotify")
		if err == nil {
			t.Fatal("expected error for mismatched scan_source.v1 capability id, got nil")
		}
		if !errors.Is(err, ErrCapabilityNotFound) {
			t.Errorf("expected ErrCapabilityNotFound, got %v", err)
		}
	})
}

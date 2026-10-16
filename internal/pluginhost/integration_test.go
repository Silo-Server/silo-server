package pluginhost_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

const (
	// examplePluginPackage is built from the SDK version this module requires.
	examplePluginPackage = "github.com/Silo-Server/silo-plugin-sdk/examples/hello-runtime-host"
	expectedEventName    = "plugin.example.hello-runtime-host.ping"
)

// capturingPublisher implements pluginhost.EventPublisher and buffers envelopes
// in a channel so the test can assert on them.
type capturingPublisher struct {
	ch chan events.Envelope
}

func (c *capturingPublisher) Publish(_ context.Context, env events.Envelope) error {
	select {
	case c.ch <- env:
	default:
	}
	return nil
}

func TestPluginPublishEvent_FlowsToHub(t *testing.T) {
	binPath := buildExamplePlugin(t)

	// Run the built binary with the "manifest" subcommand. The SDK runtime
	// handles this sub-command by printing the manifest as protojson and
	// exiting. This gives us the manifest with the correct checksum baked in
	// (the checksum is SHA-256 of the binary itself, computed at startup).
	manifestBytes, err := exec.Command(binPath, "manifest").Output()
	if err != nil {
		t.Fatalf("get manifest from binary: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(manifestBytes, manifest); err != nil {
		t.Fatalf("parse manifest protojson: %v", err)
	}

	publisher := &capturingPublisher{ch: make(chan events.Envelope, 4)}
	host := pluginhost.NewHost(pluginhost.Config{
		Logger:         hclog.NewNullLogger(),
		EventPublisher: publisher,
		LibraryLister:  nil,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := host.Start(ctx, pluginhost.StartRequest{
		InstallationID: 1,
		BinaryPath:     binPath,
		Manifest:       manifest,
	})
	if err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	defer func() {
		_ = host.Stop(1)
	}()

	// Obtain the ScheduledTask capability client for the "ping" task.
	taskClient, err := client.ScheduledTask("ping")
	if err != nil {
		t.Fatalf("client.ScheduledTask: %v", err)
	}

	// Trigger the task. The plugin calls sdkruntime.Host().PublishEvent("ping", ...)
	// which traverses the broker stream back into our capturingPublisher.
	if _, err := taskClient.Run(ctx, &pluginv1.RunScheduledTaskRequest{TaskKey: "ping"}); err != nil {
		t.Fatalf("taskClient.Run: %v", err)
	}

	select {
	case env := <-publisher.ch:
		if env.Channel != events.ChannelPlugins {
			t.Errorf("channel = %q, want %q", env.Channel, events.ChannelPlugins)
		}
		if env.Event != expectedEventName {
			t.Errorf("event = %q, want %q", env.Event, expectedEventName)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event from plugin")
	}
}

func buildExamplePlugin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "hello-runtime-host")
	cmd := exec.Command("go", "build", "-o", out, examplePluginPackage)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin: %v\n%s", err, b)
	}
	return out
}

package pluginhost_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

const fakeEventsSubscription = "plugin.silo.test.source.ping"

// buildFakeEventsPlugin builds testdata/fakeevents with its manifest.json
// beside the binary, where the plugin reads it, and returns the binary and
// the manifest the binary reports.
func buildFakeEventsPlugin(t *testing.T) (string, *pluginv1.PluginManifest) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeevents")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/fakeevents").CombinedOutput(); err != nil {
		t.Fatalf("build fakeevents: %v\n%s", err, out)
	}
	manifestJSON, err := os.ReadFile(filepath.Join("testdata", "fakeevents", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command(bin, "manifest").Output()
	if err != nil {
		t.Fatalf("read fakeevents manifest: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(raw, manifest); err != nil {
		t.Fatalf("decode fakeevents manifest: %v", err)
	}
	return bin, manifest
}

// dispatchTestStore lists one enabled installation and its capabilities.
type dispatchTestStore struct {
	installation *plugins.Installation
	capabilities []*plugins.Capability
}

func (s *dispatchTestStore) ListEnabled(context.Context) ([]*plugins.Installation, error) {
	return []*plugins.Installation{s.installation}, nil
}

func (s *dispatchTestStore) ListCapabilities(_ context.Context, installationID int) ([]*plugins.Capability, error) {
	if installationID != s.installation.ID {
		return nil, nil
	}
	return s.capabilities, nil
}

// dispatchTestResolver hands the dispatcher the started plugin's client.
type dispatchTestResolver struct {
	installationID int
	client         *pluginhost.Client
}

func (r *dispatchTestResolver) EventConsumerClient(_ context.Context, installationID int, capabilityID string) (*pluginhost.EventConsumerClient, error) {
	if installationID != r.installationID {
		return nil, fmt.Errorf("no client for installation %d", installationID)
	}
	return r.client.EventConsumer(capabilityID)
}

// memoryOutbox is an in-memory outbox that reports each delivery's outcome.
type memoryOutbox struct {
	mu       sync.Mutex
	nextID   int64
	pending  []plugins.EventDelivery
	outcomes chan string
}

func (o *memoryOutbox) Enqueue(_ context.Context, deliveries []plugins.EventDelivery) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, delivery := range deliveries {
		o.nextID++
		delivery.ID = o.nextID
		o.pending = append(o.pending, delivery)
	}
	return nil
}

func (o *memoryOutbox) Claim(_ context.Context, limit int, _ time.Duration) ([]plugins.EventDelivery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := min(limit, len(o.pending))
	claimed := o.pending[:n:n]
	o.pending = o.pending[n:]
	for i := range claimed {
		claimed[i].Attempts++
	}
	return claimed, nil
}

func (o *memoryOutbox) Delete(_ context.Context, id int64, _ int) error {
	o.outcomes <- fmt.Sprintf("delivered %d", id)
	return nil
}

func (o *memoryOutbox) Retry(_ context.Context, id int64, _ int, _ time.Duration, lastError string) error {
	o.outcomes <- fmt.Sprintf("retry %d: %s", id, lastError)
	return nil
}

// TestDispatcherDeliversHubEventToPluginProcess sends a plugin event through
// the hub and the dispatcher to a real plugin process's HandleEvent, and
// checks the delivery is recorded as delivered rather than retried.
func TestDispatcherDeliversHubEventToPluginProcess(t *testing.T) {
	bin, manifest := buildFakeEventsPlugin(t)
	const installationID = 21

	host := pluginhost.NewHost(pluginhost.Config{Logger: hclog.NewNullLogger()})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: installationID, BinaryPath: bin, Manifest: manifest})
	if err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(func() { _ = host.Stop(installationID) })

	records, err := plugins.CapabilityRecordsFromManifest(manifest)
	if err != nil {
		t.Fatalf("CapabilityRecordsFromManifest: %v", err)
	}
	store := &dispatchTestStore{
		installation: &plugins.Installation{ID: installationID, PluginID: manifest.GetPluginId(), Enabled: true},
	}
	for i := range records {
		records[i].InstallationID = installationID
		store.capabilities = append(store.capabilities, &records[i])
	}

	hub := events.NewHub("test", nil)
	outbox := &memoryOutbox{outcomes: make(chan string, 4)}
	dispatcher := plugins.NewEventDispatcherWithTypedResolver(
		&cache.NoopEventBus{},
		hub,
		store,
		&dispatchTestResolver{installationID: installationID, client: client},
		outbox,
		1,
	)
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	if err := hub.Publish(ctx, events.Envelope{
		Channel: events.ChannelPlugins,
		Event:   fakeEventsSubscription,
		Data:    []byte(`{"message":"hello"}`),
	}); err != nil {
		t.Fatalf("hub.Publish: %v", err)
	}

	select {
	case outcome := <-outbox.outcomes:
		if outcome != "delivered 1" {
			t.Fatalf("delivery outcome = %q, want %q", outcome, "delivered 1")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the plugin to handle the event")
	}
}

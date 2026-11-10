package pluginhost_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitUntilStopped(ctx context.Context, t *testing.T, host *pluginhost.Host, installationID int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for host.Running(installationID) {
		select {
		case <-ctx.Done():
			t.Fatalf("installation %d was not stopped for being idle", installationID)
		case <-ticker.C:
		}
	}
}

// Only a process started with StopWhenIdle on a host with an idle period is
// stopped; the stop is not a crash, and the next Start brings it back.
func TestHostStopsIdleProcessAndStartsItAgain(t *testing.T) {
	bin, manifest := buildExitingPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	disabled := pluginhost.NewHost(pluginhost.Config{Logger: hclog.NewNullLogger(), IdleCheckInterval: 10 * time.Millisecond})
	// A graceful stop logs nothing at warn or above; a killed process does.
	var warnings lockedBuffer
	host := pluginhost.NewHost(pluginhost.Config{
		Logger:            hclog.New(&hclog.LoggerOptions{Level: hclog.Warn, Output: &warnings}),
		IdleStopAfter:     200 * time.Millisecond,
		IdleCheckInterval: 10 * time.Millisecond,
	})
	exited := make(chan int, 4)
	host.SetExitHandler(func(id int) { exited <- id })
	t.Cleanup(func() {
		_ = disabled.Shutdown(context.Background())
		_ = host.Shutdown(context.Background())
	})

	// Started first, so each has been idle for longer than installation 3 by
	// the time that one is stopped.
	if _, err := disabled.Start(ctx, pluginhost.StartRequest{InstallationID: 1, BinaryPath: bin, Manifest: manifest, StopWhenIdle: true}); err != nil {
		t.Fatalf("Start on a host with no idle period: %v", err)
	}
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 2, BinaryPath: bin, Manifest: manifest}); err != nil {
		t.Fatalf("Start without StopWhenIdle: %v", err)
	}
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: -4, BinaryPath: bin, Manifest: manifest, StopWhenIdle: true}); err != nil {
		t.Fatalf("Start of a config test run: %v", err)
	}
	client, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 3, BinaryPath: bin, Manifest: manifest, StopWhenIdle: true})
	if err != nil {
		t.Fatalf("Start with StopWhenIdle: %v", err)
	}

	waitUntilStopped(ctx, t, host, 3)
	if _, err := host.Client(3); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("Client after idle stop = %v, want ErrClientNotFound", err)
	}
	if _, err := client.MetadataProvider("exiting"); errors.Is(err, pluginhost.ErrPluginUnhealthy) {
		t.Fatal("idle stop marked the client unhealthy")
	}
	if !disabled.Running(1) {
		t.Fatal("a host with no idle period stopped an idle process")
	}
	if !host.Running(2) {
		t.Fatal("a process started without StopWhenIdle was stopped")
	}
	if !host.Running(-4) {
		t.Fatal("a config test run was stopped for being idle")
	}

	restarted, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 3, BinaryPath: bin, Manifest: manifest, StopWhenIdle: true})
	if err != nil {
		t.Fatalf("Start after idle stop: %v", err)
	}
	if restarted.StartSeq() == client.StartSeq() {
		t.Fatal("Start after idle stop returned the stopped client")
	}
	waitUntilStopped(ctx, t, host, 3)
	// Stop returns once the idle stop has finished, so nothing is logged later.
	if err := host.Stop(3); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("Stop after idle stop = %v, want ErrClientNotFound", err)
	}

	select {
	case id := <-exited:
		t.Fatalf("idle stop reported installation %d to the exit handler", id)
	default:
	}
	if logged := warnings.String(); logged != "" {
		t.Fatalf("idle stops logged at warn or above:\n%s", logged)
	}
}

//go:build linux

package pluginhost_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// pidOf finds the process running bin, which is unique to the test.
func pidOf(t *testing.T, bin string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe")); err == nil && exe == bin {
			return pid
		}
	}
	t.Fatalf("no process runs %s", bin)
	return 0
}

// A process that is frozen when its idle period ends cannot answer go-plugin's
// shutdown request, which has no deadline. The idle stop kills it instead of
// holding the installation's start slot, so Stop returns and the plugin can
// start again.
func TestHostIdleStopKillsFrozenProcess(t *testing.T) {
	bin, manifest := buildExitingPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	host := pluginhost.NewHost(pluginhost.Config{
		Logger:            hclog.NewNullLogger(),
		IdleStopAfter:     100 * time.Millisecond,
		IdleCheckInterval: 10 * time.Millisecond,
		IdleStopTimeout:   200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })

	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 7, BinaryPath: bin, Manifest: manifest, StopWhenIdle: true}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := pidOf(t, bin)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// Without the fix the frozen process outlives the test; don't leave it.
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	waitUntilStopped(ctx, t, host, 7)
	stopped := make(chan error, 1)
	go func() { stopped <- host.Stop(7) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, pluginhost.ErrClientNotFound) {
			t.Fatalf("Stop after idle stop = %v, want ErrClientNotFound", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop is still waiting for the idle stop of a frozen process")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("frozen process %d still exists after the idle stop (kill 0: %v)", pid, err)
	}
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 7, BinaryPath: bin, Manifest: manifest, StopWhenIdle: true}); err != nil {
		t.Fatalf("Start after the idle stop of a frozen process: %v", err)
	}
}

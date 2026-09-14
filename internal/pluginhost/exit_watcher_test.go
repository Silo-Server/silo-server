package pluginhost_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// buildExitingPlugin compiles testdata/exitingplugin into a temp dir and
// returns the binary path with the manifest the binary reports (checksum
// included), which is what Host.Start compares against the live manifest.
func buildExitingPlugin(t *testing.T) (string, *pluginv1.PluginManifest) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "exitingplugin")
	build := exec.Command("go", "build", "-o", bin, "./testdata/exitingplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build exitingplugin: %v\n%s", err, out)
	}
	raw, err := exec.Command(bin, "manifest").Output()
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	manifest := &pluginv1.PluginManifest{}
	if err := protojson.Unmarshal(raw, manifest); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	return bin, manifest
}

func TestHostExitWatcherReportsCrash(t *testing.T) {
	bin, manifest := buildExitingPlugin(t)
	exitFile := filepath.Join(t.TempDir(), "exit-now")
	t.Setenv("SILO_TEST_PLUGIN_EXIT_FILE", exitFile)

	host := pluginhost.NewHost(pluginhost.Config{
		Logger:            hclog.NewNullLogger(),
		ExitCheckInterval: 20 * time.Millisecond,
	})
	exited := make(chan int, 4)
	host.SetExitHandler(func(id int) { exited <- id })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 7, BinaryPath: bin, Manifest: manifest})
	if err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(func() { _ = host.Stop(7) })

	if _, err := host.Client(7); err != nil {
		t.Fatalf("Client before crash: %v", err)
	}
	if err := os.WriteFile(exitFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case id := <-exited:
		if id != 7 {
			t.Fatalf("exit handler got installation %d, want 7", id)
		}
	case <-ctx.Done():
		t.Fatal("exit handler was not called after the plugin process exited")
	}
	if _, err := host.Client(7); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("Client after crash = %v, want ErrClientNotFound", err)
	}
	if _, err := client.MetadataProvider("exiting"); !errors.Is(err, pluginhost.ErrPluginUnhealthy) {
		t.Fatalf("retained client after crash = %v, want ErrPluginUnhealthy", err)
	}
}

func TestHostExitWatcherIgnoresDeliberateStop(t *testing.T) {
	bin, manifest := buildExitingPlugin(t)
	exitFile := filepath.Join(t.TempDir(), "exit-now")
	t.Setenv("SILO_TEST_PLUGIN_EXIT_FILE", exitFile)

	host := pluginhost.NewHost(pluginhost.Config{
		Logger:            hclog.NewNullLogger(),
		ExitCheckInterval: 20 * time.Millisecond,
	})
	exited := make(chan int, 4)
	host.SetExitHandler(func(id int) { exited <- id })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 8, BinaryPath: bin, Manifest: manifest}); err != nil {
		t.Fatalf("host.Start(8): %v", err)
	}
	if err := host.Stop(8); err != nil {
		t.Fatalf("host.Stop: %v", err)
	}

	// Stop reaps the process synchronously, so a report for 8 after this
	// point would be a crash misclassification. Rather than waiting a fixed
	// time for nothing to happen, crash a second instance on the same host
	// and use its report as the clock: by the time the watcher has noticed
	// 9's exit (written after Stop returned), 8's watcher has had at least
	// as many ticks to misfire.
	if _, err := host.Start(ctx, pluginhost.StartRequest{InstallationID: 9, BinaryPath: bin, Manifest: manifest}); err != nil {
		t.Fatalf("host.Start(9): %v", err)
	}
	t.Cleanup(func() { _ = host.Stop(9) })
	if err := os.WriteFile(exitFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for {
		select {
		case id := <-exited:
			switch id {
			case 9:
				return
			case 8:
				t.Fatal("exit handler reported installation 8 after a deliberate Stop")
			default:
				t.Fatalf("exit handler reported unexpected installation %d", id)
			}
		case <-ctx.Done():
			t.Fatal("exit handler was not called for the crashed instance 9")
		}
	}
}

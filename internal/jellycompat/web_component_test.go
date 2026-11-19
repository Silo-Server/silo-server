package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedWebSeekSources(t *testing.T, root string) {
	t.Helper()
	for fixture, path := range map[string]string{"playbackmanager.js": webPlaybackManagerSource, "htmlvideo.js": webHTMLVideoPlayerSource} {
		data, err := os.ReadFile(filepath.Join("testdata", "web-seek-reanchor", fixture))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedWebSeekPatchBehavior(t *testing.T) {
	root := t.TempDir()
	seedWebSeekSources(t, root)
	if err := patchManagedWebSources(root); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js required to execute upstream Jellyfin Web seek logic")
	}
	cmd := exec.CommandContext(t.Context(), node, "testdata/web-seek-reanchor/behavior.cjs", filepath.Join(root, webPlaybackManagerSource), filepath.Join(root, webHTMLVideoPlayerSource))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("patched upstream behavior: %v\n%s", err, out)
	}
}

func TestManagedWebSeekPatchRejectsIncompatibleSources(t *testing.T) {
	for _, mode := range []string{"missing", "ambiguous", "already patched"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			seedWebSeekSources(t, root)
			manager := filepath.Join(root, webPlaybackManagerSource)
			player := filepath.Join(root, webHTMLVideoPlayerSource)
			original, err := os.ReadFile(manager)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				if err := os.WriteFile(player, []byte("upstream changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				data, err := os.ReadFile(player)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(player, append(data, data...), 0o644); err != nil {
					t.Fatal(err)
				}
			case "already patched":
				if err := patchManagedWebSources(root); err != nil {
					t.Fatal(err)
				}
				original, err = os.ReadFile(manager)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := patchManagedWebSources(root); err == nil || !strings.Contains(err.Error(), "incompatible upstream source") {
				t.Fatalf("got %v", err)
			}
			after, err := os.ReadFile(manager)
			if err != nil || string(after) != string(original) {
				t.Fatal("failed validation partially modified source")
			}
		})
	}
}

func TestInstallWebComponentRejectsUnsafeVersion(t *testing.T) {
	root := t.TempDir()
	_, err := InstallWebComponent(context.Background(), WebComponentInstallOptions{
		InstallRoot: root,
		Version:     "10.11.6;touch-bad",
		RunCommand: func(context.Context, string, []string, string) error {
			t.Fatal("RunCommand should not be called for an invalid version")
			return nil
		},
	})
	if err == nil {
		t.Fatal("InstallWebComponent returned nil error for invalid version")
	}
}

func TestInstallWebComponentRejectsUnofficialSource(t *testing.T) {
	root := t.TempDir()
	_, err := InstallWebComponent(context.Background(), WebComponentInstallOptions{
		InstallRoot: root,
		Version:     "10.11.6",
		SourceURL:   "https://example.test/jellyfin-web.git",
		RunCommand: func(context.Context, string, []string, string) error {
			t.Fatal("RunCommand should not be called for an invalid source URL")
			return nil
		},
	})
	if err == nil {
		t.Fatal("InstallWebComponent returned nil error for unofficial source URL")
	}
}

func TestBeginWebOperationRejectsFreshMalformedLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, webInstallLock), []byte("installing"), 0o644); err != nil {
		t.Fatalf("write malformed lock: %v", err)
	}
	defer clearWebInstallState(root)

	_, err := beginWebOperation(root, WebComponentOperationInstall)
	if !errors.Is(err, ErrWebComponentOperationActive) {
		t.Fatalf("beginWebOperation error = %v, want ErrWebComponentOperationActive", err)
	}
}

func TestBeginWebOperationRejectsLiveProcessLock(t *testing.T) {
	root := t.TempDir()
	host, _ := os.Hostname()
	live := WebComponentOperationStatus{
		ID:        "install-live",
		Kind:      WebComponentOperationInstall,
		State:     WebComponentOperationRunning,
		PID:       os.Getpid(),
		Process:   currentProcessToken(),
		Host:      host,
		StartedAt: "2026-06-07T00:00:00Z",
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := writeWebOperationLock(root, live); err != nil {
		t.Fatalf("write live lock: %v", err)
	}
	defer clearWebInstallState(root)

	_, err := beginWebOperation(root, WebComponentOperationRemove)
	if !errors.Is(err, ErrWebComponentOperationActive) {
		t.Fatalf("beginWebOperation error = %v, want ErrWebComponentOperationActive", err)
	}
}

func writeWebOperationLock(root string, op WebComponentOperationStatus) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, webInstallLock), data, 0o644)
}

func TestFinishWebOperationDoesNotClearDifferentLock(t *testing.T) {
	root := t.TempDir()
	first, err := beginWebOperation(root, WebComponentOperationInstall)
	if err != nil {
		t.Fatalf("begin first operation: %v", err)
	}
	second := WebComponentOperationStatus{
		ID:        "remove-new",
		Kind:      WebComponentOperationRemove,
		State:     WebComponentOperationRunning,
		PID:       os.Getpid(),
		Process:   currentProcessToken(),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeWebOperationLock(root, second); err != nil {
		t.Fatalf("replace lock: %v", err)
	}

	finishWebOperation(root, first.ID, nil)

	current := readWebOperationState(root)
	if current == nil || current.ID != second.ID {
		t.Fatalf("current lock = %+v, want second operation lock", current)
	}
	clearWebInstallState(root)
}

package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestWebComponentStatusMissing(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
		"jellyfin_compat.web_version":     "10.11.6",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	status := WebComponentStatusForConfig(cfg, map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
		"jellyfin_compat.web_version":     "10.11.6",
	})

	if status.APIState != "enabled" {
		t.Fatalf("APIState = %q, want enabled", status.APIState)
	}
	if status.WebState != WebComponentMissing {
		t.Fatalf("WebState = %q, want %q", status.WebState, WebComponentMissing)
	}
}

func TestWebComponentStatusInstalledWithProvenance(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "10.11.6")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatalf("mkdir release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "LICENSE"), []byte("GPL-2.0"), 0o644); err != nil {
		t.Fatalf("write license: %v", err)
	}
	metadata := WebComponentMetadata{
		Component: "jellyfin-web",
		SourceURL: DefaultWebSourceURL,
		Version:   "10.11.6",
		Tag:       "v10.11.6",
		CommitSHA: "abc123",
		Checksum:  "sha256:test",
		License:   "GPL-2.0",
	}
	if err := writeWebMetadata(release, metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := writeWebSourceFile(release, metadata); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}

	status := webComponentStatus(root, filepath.Join(root, "current"), "10.11.6", DefaultWebSourceURL)
	if status.WebState != WebComponentInstalled {
		t.Fatalf("WebState = %q, want %q", status.WebState, WebComponentInstalled)
	}
	if !status.LicensePresent || !status.ProvenancePresent {
		t.Fatalf("license/provenance = %t/%t, want true/true", status.LicensePresent, status.ProvenancePresent)
	}
	if status.CommitSHA != "abc123" {
		t.Fatalf("CommitSHA = %q, want abc123", status.CommitSHA)
	}
}

func TestWebComponentStatusUpdateAvailable(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "10.11.6")
	writeValidWebRelease(t, release, "10.11.6")
	if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}

	status := webComponentStatus(root, filepath.Join(root, "current"), "10.11.7", DefaultWebSourceURL)
	if status.WebState != WebComponentUpdateAvailable {
		t.Fatalf("WebState = %q, want %q", status.WebState, WebComponentUpdateAvailable)
	}
}

func TestWebComponentStatusUsesPersistedSettingsForDisplay(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":     "false",
		"jellyfin_compat.listen":      ":8096",
		"jellyfin_compat.public_url":  "http://127.0.0.1:8096",
		"jellyfin_compat.server_name": "Silo",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	status := WebComponentStatusForConfig(cfg, map[string]string{
		"jellyfin_compat.enabled":                 "true",
		"jellyfin_compat.listen":                  ":19096",
		"jellyfin_compat.public_url":              "https://compat.example.test",
		"jellyfin_compat.server_name":             "Silo Compat",
		"jellyfin_compat.emulated_server_version": "10.11.6",
		"jellyfin_compat.web_install_dir":         root,
		"jellyfin_compat.web_dir":                 filepath.Join(root, "current"),
	})

	if !status.Enabled || status.APIState != "enabled" {
		t.Fatalf("enabled/APIState = %t/%q, want true/enabled", status.Enabled, status.APIState)
	}
	if status.Listen != ":19096" {
		t.Fatalf("Listen = %q, want :19096", status.Listen)
	}
	if status.PublicURL != "https://compat.example.test" {
		t.Fatalf("PublicURL = %q, want persisted public URL", status.PublicURL)
	}
	if status.ServerName != "Silo Compat" {
		t.Fatalf("ServerName = %q, want persisted server name", status.ServerName)
	}
	if !status.RestartRequired {
		t.Fatal("RestartRequired = false, want true when persisted settings differ from running config")
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

func TestRemoveWebComponentOnlyRemovesGeneratedAssets(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "10.11.6")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatalf("mkdir release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "LICENSE"), []byte("GPL-2.0"), 0o644); err != nil {
		t.Fatalf("write license: %v", err)
	}
	metadata := WebComponentMetadata{
		Component: "jellyfin-web",
		SourceURL: DefaultWebSourceURL,
		Version:   "10.11.6",
		Tag:       "v10.11.6",
		License:   "GPL-2.0",
	}
	if err := writeWebMetadata(release, metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := writeWebSourceFile(release, metadata); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	if err := RemoveWebComponent(root); err != nil {
		t.Fatalf("RemoveWebComponent: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
	if _, err := os.Stat(release); !os.IsNotExist(err) {
		t.Fatalf("release dir still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Fatalf("current link still exists or stat failed unexpectedly: %v", err)
	}
}

func TestWebComponentStatusRecoversLegacyStaleOperationLock(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, webInstallLock), []byte("installing"), 0o644); err != nil {
		t.Fatalf("write legacy lock: %v", err)
	}
	old := time.Now().Add(-(webMalformedLockGrace + time.Second))
	if err := os.Chtimes(filepath.Join(root, webInstallLock), old, old); err != nil {
		t.Fatalf("age legacy lock: %v", err)
	}

	status := webComponentStatus(root, filepath.Join(root, "current"), "10.11.6", DefaultWebSourceURL)

	if status.WebState == WebComponentInstalling {
		t.Fatalf("WebState = %q, want non-installing state after stale lock recovery", status.WebState)
	}
	if status.Operation != nil {
		t.Fatalf("Operation = %+v, want nil after stale lock recovery", status.Operation)
	}
	if _, err := os.Stat(filepath.Join(root, webInstallLock)); !os.IsNotExist(err) {
		t.Fatalf("legacy lock still exists or stat failed unexpectedly: %v", err)
	}
	if !strings.Contains(status.LastError, "recovered stale Jellyfin Web") {
		t.Fatalf("LastError = %q, want recovered stale lock message", status.LastError)
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

func TestBeginWebOperationRecoversDeadProcessLock(t *testing.T) {
	root := t.TempDir()
	host, _ := os.Hostname()
	stale := WebComponentOperationStatus{
		ID:        "install-dead",
		Kind:      WebComponentOperationInstall,
		State:     WebComponentOperationRunning,
		PID:       999999,
		Process:   "dead-process-token",
		Host:      host,
		StartedAt: "2026-06-07T00:00:00Z",
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := writeWebOperationLock(root, stale); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	op, err := beginWebOperation(root, WebComponentOperationInstall)
	if err != nil {
		t.Fatalf("beginWebOperation: %v", err)
	}
	defer finishWebOperation(root, op.ID, nil)

	if op.ID == stale.ID {
		t.Fatalf("operation ID was not replaced: %q", op.ID)
	}
	if op.PID != os.Getpid() || op.Process == "" {
		t.Fatalf("operation process identity = pid %d token %q, want current process", op.PID, op.Process)
	}
}

func TestBeginWebOperationRecoversDeadProcessLockFromDifferentHost(t *testing.T) {
	root := t.TempDir()
	stale := WebComponentOperationStatus{
		ID:        "install-dead-host",
		Kind:      WebComponentOperationInstall,
		State:     WebComponentOperationRunning,
		PID:       999999,
		Process:   "dead-process-token",
		Host:      "previous-container",
		StartedAt: "2026-06-07T00:00:00Z",
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := writeWebOperationLock(root, stale); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	op, err := beginWebOperation(root, WebComponentOperationRemove)
	if err != nil {
		t.Fatalf("beginWebOperation: %v", err)
	}
	defer finishWebOperation(root, op.ID, nil)

	if op.ID == stale.ID {
		t.Fatalf("operation ID was not replaced: %q", op.ID)
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

func TestResolveCompatWebFSDoesNotFallbackToVendoredDirectory(t *testing.T) {
	cfg := &config.Config{}

	webFS, _, err := resolveCompatWebFS(context.Background(), Dependencies{Config: cfg})
	if webFS != nil {
		t.Fatalf("resolveCompatWebFS returned a web filesystem without configured assets (err=%v)", err)
	}
}

func writeWebOperationLock(root string, op WebComponentOperationStatus) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, webInstallLock), data, 0o644)
}

func writeValidWebRelease(t *testing.T, release, version string) {
	t.Helper()
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatalf("mkdir release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "LICENSE"), []byte("GPL-2.0"), 0o644); err != nil {
		t.Fatalf("write license: %v", err)
	}
	metadata := WebComponentMetadata{
		Component: "jellyfin-web",
		SourceURL: DefaultWebSourceURL,
		Version:   version,
		Tag:       "v" + version,
		CommitSHA: "abc123",
		Checksum:  "sha256:test",
		License:   "GPL-2.0",
	}
	if err := writeWebMetadata(release, metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := writeWebSourceFile(release, metadata); err != nil {
		t.Fatalf("write source file: %v", err)
	}
}

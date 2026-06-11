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

func TestWebComponentStatusDoesNotRequireRestartForLiveIdentitySettings(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":                 "true",
		"jellyfin_compat.listen":                  ":8096",
		"jellyfin_compat.public_url":              "http://127.0.0.1:8096",
		"jellyfin_compat.server_name":             "Silo",
		"jellyfin_compat.emulated_server_version": "10.11.0",
		"jellyfin_compat.web_install_dir":         root,
		"jellyfin_compat.web_dir":                 filepath.Join(root, "current"),
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	status := WebComponentStatusForConfig(cfg, map[string]string{
		"jellyfin_compat.public_url":              "https://compat.example.test",
		"jellyfin_compat.server_name":             "Silo Compat",
		"jellyfin_compat.emulated_server_version": "10.11.6",
		"jellyfin_compat.web_install_dir":         root,
		"jellyfin_compat.web_dir":                 filepath.Join(root, "current"),
	})

	if status.PublicURL != "https://compat.example.test" {
		t.Fatalf("PublicURL = %q, want persisted public URL", status.PublicURL)
	}
	if status.ServerName != "Silo Compat" {
		t.Fatalf("ServerName = %q, want persisted server name", status.ServerName)
	}
	if status.EmulatedVersion != "10.11.6" {
		t.Fatalf("EmulatedVersion = %q, want persisted emulated version", status.EmulatedVersion)
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true, want false for live identity settings")
	}
}

func TestWebComponentStatusDoesNotDefaultPinnedVersionToEmulatedVersion(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.emulated_server_version": "10.12.0",
		"jellyfin_compat.web_install_dir":         root,
		"jellyfin_compat.web_dir":                 filepath.Join(root, "current"),
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	status := WebComponentStatusForConfig(cfg, map[string]string{
		"jellyfin_compat.emulated_server_version": "10.12.0",
		"jellyfin_compat.web_install_dir":         root,
		"jellyfin_compat.web_dir":                 filepath.Join(root, "current"),
	})

	if status.PinnedVersion != config.DefaultJellyfinWebVersion {
		t.Fatalf("PinnedVersion = %q, want configured Web default", status.PinnedVersion)
	}
}

func TestWebComponentStatusDisablesWebUIWhenProxyDisabled(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":         "false",
		"jellyfin_compat.web_enabled":     "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	status := WebComponentStatusForConfig(cfg, map[string]string{
		"jellyfin_compat.enabled":         "false",
		"jellyfin_compat.web_enabled":     "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
	})

	if status.WebEnabled {
		t.Fatal("WebEnabled = true, want false when Jellyfin proxy is disabled")
	}
}

func TestWebComponentStatusReportsWebEnabledSetting(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	status := WebComponentStatusForConfig(cfg, map[string]string{
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
		"jellyfin_compat.web_enabled":     "false",
	})

	if status.WebEnabled {
		t.Fatal("WebEnabled = true, want false from persisted setting")
	}
	if status.RestartRequired {
		t.Fatal("RestartRequired = true, want false when only persisted web_enabled differs from running config")
	}
}

func TestSelectCompatibleWebVersion(t *testing.T) {
	tests := []struct {
		name      string
		api       string
		available []string
		want      string
	}{
		{
			name:      "latest patch from same emulated minor",
			api:       "10.12.0",
			available: []string{"10.12.0", "10.12.2", "10.12.1", "10.11.9"},
			want:      "10.12.2",
		},
		{
			name:      "newest lower minor when emulated minor is unavailable",
			api:       "10.12.0",
			available: []string{"10.10.9", "10.11.6", "10.11.8", "10.13.0"},
			want:      "10.11.8",
		},
		{
			name:      "ignores prerelease tags",
			api:       "10.11.0",
			available: []string{"10.11.7-alpha", "10.11.6", "10.10.10"},
			want:      "10.11.6",
		},
		{
			name:      "uses oldest available when all versions are newer",
			api:       "9.9.0",
			available: []string{"10.10.1", "10.9.9", "10.11.0"},
			want:      "10.9.9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectCompatibleWebVersion(tt.api, tt.available)
			if err != nil {
				t.Fatalf("SelectCompatibleWebVersion: %v", err)
			}
			if got != tt.want {
				t.Fatalf("SelectCompatibleWebVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectCompatibleWebVersionRejectsInvalidInputs(t *testing.T) {
	if _, err := SelectCompatibleWebVersion("not-a-version", []string{"10.11.6"}); err == nil {
		t.Fatal("SelectCompatibleWebVersion returned nil error for invalid API version")
	}
	if _, err := SelectCompatibleWebVersion("10.12.0", []string{"main", "10.11.6-alpha"}); err == nil {
		t.Fatal("SelectCompatibleWebVersion returned nil error with no stable Web versions")
	}
}

func TestParseRemoteWebReleaseVersions(t *testing.T) {
	versions, err := parseRemoteWebReleaseVersions(strings.NewReader(`[
		{"tag_name":"v10.11.6","draft":false,"prerelease":false},
		{"tag_name":"10.12.0","draft":true,"prerelease":false},
		{"tag_name":"10.12.1","draft":false,"prerelease":true},
		{"tag_name":"not-a-version","draft":false,"prerelease":false}
	]`))
	if err != nil {
		t.Fatalf("parseRemoteWebReleaseVersions: %v", err)
	}
	want := []string{"10.11.6"}
	if len(versions) != len(want) {
		t.Fatalf("versions = %#v, want %#v", versions, want)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("versions[%d] = %q, want %q", i, versions[i], want[i])
		}
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

func TestStartWebComponentRemovePublishesProgress(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "10.11.6")
	writeValidWebRelease(t, release, "10.11.6")
	if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}

	progress := make(chan WebComponentOperationStatus, 4)
	status, err := StartWebComponentRemove(WebComponentRemoveOptions{
		InstallRoot: root,
		OnProgress: func(op WebComponentOperationStatus) {
			progress <- op
		},
	})
	if err != nil {
		t.Fatalf("StartWebComponentRemove: %v", err)
	}
	if status.WebState != WebComponentRemoving {
		t.Fatalf("WebState = %q, want removing", status.WebState)
	}
	if status.Operation == nil || status.Operation.Kind != WebComponentOperationRemove {
		t.Fatalf("Operation = %+v, want remove operation", status.Operation)
	}

	seenRunning := false
	timeout := time.After(2 * time.Second)
	for {
		select {
		case op := <-progress:
			if op.Kind != WebComponentOperationRemove {
				t.Fatalf("operation kind = %q, want remove", op.Kind)
			}
			if op.State == WebComponentOperationRunning {
				seenRunning = true
				continue
			}
			if op.State != WebComponentOperationSucceeded {
				t.Fatalf("terminal state = %q, want succeeded: %s", op.State, op.Error)
			}
			if !seenRunning {
				t.Fatal("did not receive a running progress update before terminal completion")
			}
			if op.ProgressPercent != 100 {
				t.Fatalf("terminal progress = %d, want 100", op.ProgressPercent)
			}
			if op.Message != "Jellyfin Web assets removed" {
				t.Fatalf("terminal message = %q, want Jellyfin Web assets removed", op.Message)
			}
			if _, err := os.Stat(release); !os.IsNotExist(err) {
				t.Fatalf("release dir still exists or stat failed unexpectedly: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
				t.Fatalf("current link still exists or stat failed unexpectedly: %v", err)
			}
			return
		case <-timeout:
			t.Fatal("timed out waiting for remove operation completion progress")
		}
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

func TestWebComponentOperationProgressPersistsToStatus(t *testing.T) {
	root := t.TempDir()
	op, err := beginWebOperation(root, WebComponentOperationInstall)
	if err != nil {
		t.Fatalf("beginWebOperation: %v", err)
	}
	t.Cleanup(func() {
		webOperationsMu.Lock()
		delete(webOperations, root)
		webOperationsMu.Unlock()
		clearWebInstallState(root)
	})

	if op.Phase != WebComponentOperationPreparing || op.ProgressPercent != 1 {
		t.Fatalf("initial progress = %q/%d, want preparing/1", op.Phase, op.ProgressPercent)
	}
	updated := updateWebOperationProgress(root, op.ID, WebComponentOperationBuilding, 110, "Building Jellyfin Web assets")
	if updated == nil {
		t.Fatal("updateWebOperationProgress returned nil")
	}
	if updated.Phase != WebComponentOperationBuilding || updated.ProgressPercent != 100 {
		t.Fatalf("updated progress = %q/%d, want building/100", updated.Phase, updated.ProgressPercent)
	}
	if updated.Message != "Building Jellyfin Web assets" {
		t.Fatalf("updated message = %q", updated.Message)
	}

	status := webComponentStatus(root, filepath.Join(root, "current"), "10.11.6", DefaultWebSourceURL)
	if status.Operation == nil {
		t.Fatal("status.Operation = nil, want progress operation")
	}
	if status.Operation.Phase != WebComponentOperationBuilding || status.Operation.ProgressPercent != 100 {
		t.Fatalf("status progress = %q/%d, want building/100", status.Operation.Phase, status.Operation.ProgressPercent)
	}

	finished := finishWebOperation(root, op.ID, nil)
	if finished == nil {
		t.Fatal("finishWebOperation returned nil")
	}
	if finished.State != WebComponentOperationSucceeded || finished.ProgressPercent != 100 {
		t.Fatalf("finished state/progress = %q/%d, want succeeded/100", finished.State, finished.ProgressPercent)
	}
	if finished.Message != "Jellyfin Web install complete" {
		t.Fatalf("finished message = %q", finished.Message)
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

func TestResolveCompatWebFSHonorsWebEnabledSetting(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "10.11.6")
	writeValidWebRelease(t, release, "10.11.6")
	if err := os.Symlink("10.11.6", filepath.Join(root, "current")); err != nil {
		t.Fatalf("symlink current: %v", err)
	}
	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_install_dir": root,
		"jellyfin_compat.web_dir":         filepath.Join(root, "current"),
		"jellyfin_compat.web_version":     "10.11.6",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	webFS, _, err := resolveCompatWebFS(context.Background(), Dependencies{Config: cfg})
	if err != nil {
		t.Fatalf("resolve enabled WebFS: %v", err)
	}
	if webFS == nil {
		t.Fatal("resolve enabled WebFS = nil, want installed assets")
	}

	webFS, _, err = resolveCompatWebFS(context.Background(), Dependencies{
		Config: cfg,
		SettingsRepo: webComponentTestSettings{
			"jellyfin_compat.web_enabled": "false",
		},
	})
	if err != nil {
		t.Fatalf("resolve disabled WebFS: %v", err)
	}
	if webFS != nil {
		t.Fatal("resolve disabled WebFS returned assets, want nil")
	}

	webFS, _, err = resolveCompatWebFS(context.Background(), Dependencies{
		Config: cfg,
		SettingsRepo: webComponentTestSettings{
			"jellyfin_compat.enabled":     "false",
			"jellyfin_compat.web_enabled": "true",
		},
	})
	if err != nil {
		t.Fatalf("resolve proxy-disabled WebFS: %v", err)
	}
	if webFS != nil {
		t.Fatal("resolve proxy-disabled WebFS returned assets, want nil")
	}
	if _, err := os.Stat(release); err != nil {
		t.Fatalf("disabled Web UI should not remove release: %v", err)
	}
}

func TestResolveCompatWebFSDoesNotFallbackToVendoredDirectory(t *testing.T) {
	cfg := &config.Config{}

	webFS, _, err := resolveCompatWebFS(context.Background(), Dependencies{Config: cfg})
	if webFS != nil {
		t.Fatalf("resolveCompatWebFS returned a web filesystem without configured assets (err=%v)", err)
	}
}

type webComponentTestSettings map[string]string

func (s webComponentTestSettings) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
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

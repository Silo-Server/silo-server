package jellycompat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

const (
	DefaultWebSourceURL = "https://github.com/jellyfin/jellyfin-web.git"

	webMetadataFile = "SILO-JELLYFIN-WEB.json"
	webSourceFile   = "SILO-JELLYFIN-WEB-SOURCE.txt"
	webInstallLock  = ".installing"
	webLastError    = ".last-error"
	webTempMarker   = ".silo-jellyfin-web-temp"

	webMalformedLockGrace = 2 * time.Minute
	webOperationStaleAge  = 90 * time.Minute
)

var (
	ErrWebComponentOperationActive = errors.New("jellyfin web operation already running")
	ErrWebInstallerUnavailable     = errors.New("jellyfin web installer prerequisites are missing")

	webVersionPattern = regexp.MustCompile(`^[0-9]+[.][0-9]+[.][0-9]+(?:[-.][A-Za-z0-9]+)*$`)
	webOperationsMu   sync.Mutex
	webOperations     = map[string]*WebComponentOperationStatus{}
)

type WebComponentState string

const (
	WebComponentMissing         WebComponentState = "missing"
	WebComponentInstalling      WebComponentState = "installing"
	WebComponentRemoving        WebComponentState = "removing"
	WebComponentInstalled       WebComponentState = "installed"
	WebComponentFailed          WebComponentState = "failed"
	WebComponentUpdateAvailable WebComponentState = "update_available"
)

type WebComponentOperationKind string

const (
	WebComponentOperationInstall WebComponentOperationKind = "install"
	WebComponentOperationRemove  WebComponentOperationKind = "remove"
)

type WebComponentOperationState string

const (
	WebComponentOperationRunning   WebComponentOperationState = "running"
	WebComponentOperationSucceeded WebComponentOperationState = "succeeded"
	WebComponentOperationFailed    WebComponentOperationState = "failed"
)

type WebComponentOperationPhase string

const (
	WebComponentOperationPreparing   WebComponentOperationPhase = "preparing"
	WebComponentOperationDownloading WebComponentOperationPhase = "downloading"
	WebComponentOperationInstalling  WebComponentOperationPhase = "installing_dependencies"
	WebComponentOperationBuilding    WebComponentOperationPhase = "building"
	WebComponentOperationStaging     WebComponentOperationPhase = "staging"
	WebComponentOperationActivating  WebComponentOperationPhase = "activating"
	WebComponentOperationPersisting  WebComponentOperationPhase = "persisting_settings"
	WebComponentOperationRemoving    WebComponentOperationPhase = "removing"
)

type WebComponentMetadata struct {
	Component    string `json:"component"`
	SourceURL    string `json:"source_url"`
	Version      string `json:"version"`
	Tag          string `json:"tag"`
	CommitSHA    string `json:"commit_sha"`
	Checksum     string `json:"checksum"`
	BuildCommand string `json:"build_command"`
	InstalledAt  string `json:"installed_at"`
	Modified     bool   `json:"modified"`
	License      string `json:"license"`
}

type WebInstallerPrerequisite struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Message   string `json:"message,omitempty"`
}

type WebComponentOperationStatus struct {
	ID              string                     `json:"id"`
	Kind            WebComponentOperationKind  `json:"kind"`
	State           WebComponentOperationState `json:"state"`
	PID             int                        `json:"pid,omitempty"`
	Process         string                     `json:"process,omitempty"`
	Host            string                     `json:"host,omitempty"`
	StartedAt       string                     `json:"started_at"`
	CompletedAt     string                     `json:"completed_at,omitempty"`
	Phase           WebComponentOperationPhase `json:"phase,omitempty"`
	ProgressPercent int                        `json:"progress_percent,omitempty"`
	Message         string                     `json:"message,omitempty"`
	Error           string                     `json:"error,omitempty"`
	lockMTime       time.Time                  `json:"-"`
	malformed       bool                       `json:"-"`
}

type WebComponentStatus struct {
	Enabled           bool                         `json:"enabled"`
	APIState          string                       `json:"api_state"`
	Listen            string                       `json:"listen"`
	PublicURL         string                       `json:"public_url"`
	EmulatedVersion   string                       `json:"emulated_server_version"`
	ServerName        string                       `json:"server_name"`
	WebEnabled        bool                         `json:"web_enabled"`
	WebState          WebComponentState            `json:"web_state"`
	PinnedVersion     string                       `json:"pinned_version"`
	InstalledVersion  string                       `json:"installed_version,omitempty"`
	SourceURL         string                       `json:"source_url"`
	Tag               string                       `json:"tag,omitempty"`
	CommitSHA         string                       `json:"commit_sha,omitempty"`
	Checksum          string                       `json:"checksum,omitempty"`
	InstallRoot       string                       `json:"install_root"`
	InstallPath       string                       `json:"install_path"`
	InstalledAt       string                       `json:"installed_at,omitempty"`
	LicensePresent    bool                         `json:"license_present"`
	ProvenancePresent bool                         `json:"provenance_present"`
	InstallerReady    bool                         `json:"installer_ready"`
	Prerequisites     []WebInstallerPrerequisite   `json:"prerequisites"`
	Operation         *WebComponentOperationStatus `json:"operation,omitempty"`
	LastError         string                       `json:"last_error,omitempty"`
	RestartRequired   bool                         `json:"restart_required"`
}

type WebComponentInstallOptions struct {
	InstallRoot string
	SourceURL   string
	Version     string
	Now         func() time.Time
	RunCommand  func(context.Context, string, []string, string) error
	OnProgress  func(WebComponentOperationStatus)
}

type WebComponentRemoveOptions struct {
	InstallRoot string
	OnProgress  func(WebComponentOperationStatus)
}

type WebComponentInstallCompleteFunc func(context.Context, WebComponentStatus) error

func DefaultWebInstallRoot(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.JellyfinCompat.WebInstallDir) != "" {
		return strings.TrimSpace(cfg.JellyfinCompat.WebInstallDir)
	}
	return config.DefaultJellyfinWebInstallDir
}

func DefaultWebInstallPath(cfg *config.Config) string {
	return ManagedWebInstallPath(DefaultWebInstallRoot(cfg))
}

func ManagedWebInstallPath(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = config.DefaultJellyfinWebInstallDir
	}
	return filepath.Join(root, "current")
}

func DefaultWebVersion(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.JellyfinCompat.WebVersion) != "" {
		return strings.TrimSpace(cfg.JellyfinCompat.WebVersion)
	}
	return config.DefaultJellyfinWebVersion
}

func ResolveCompatibleWebVersion(ctx context.Context, sourceURL, apiVersion string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sourceURL, err := normalizeWebSourceURL(sourceURL)
	if err != nil {
		return "", err
	}
	versions, err := listRemoteWebVersions(ctx, sourceURL)
	if err != nil {
		return "", fmt.Errorf("list Jellyfin Web versions: %w", err)
	}
	version, err := SelectCompatibleWebVersion(apiVersion, versions)
	if err != nil {
		return "", err
	}
	return version, nil
}

func SelectCompatibleWebVersion(apiVersion string, available []string) (string, error) {
	target, ok := parseStableWebVersion(apiVersion)
	if !ok {
		return "", fmt.Errorf("invalid Jellyfin API version %q", strings.TrimSpace(apiVersion))
	}

	byVersion := map[string]webStableVersion{}
	for _, raw := range available {
		version, ok := parseStableWebVersion(raw)
		if !ok {
			continue
		}
		byVersion[version.String()] = version
	}
	if len(byVersion) == 0 {
		return "", errors.New("no stable Jellyfin Web versions found")
	}

	versions := make([]webStableVersion, 0, len(byVersion))
	for _, version := range byVersion {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareStableWebVersions(versions[i], versions[j]) > 0
	})

	for _, version := range versions {
		if version.major == target.major && version.minor == target.minor {
			return version.String(), nil
		}
	}
	for _, version := range versions {
		if compareStableWebVersionMinor(version, target) < 0 {
			return version.String(), nil
		}
	}

	return versions[len(versions)-1].String(), nil
}

func listRemoteWebVersions(ctx context.Context, sourceURL string) ([]string, error) {
	if _, err := normalizeWebSourceURL(sourceURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/jellyfin/jellyfin-web/releases?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "silo-jellyfin-web-installer")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub releases request returned %s", resp.Status)
	}
	return parseRemoteWebReleaseVersions(resp.Body)
}

type webReleaseVersion struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func parseRemoteWebReleaseVersions(r io.Reader) ([]string, error) {
	var releases []webReleaseVersion
	if err := json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&releases); err != nil {
		return nil, err
	}
	versions := []string{}
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		version := normalizeWebVersion(release.TagName)
		if version == "" {
			continue
		}
		versions = append(versions, version)
	}
	return versions, nil
}

type webStableVersion struct {
	major int
	minor int
	patch int
}

func parseStableWebVersion(raw string) (webStableVersion, bool) {
	version := normalizeWebVersion(raw)
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return webStableVersion{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return webStableVersion{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return webStableVersion{}, false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return webStableVersion{}, false
	}
	return webStableVersion{major: major, minor: minor, patch: patch}, true
}

func (v webStableVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func compareStableWebVersionMinor(left, right webStableVersion) int {
	if left.major != right.major {
		return left.major - right.major
	}
	return left.minor - right.minor
}

func compareStableWebVersions(left, right webStableVersion) int {
	if diff := compareStableWebVersionMinor(left, right); diff != 0 {
		return diff
	}
	return left.patch - right.patch
}

func WebComponentStatusForConfig(cfg *config.Config, settings map[string]string) WebComponentStatus {
	enabled := false
	if cfg != nil {
		enabled = cfg.JellyfinCompat.Enabled
	}
	if raw := strings.TrimSpace(settings["jellyfin_compat.enabled"]); raw != "" {
		enabled = strings.EqualFold(raw, "true") || raw == "1" || strings.EqualFold(raw, "yes")
	}

	configuredWebEnabled := true
	if cfg != nil {
		configuredWebEnabled = cfg.JellyfinCompat.WebEnabled
	}
	var webEnabledError string
	if raw := strings.TrimSpace(settings["jellyfin_compat.web_enabled"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			webEnabledError = fmt.Sprintf("invalid Jellyfin Web UI enabled setting %q", raw)
		} else {
			configuredWebEnabled = parsed
		}
	}
	webEnabled := enabled && configuredWebEnabled

	root := stringSetting(settings, "jellyfin_compat.web_install_dir", DefaultWebInstallRoot(cfg))
	webDir := stringSetting(settings, "jellyfin_compat.web_dir", DefaultWebInstallPath(cfg))
	pinned := stringSetting(settings, "jellyfin_compat.web_version", DefaultWebVersion(cfg))
	sourceURL := stringSetting(settings, "jellyfin_compat.web_source_url", DefaultWebSourceURL)

	status := webComponentStatus(root, webDir, pinned, sourceURL)
	status.Enabled = enabled
	status.WebEnabled = webEnabled
	if webEnabledError != "" {
		status.LastError = appendStatusError(status.LastError, webEnabledError)
	}
	if cfg != nil {
		status.Listen = cfg.JellyfinCompat.Listen
		status.PublicURL = cfg.JellyfinCompat.PublicURL
		status.EmulatedVersion = cfg.JellyfinCompat.EmulatedServerVersion
		status.ServerName = cfg.JellyfinCompat.ServerName
	}
	status.Listen = stringSetting(settings, "jellyfin_compat.listen", status.Listen)
	status.PublicURL = stringSetting(settings, "jellyfin_compat.public_url", status.PublicURL)
	status.EmulatedVersion = stringSetting(settings, "jellyfin_compat.emulated_server_version", status.EmulatedVersion)
	status.ServerName = stringSetting(settings, "jellyfin_compat.server_name", status.ServerName)
	status.APIState = "disabled"
	if enabled {
		status.APIState = "enabled"
		if status.Listen == "" {
			status.APIState = "error"
			status.LastError = appendStatusError(status.LastError, "jellyfin compatibility listen address is empty")
		}
	}
	if cfg != nil {
		status.RestartRequired = enabled != cfg.JellyfinCompat.Enabled ||
			strings.TrimSpace(status.Listen) != strings.TrimSpace(cfg.JellyfinCompat.Listen)
	}
	return status
}

func StartWebComponentInstall(opts WebComponentInstallOptions, onComplete WebComponentInstallCompleteFunc) (WebComponentStatus, error) {
	opts, status, err := normalizeWebInstallOptions(opts)
	if err != nil {
		return status, err
	}
	if opts.RunCommand == nil {
		if err := ensureWebInstallerPrerequisites(); err != nil {
			status.LastError = err.Error()
			return status, err
		}
	}

	op, err := beginWebOperation(opts.InstallRoot, WebComponentOperationInstall)
	if err != nil {
		status.Operation = currentWebOperation(opts.InstallRoot)
		if status.Operation != nil && status.Operation.State == WebComponentOperationRunning {
			status.WebState = WebComponentInstalling
		}
		return status, err
	}
	status.Operation = op
	status.WebState = WebComponentInstalling
	notifyWebOperationProgress(opts.OnProgress, op)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()

		installStatus, installErr := installWebComponentLocked(ctx, opts, op.ID)
		if installErr == nil && onComplete != nil {
			notifyWebOperationProgress(
				opts.OnProgress,
				updateWebOperationProgress(opts.InstallRoot, op.ID, WebComponentOperationPersisting, 98, "Persisting Jellyfin Web settings"),
			)
			installErr = onComplete(context.Background(), installStatus)
			if installErr != nil {
				writeWebInstallError(opts.InstallRoot, installErr)
			}
		}
		notifyWebOperationProgress(opts.OnProgress, finishWebOperation(opts.InstallRoot, op.ID, installErr))
	}()

	return status, nil
}

func StartWebComponentRemove(opts WebComponentRemoveOptions) (WebComponentStatus, error) {
	root, err := normalizeWebInstallRoot(opts.InstallRoot)
	status := webComponentStatus(root, ManagedWebInstallPath(root), "", "")
	if err != nil {
		status.LastError = err.Error()
		return status, err
	}
	op, err := beginWebOperation(root, WebComponentOperationRemove)
	if err != nil {
		status.Operation = currentWebOperation(root)
		if status.Operation != nil && status.Operation.State == WebComponentOperationRunning {
			status.WebState = WebComponentRemoving
		}
		return status, err
	}
	status.Operation = op
	status.WebState = WebComponentRemoving
	notifyWebOperationProgress(opts.OnProgress, op)

	go func() {
		notifyWebOperationProgress(
			opts.OnProgress,
			updateWebOperationProgress(root, op.ID, WebComponentOperationRemoving, 50, "Removing managed Jellyfin Web assets"),
		)
		err := removeWebComponentLocked(root)
		notifyWebOperationProgress(opts.OnProgress, finishWebOperation(root, op.ID, err))
	}()

	return status, nil
}

func InstallWebComponent(ctx context.Context, opts WebComponentInstallOptions) (WebComponentStatus, error) {
	opts, status, err := normalizeWebInstallOptions(opts)
	if err != nil {
		return status, err
	}
	if opts.RunCommand == nil {
		if err := ensureWebInstallerPrerequisites(); err != nil {
			status.LastError = err.Error()
			return status, err
		}
	}

	op, err := beginWebOperation(opts.InstallRoot, WebComponentOperationInstall)
	if err != nil {
		status.Operation = currentWebOperation(opts.InstallRoot)
		if status.Operation != nil && status.Operation.State == WebComponentOperationRunning {
			status.WebState = WebComponentInstalling
		}
		return status, err
	}

	notifyWebOperationProgress(opts.OnProgress, op)
	installStatus, err := installWebComponentLocked(ctx, opts, op.ID)
	notifyWebOperationProgress(opts.OnProgress, finishWebOperation(opts.InstallRoot, op.ID, err))
	return installStatus, err
}

func installWebComponentLocked(ctx context.Context, opts WebComponentInstallOptions, opID string) (WebComponentStatus, error) {
	root := opts.InstallRoot
	sourceURL := opts.SourceURL
	version := opts.Version
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	run := opts.RunCommand
	if run == nil {
		run = runWebInstallCommand
	}
	reportProgress := func(phase WebComponentOperationPhase, progressPercent int, message string) {
		notifyWebOperationProgress(
			opts.OnProgress,
			updateWebOperationProgress(root, opID, phase, progressPercent, message),
		)
	}

	reportProgress(WebComponentOperationPreparing, 2, "Preparing Jellyfin Web install directory")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}

	reportProgress(WebComponentOperationPreparing, 5, "Creating temporary install workspace")
	tmpRoot, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	defer os.RemoveAll(tmpRoot)
	if err := os.WriteFile(filepath.Join(tmpRoot, webTempMarker), []byte("silo jellyfin web install workspace\n"), 0o644); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}

	srcDir := filepath.Join(tmpRoot, "src")
	tag := "v" + version
	reportProgress(WebComponentOperationDownloading, 10, fmt.Sprintf("Downloading Jellyfin Web %s", tag))
	if err := run(ctx, "", []string{"git", "clone", "--depth", "1", "--branch", tag, sourceURL, srcDir}, ""); err != nil {
		err = fmt.Errorf("clone jellyfin-web %s: %w", tag, err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationDownloading, 25, "Download complete; resolving Jellyfin Web revision")
	commitSHA, err := commandOutput(ctx, srcDir, "git", "rev-parse", "HEAD")
	if err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationInstalling, 35, "Installing Jellyfin Web dependencies")
	if err := run(ctx, srcDir, []string{"npm", "ci"}, ""); err != nil {
		err = fmt.Errorf("install jellyfin-web dependencies: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationBuilding, 60, "Building Jellyfin Web production assets")
	if err := run(ctx, srcDir, []string{"npm", "run", "build:production"}, ""); err != nil {
		err = fmt.Errorf("build jellyfin-web production bundle: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}

	reportProgress(WebComponentOperationStaging, 75, "Staging built Jellyfin Web assets")
	releaseDir := filepath.Join(root, version)
	stagedDir := filepath.Join(tmpRoot, version)
	if err := copyDir(filepath.Join(srcDir, "dist"), stagedDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	if err := copyFile(filepath.Join(srcDir, "LICENSE"), filepath.Join(stagedDir, "LICENSE")); err != nil {
		err = fmt.Errorf("copy jellyfin-web license: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationStaging, 85, "Calculating Jellyfin Web asset checksum")
	checksum, err := directoryChecksum(stagedDir)
	if err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	metadata := WebComponentMetadata{
		Component:    "jellyfin-web",
		SourceURL:    sourceURL,
		Version:      version,
		Tag:          tag,
		CommitSHA:    strings.TrimSpace(commitSHA),
		Checksum:     "sha256:" + checksum,
		BuildCommand: "npm ci && npm run build:production",
		InstalledAt:  now().UTC().Format(time.RFC3339),
		Modified:     false,
		License:      "GPL-2.0",
	}
	if err := writeWebMetadata(stagedDir, metadata); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	if err := writeWebSourceFile(stagedDir, metadata); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationStaging, 90, "Validating staged Jellyfin Web assets")
	if _, err := validateWebComponentDirectory(stagedDir); err != nil {
		err = fmt.Errorf("validate staged jellyfin-web assets: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationActivating, 94, "Preparing Jellyfin Web activation")
	if err := ensureCanReplaceWebRelease(releaseDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	if err := os.RemoveAll(releaseDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	if err := os.Rename(stagedDir, releaseDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	reportProgress(WebComponentOperationActivating, 96, fmt.Sprintf("Activating Jellyfin Web %s", version))
	if err := activateWebComponent(root, version); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), err
	}
	clearWebInstallError(root)
	return webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL), nil
}

func RemoveWebComponent(root string) error {
	root, err := normalizeWebInstallRoot(root)
	if err != nil {
		return err
	}
	op, err := beginWebOperation(root, WebComponentOperationRemove)
	if err != nil {
		return err
	}
	err = removeWebComponentLocked(root)
	finishWebOperation(root, op.ID, err)
	return err
}

func removeWebComponentLocked(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			clearWebInstallError(root)
			return nil
		}
		writeWebInstallError(root, err)
		return err
	}

	removeCurrent := currentLinkTargetsManagedWebRelease(root)
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		if name == ".current-next" {
			if entry.Type()&os.ModeSymlink != 0 {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					writeWebInstallError(root, err)
					return err
				}
			}
			continue
		}
		if name == "current" {
			if !removeCurrent {
				continue
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				writeWebInstallError(root, err)
				return err
			}
			continue
		}
		if entry.IsDir() && strings.HasPrefix(name, ".install-") {
			if !filePresent(filepath.Join(path, webTempMarker)) {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				writeWebInstallError(root, err)
				return err
			}
			continue
		}
		if entry.IsDir() && webComponentDirectoryReady(path) {
			if err := os.RemoveAll(path); err != nil {
				writeWebInstallError(root, err)
				return err
			}
		}
	}
	clearWebInstallError(root)
	return nil
}

func normalizeWebInstallOptions(opts WebComponentInstallOptions) (WebComponentInstallOptions, WebComponentStatus, error) {
	root, err := normalizeWebInstallRoot(opts.InstallRoot)
	status := webComponentStatus(root, ManagedWebInstallPath(root), opts.Version, opts.SourceURL)
	if err != nil {
		status.LastError = err.Error()
		return opts, status, err
	}

	version, err := normalizeRequiredWebVersion(opts.Version)
	if err != nil {
		status.InstallRoot = root
		status.InstallPath = ManagedWebInstallPath(root)
		status.LastError = err.Error()
		return opts, status, err
	}

	sourceURL, err := normalizeWebSourceURL(opts.SourceURL)
	if err != nil {
		status.InstallRoot = root
		status.InstallPath = ManagedWebInstallPath(root)
		status.LastError = err.Error()
		return opts, status, err
	}

	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.InstallRoot = root
	opts.Version = version
	opts.SourceURL = sourceURL
	status = webComponentStatus(root, ManagedWebInstallPath(root), version, sourceURL)
	return opts, status, nil
}

func normalizeWebInstallRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = config.DefaultJellyfinWebInstallDir
	}
	cleaned := filepath.Clean(root)
	if !filepath.IsAbs(cleaned) {
		abs, err := filepath.Abs(cleaned)
		if err != nil {
			return cleaned, fmt.Errorf("resolve jellyfin web install directory: %w", err)
		}
		cleaned = abs
	}
	if cleaned == string(filepath.Separator) {
		return cleaned, errors.New("jellyfin web install directory cannot be the filesystem root")
	}
	return cleaned, nil
}

func normalizeManagedWebInstallPath(root, webDir string) (string, error) {
	expected := ManagedWebInstallPath(root)
	expectedAbs, err := cleanAbsolutePath(expected)
	if err != nil {
		return expected, err
	}
	webDir = strings.TrimSpace(webDir)
	if webDir == "" {
		return expectedAbs, nil
	}
	actualAbs, err := cleanAbsolutePath(webDir)
	if err != nil {
		return expectedAbs, err
	}
	if actualAbs != expectedAbs {
		return expectedAbs, fmt.Errorf("jellyfin web_dir must use managed install path %s", expectedAbs)
	}
	return expectedAbs, nil
}

func cleanAbsolutePath(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		cleaned = config.DefaultJellyfinWebDir
	}
	if filepath.IsAbs(cleaned) {
		return cleaned, nil
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return cleaned, err
	}
	return abs, nil
}

func ensureCanReplaceWebRelease(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing Jellyfin Web release: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to replace non-directory Jellyfin Web release path %s", path)
	}
	if !webComponentDirectoryReady(path) {
		return fmt.Errorf("refusing to replace %s because it is not a managed Jellyfin Web release", path)
	}
	return nil
}

func validateWebComponentDirectory(path string) (WebComponentMetadata, error) {
	if !filePresent(filepath.Join(path, "index.html")) {
		return WebComponentMetadata{}, errors.New("index.html is missing")
	}
	if !filePresent(filepath.Join(path, "LICENSE")) {
		return WebComponentMetadata{}, errors.New("LICENSE is missing")
	}
	if !filePresent(filepath.Join(path, webSourceFile)) {
		return WebComponentMetadata{}, fmt.Errorf("%s is missing", webSourceFile)
	}
	metadata, err := readWebMetadata(path)
	if err != nil {
		return WebComponentMetadata{}, fmt.Errorf("%s is missing or invalid: %w", webMetadataFile, err)
	}
	if metadata.Component != "jellyfin-web" {
		return WebComponentMetadata{}, fmt.Errorf("unexpected component %q", metadata.Component)
	}
	if normalizeWebVersion(metadata.Version) == "" {
		return WebComponentMetadata{}, fmt.Errorf("invalid Jellyfin Web metadata version %q", metadata.Version)
	}
	if strings.TrimSpace(metadata.License) == "" {
		return WebComponentMetadata{}, errors.New("Jellyfin Web metadata license is missing")
	}
	if _, err := normalizeWebSourceURL(metadata.SourceURL); err != nil {
		return WebComponentMetadata{}, err
	}
	return metadata, nil
}

func webComponentDirectoryReady(path string) bool {
	_, err := validateWebComponentDirectory(path)
	return err == nil
}

func currentLinkTargetsManagedWebRelease(root string) bool {
	current := filepath.Join(root, "current")
	info, err := os.Lstat(current)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(current)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	rootAbs, err := cleanAbsolutePath(root)
	if err != nil {
		return false
	}
	targetAbs, err := cleanAbsolutePath(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return false
	}
	return webComponentDirectoryReady(targetAbs)
}

func normalizeRequiredWebVersion(version string) (string, error) {
	normalized := normalizeWebVersion(version)
	if normalized == "" {
		return "", fmt.Errorf("invalid Jellyfin Web version %q", strings.TrimSpace(version))
	}
	return normalized, nil
}

func normalizeWebSourceURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultWebSourceURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Jellyfin Web source URL %q", raw)
	}
	if parsed.Scheme != "https" {
		return "", errors.New("jellyfin web source URL must use https")
	}
	host := strings.EqualFold(parsed.Host, "github.com")
	repoPath := strings.TrimSuffix(parsed.EscapedPath(), ".git")
	if !host || !strings.EqualFold(repoPath, "/jellyfin/jellyfin-web") {
		return "", errors.New("jellyfin web source URL must be the official Jellyfin Web repository")
	}
	return DefaultWebSourceURL, nil
}

func CheckWebInstallerPrerequisites() []WebInstallerPrerequisite {
	prereqs := []WebInstallerPrerequisite{
		{Name: "Git", Command: "git"},
		{Name: "npm", Command: "npm"},
	}
	for i := range prereqs {
		path, err := exec.LookPath(prereqs[i].Command)
		if err == nil {
			prereqs[i].Available = true
			prereqs[i].Path = path
			continue
		}
		prereqs[i].Message = fmt.Sprintf("%s is required to download and build Jellyfin Web", prereqs[i].Command)
	}
	return prereqs
}

func ensureWebInstallerPrerequisites() error {
	var missing []string
	for _, prereq := range CheckWebInstallerPrerequisites() {
		if !prereq.Available {
			missing = append(missing, prereq.Command)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: install %s on the Silo host or container", ErrWebInstallerUnavailable, strings.Join(missing, ", "))
	}
	return nil
}

func setInitialWebOperationProgress(op *WebComponentOperationStatus) {
	if op == nil {
		return
	}
	switch op.Kind {
	case WebComponentOperationRemove:
		op.Phase = WebComponentOperationRemoving
		op.ProgressPercent = 5
		op.Message = "Removing managed Jellyfin Web assets"
	default:
		op.Phase = WebComponentOperationPreparing
		op.ProgressPercent = 1
		op.Message = "Preparing Jellyfin Web install"
	}
}

func notifyWebOperationProgress(onProgress func(WebComponentOperationStatus), op *WebComponentOperationStatus) {
	if onProgress == nil || op == nil {
		return
	}
	onProgress(*op)
}

func updateWebOperationProgress(root, id string, phase WebComponentOperationPhase, progressPercent int, message string) *WebComponentOperationStatus {
	if id == "" {
		return nil
	}
	progressPercent = clampWebOperationProgress(progressPercent)

	webOperationsMu.Lock()
	op := webOperations[root]
	if op == nil || op.ID != id || op.State != WebComponentOperationRunning {
		webOperationsMu.Unlock()
		return nil
	}
	op.Phase = phase
	op.ProgressPercent = progressPercent
	op.Message = message
	copied := copyWebOperation(op)
	webOperationsMu.Unlock()

	_ = replaceWebOperationState(root, copied)
	return copied
}

func clampWebOperationProgress(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func beginWebOperation(root string, kind WebComponentOperationKind) (*WebComponentOperationStatus, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	host, _ := os.Hostname()
	op := &WebComponentOperationStatus{
		ID:        fmt.Sprintf("%s-%d", kind, now.UnixNano()),
		Kind:      kind,
		State:     WebComponentOperationRunning,
		PID:       os.Getpid(),
		Process:   currentProcessToken(),
		Host:      host,
		StartedAt: now.Format(time.RFC3339),
	}
	setInitialWebOperationProgress(op)

	webOperationsMu.Lock()
	defer webOperationsMu.Unlock()

	if existing := webOperations[root]; existing != nil && existing.State == WebComponentOperationRunning {
		return copyWebOperation(existing), ErrWebComponentOperationActive
	}

	if err := writeWebOperationState(root, op); err != nil {
		if os.IsExist(err) {
			existing := readWebOperationState(root)
			if isRecoverableWebOperation(existing) {
				if recoverErr := recoverStaleWebOperation(root, existing); recoverErr != nil {
					return copyWebOperation(existing), recoverErr
				}
				if err := writeWebOperationState(root, op); err != nil {
					if !os.IsExist(err) {
						return nil, err
					}
				} else {
					webOperations[root] = op
					return copyWebOperation(op), nil
				}
			}
			return copyWebOperation(existing), ErrWebComponentOperationActive
		}
		return nil, err
	}

	webOperations[root] = op
	return copyWebOperation(op), nil
}

func isRecoverableWebOperation(op *WebComponentOperationStatus) bool {
	if op == nil {
		return true
	}
	if op.malformed && webOperationLockAge(op) < webMalformedLockGrace {
		return false
	}
	if op.State != "" && op.State != WebComponentOperationRunning {
		return true
	}
	if op.PID <= 0 {
		return webOperationLockAge(op) >= webMalformedLockGrace
	}
	if op.Process != "" {
		if token := processToken(op.PID); token != "" {
			return op.Process != token
		}
	}
	if webOperationLockAge(op) >= webOperationStaleAge {
		return true
	}
	return !processIsRunning(op.PID)
}

func webOperationLockAge(op *WebComponentOperationStatus) time.Duration {
	if op == nil {
		return webOperationStaleAge
	}
	startedAt, err := time.Parse(time.RFC3339, op.StartedAt)
	if err == nil {
		return time.Since(startedAt)
	}
	if !op.lockMTime.IsZero() {
		return time.Since(op.lockMTime)
	}
	return webOperationStaleAge
}

func recoverStaleWebOperation(root string, op *WebComponentOperationStatus) error {
	operationLabel := "unknown Jellyfin Web operation"
	if op != nil && op.Kind != "" {
		operationLabel = fmt.Sprintf("stale Jellyfin Web %s operation", op.Kind)
	}
	err := fmt.Errorf("recovered %s lock after process restart", operationLabel)
	writeWebInstallError(root, err)
	id := ""
	if op != nil {
		id = op.ID
	}
	if !clearWebInstallStateForOperation(root, id) {
		return ErrWebComponentOperationActive
	}
	return nil
}

func processIsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return processIsRunningBySignal(pid)
}

func processIsRunningBySignal(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func currentProcessToken() string {
	return processToken(os.Getpid())
}

func processToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	stat := string(data)
	commEnd := strings.LastIndex(stat, ") ")
	if commEnd == -1 || commEnd+2 >= len(stat) {
		return ""
	}
	fields := strings.Fields(stat[commEnd+2:])
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}

func finishWebOperation(root, id string, err error) *WebComponentOperationStatus {
	now := time.Now().UTC().Format(time.RFC3339)

	webOperationsMu.Lock()
	op := webOperations[root]
	var copied *WebComponentOperationStatus
	if op != nil && op.ID == id {
		op.CompletedAt = now
		if err != nil {
			op.State = WebComponentOperationFailed
			op.Error = err.Error()
			op.Message = "Jellyfin Web operation failed"
		} else {
			op.State = WebComponentOperationSucceeded
			op.Error = ""
			op.ProgressPercent = 100
			if op.Kind == WebComponentOperationRemove {
				op.Message = "Jellyfin Web assets removed"
			} else {
				op.Message = "Jellyfin Web install complete"
			}
		}
		copied = copyWebOperation(op)
	}
	webOperationsMu.Unlock()

	if clearWebInstallStateForOperation(root, id) {
		if err != nil {
			writeWebInstallError(root, err)
		} else {
			clearWebInstallError(root)
		}
	}
	return copied
}

func currentWebOperation(root string) *WebComponentOperationStatus {
	webOperationsMu.Lock()
	op := copyWebOperation(webOperations[root])
	webOperationsMu.Unlock()
	if op != nil {
		return op
	}
	op = readWebOperationState(root)
	if op != nil && isRecoverableWebOperation(op) {
		if err := recoverStaleWebOperation(root, op); err == nil {
			return nil
		}
	}
	return op
}

func copyWebOperation(op *WebComponentOperationStatus) *WebComponentOperationStatus {
	if op == nil {
		return nil
	}
	copied := *op
	return &copied
}

func readWebOperationState(root string) *WebComponentOperationStatus {
	path := filepath.Join(root, webInstallLock)
	info, statErr := os.Stat(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var op WebComponentOperationStatus
	if err := json.Unmarshal(data, &op); err == nil && op.Kind != "" {
		if op.State == "" {
			op.State = WebComponentOperationRunning
		}
		if statErr == nil {
			op.lockMTime = info.ModTime()
		}
		return &op
	}

	legacy := strings.TrimSpace(string(data))
	if legacy == "" {
		legacy = string(WebComponentOperationInstall)
	}
	kind := WebComponentOperationInstall
	if strings.Contains(legacy, "remov") {
		kind = WebComponentOperationRemove
	}
	return &WebComponentOperationStatus{
		ID:        "legacy",
		Kind:      kind,
		State:     WebComponentOperationRunning,
		StartedAt: lockStartedAt(info, statErr),
		Error:     "",
		lockMTime: lockModifiedAt(info, statErr),
		malformed: true,
	}
}

func lockStartedAt(info os.FileInfo, err error) string {
	modified := lockModifiedAt(info, err)
	if modified.IsZero() {
		return ""
	}
	return modified.UTC().Format(time.RFC3339)
}

func lockModifiedAt(info os.FileInfo, err error) time.Time {
	if err != nil || info == nil {
		return time.Time{}
	}
	return info.ModTime()
}

func webComponentStatus(root, webDir, pinnedVersion, sourceURL string) WebComponentStatus {
	var statusError string

	normalizedRoot, err := normalizeWebInstallRoot(root)
	if err != nil {
		statusError = appendStatusError(statusError, err.Error())
		normalizedRoot = strings.TrimSpace(root)
		if normalizedRoot == "" {
			normalizedRoot = config.DefaultJellyfinWebInstallDir
		}
	}

	webDir = strings.TrimSpace(webDir)
	if webDir == "" {
		webDir = ManagedWebInstallPath(normalizedRoot)
	}
	managedWebDir, err := normalizeManagedWebInstallPath(normalizedRoot, webDir)
	if err != nil {
		statusError = appendStatusError(statusError, err.Error())
	}
	webDir = managedWebDir

	rawPinnedVersion := strings.TrimSpace(pinnedVersion)
	pinnedVersion = normalizeWebVersion(rawPinnedVersion)
	if pinnedVersion == "" {
		if rawPinnedVersion != "" {
			statusError = appendStatusError(statusError, fmt.Sprintf("invalid Jellyfin Web version %q", rawPinnedVersion))
		}
		pinnedVersion = config.DefaultJellyfinWebVersion
	}

	sourceURL, err = normalizeWebSourceURL(sourceURL)
	if err != nil {
		statusError = appendStatusError(statusError, err.Error())
		sourceURL = DefaultWebSourceURL
	}

	prereqs := CheckWebInstallerPrerequisites()
	installerReady := true
	for _, prereq := range prereqs {
		if !prereq.Available {
			installerReady = false
			break
		}
	}
	status := WebComponentStatus{
		WebEnabled:     true,
		WebState:       WebComponentMissing,
		PinnedVersion:  pinnedVersion,
		SourceURL:      sourceURL,
		InstallRoot:    normalizedRoot,
		InstallPath:    webDir,
		InstallerReady: installerReady,
		Prerequisites:  prereqs,
		LastError:      statusError,
	}

	status.Operation = currentWebOperation(normalizedRoot)
	operationRunning := status.Operation != nil && status.Operation.State == WebComponentOperationRunning
	if operationRunning {
		switch status.Operation.Kind {
		case WebComponentOperationRemove:
			status.WebState = WebComponentRemoving
		default:
			status.WebState = WebComponentInstalling
		}
	}

	status.LastError = appendStatusError(status.LastError, readWebLastError(normalizedRoot))
	if _, err := fs.Stat(os.DirFS(webDir), "index.html"); err != nil {
		if !operationRunning && status.LastError != "" {
			status.WebState = WebComponentFailed
		}
		return status
	}
	status.LicensePresent = filePresent(filepath.Join(webDir, "LICENSE"))
	status.ProvenancePresent = filePresent(filepath.Join(webDir, webMetadataFile)) &&
		filePresent(filepath.Join(webDir, webSourceFile))
	metadata, err := validateWebComponentDirectory(webDir)
	if err == nil {
		if !operationRunning {
			status.WebState = WebComponentInstalled
		}
		status.InstalledVersion = metadata.Version
		status.Tag = metadata.Tag
		status.CommitSHA = metadata.CommitSHA
		status.Checksum = metadata.Checksum
		status.InstalledAt = metadata.InstalledAt
		status.SourceURL = firstNonEmptyString(metadata.SourceURL, status.SourceURL)
		if !operationRunning && normalizeWebVersion(metadata.Version) != pinnedVersion {
			status.WebState = WebComponentUpdateAvailable
		}
		return status
	}
	status.LastError = appendStatusError(status.LastError, fmt.Sprintf("Jellyfin Web assets are not served because required provenance is missing or invalid: %v", err))
	if !operationRunning {
		status.WebState = WebComponentFailed
	}
	return status
}

func normalizeWebVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	if !webVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

func stringSetting(settings map[string]string, key, fallback string) string {
	if settings != nil {
		if value := strings.TrimSpace(settings[key]); value != "" {
			return value
		}
	}
	return fallback
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appendStatusError(existing, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return strings.TrimSpace(existing)
	}
	if strings.TrimSpace(existing) == "" {
		return next
	}
	return existing + "; " + next
}

func commandOutput(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func runWebInstallCommand(ctx context.Context, dir string, argv []string, stdin string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", strings.Join(argv, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func directoryChecksum(root string) (string, error) {
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, rel := range files {
		if _, err := hash.Write([]byte(rel + "\x00")); err != nil {
			return "", err
		}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(hash, file); err != nil {
			file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeWebMetadata(dir string, metadata WebComponentMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, webMetadataFile), data, 0o644)
}

func readWebMetadata(dir string) (WebComponentMetadata, error) {
	var metadata WebComponentMetadata
	data, err := os.ReadFile(filepath.Join(dir, webMetadataFile))
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func writeWebSourceFile(dir string, metadata WebComponentMetadata) error {
	body := fmt.Sprintf(`Jellyfin Web compatibility component

Source: %s
Tag: %s
Commit: %s
License: %s
Build: %s
Modified: %t
Checksum: %s

This component is separate from Silo's AGPL-licensed server code. It is installed only
when an administrator explicitly requests Jellyfin-compatible web UI assets.
`, metadata.SourceURL, metadata.Tag, metadata.CommitSHA, metadata.License, metadata.BuildCommand, metadata.Modified, metadata.Checksum)
	return os.WriteFile(filepath.Join(dir, webSourceFile), []byte(body), 0o644)
}

func activateWebComponent(root, version string) error {
	current := filepath.Join(root, "current")
	tmpLink := filepath.Join(root, ".current-next")
	_ = os.Remove(tmpLink)
	if err := os.Symlink(version, tmpLink); err != nil {
		return err
	}
	if err := os.Rename(tmpLink, current); err != nil {
		_ = os.Remove(tmpLink)
		return err
	}
	return nil
}

func writeWebOperationState(root string, op *WebComponentOperationStatus) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(root, ".install-lock-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, filepath.Join(root, webInstallLock)); err != nil {
		return err
	}
	return nil
}

func replaceWebOperationState(root string, op *WebComponentOperationStatus) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(root, ".install-lock-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(root, webInstallLock))
}

func clearWebInstallState(root string) {
	_ = os.Remove(filepath.Join(root, webInstallLock))
}

func clearWebInstallStateForOperation(root, id string) bool {
	current := readWebOperationState(root)
	if current == nil {
		clearWebInstallState(root)
		return true
	}
	if id != "" && current.ID != id {
		return false
	}
	if id == "" && !isRecoverableWebOperation(current) {
		return false
	}
	clearWebInstallState(root)
	return true
}

func writeWebInstallError(root string, err error) {
	if err == nil {
		return
	}
	_ = os.WriteFile(filepath.Join(root, webLastError), []byte(err.Error()), 0o644)
}

func clearWebInstallError(root string) {
	_ = os.Remove(filepath.Join(root, webLastError))
}

func readWebLastError(root string) string {
	data, err := os.ReadFile(filepath.Join(root, webLastError))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func filePresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

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
	ID          string                     `json:"id"`
	Kind        WebComponentOperationKind  `json:"kind"`
	State       WebComponentOperationState `json:"state"`
	PID         int                        `json:"pid,omitempty"`
	Process     string                     `json:"process,omitempty"`
	Host        string                     `json:"host,omitempty"`
	StartedAt   string                     `json:"started_at"`
	CompletedAt string                     `json:"completed_at,omitempty"`
	Error       string                     `json:"error,omitempty"`
}

type WebComponentStatus struct {
	Enabled           bool                         `json:"enabled"`
	APIState          string                       `json:"api_state"`
	Listen            string                       `json:"listen"`
	PublicURL         string                       `json:"public_url"`
	EmulatedVersion   string                       `json:"emulated_server_version"`
	ServerName        string                       `json:"server_name"`
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
}

type WebComponentInstallCompleteFunc func(context.Context, WebComponentStatus) error

func DefaultWebInstallRoot(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.JellyfinCompat.WebInstallDir) != "" {
		return strings.TrimSpace(cfg.JellyfinCompat.WebInstallDir)
	}
	return config.DefaultJellyfinWebInstallDir
}

func DefaultWebInstallPath(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.JellyfinCompat.WebDir) != "" {
		return strings.TrimSpace(cfg.JellyfinCompat.WebDir)
	}
	return filepath.Join(DefaultWebInstallRoot(cfg), "current")
}

func DefaultWebVersion(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.JellyfinCompat.WebVersion) != "" {
		return strings.TrimSpace(cfg.JellyfinCompat.WebVersion)
	}
	return config.DefaultJellyfinWebVersion
}

func WebComponentStatusForConfig(cfg *config.Config, settings map[string]string) WebComponentStatus {
	enabled := false
	if cfg != nil {
		enabled = cfg.JellyfinCompat.Enabled
	}
	if raw := strings.TrimSpace(settings["jellyfin_compat.enabled"]); raw != "" {
		enabled = strings.EqualFold(raw, "true") || raw == "1" || strings.EqualFold(raw, "yes")
	}

	root := stringSetting(settings, "jellyfin_compat.web_install_dir", DefaultWebInstallRoot(cfg))
	webDir := stringSetting(settings, "jellyfin_compat.web_dir", DefaultWebInstallPath(cfg))
	pinned := stringSetting(settings, "jellyfin_compat.web_version", DefaultWebVersion(cfg))
	sourceURL := stringSetting(settings, "jellyfin_compat.web_source_url", DefaultWebSourceURL)

	status := webComponentStatus(root, webDir, pinned, sourceURL)
	status.Enabled = enabled
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
			strings.TrimSpace(status.Listen) != strings.TrimSpace(cfg.JellyfinCompat.Listen) ||
			strings.TrimSpace(status.PublicURL) != strings.TrimSpace(cfg.JellyfinCompat.PublicURL) ||
			strings.TrimSpace(status.EmulatedVersion) != strings.TrimSpace(cfg.JellyfinCompat.EmulatedServerVersion) ||
			strings.TrimSpace(status.ServerName) != strings.TrimSpace(cfg.JellyfinCompat.ServerName)
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

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()

		installStatus, installErr := installWebComponentLocked(ctx, opts)
		if installErr == nil && onComplete != nil {
			installErr = onComplete(context.Background(), installStatus)
			if installErr != nil {
				writeWebInstallError(opts.InstallRoot, installErr)
			}
		}
		finishWebOperation(opts.InstallRoot, op.ID, installErr)
	}()

	return status, nil
}

func StartWebComponentRemove(root string) (WebComponentStatus, error) {
	root, err := normalizeWebInstallRoot(root)
	status := webComponentStatus(root, filepath.Join(root, "current"), "", "")
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

	go func() {
		err := removeWebComponentLocked(root)
		finishWebOperation(root, op.ID, err)
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

	installStatus, err := installWebComponentLocked(ctx, opts)
	finishWebOperation(opts.InstallRoot, op.ID, err)
	return installStatus, err
}

func installWebComponentLocked(ctx context.Context, opts WebComponentInstallOptions) (WebComponentStatus, error) {
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

	if err := os.MkdirAll(root, 0o755); err != nil {
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}

	tmpRoot, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	defer os.RemoveAll(tmpRoot)

	srcDir := filepath.Join(tmpRoot, "src")
	tag := "v" + version
	if err := run(ctx, "", []string{"git", "clone", "--depth", "1", "--branch", tag, sourceURL, srcDir}, ""); err != nil {
		err = fmt.Errorf("clone jellyfin-web %s: %w", tag, err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	commitSHA, err := commandOutput(ctx, srcDir, "git", "rev-parse", "HEAD")
	if err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := run(ctx, srcDir, []string{"npm", "ci"}, ""); err != nil {
		err = fmt.Errorf("install jellyfin-web dependencies: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := run(ctx, srcDir, []string{"npm", "run", "build:production"}, ""); err != nil {
		err = fmt.Errorf("build jellyfin-web production bundle: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}

	releaseDir := filepath.Join(root, version)
	stagedDir := filepath.Join(tmpRoot, version)
	if err := copyDir(filepath.Join(srcDir, "dist"), stagedDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := copyFile(filepath.Join(srcDir, "LICENSE"), filepath.Join(stagedDir, "LICENSE")); err != nil {
		err = fmt.Errorf("copy jellyfin-web license: %w", err)
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	checksum, err := directoryChecksum(stagedDir)
	if err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
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
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := writeWebSourceFile(stagedDir, metadata); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if !filePresent(filepath.Join(stagedDir, "LICENSE")) || !filePresent(filepath.Join(stagedDir, webMetadataFile)) || !filePresent(filepath.Join(stagedDir, webSourceFile)) {
		err := errors.New("jellyfin-web install is missing required license or provenance files")
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := os.RemoveAll(releaseDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := os.Rename(stagedDir, releaseDir); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	if err := activateWebComponent(root, version); err != nil {
		writeWebInstallError(root, err)
		return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), err
	}
	clearWebInstallError(root)
	return webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL), nil
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

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)
		if name == "current" || name == ".current-next" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				writeWebInstallError(root, err)
				return err
			}
			continue
		}
		if entry.IsDir() && strings.HasPrefix(name, ".install-") {
			if err := os.RemoveAll(path); err != nil {
				writeWebInstallError(root, err)
				return err
			}
			continue
		}
		if entry.IsDir() && filePresent(filepath.Join(path, webMetadataFile)) && filePresent(filepath.Join(path, webSourceFile)) {
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
	status := webComponentStatus(root, filepath.Join(root, "current"), opts.Version, opts.SourceURL)
	if err != nil {
		status.LastError = err.Error()
		return opts, status, err
	}

	version, err := normalizeRequiredWebVersion(opts.Version)
	if err != nil {
		status.InstallRoot = root
		status.InstallPath = filepath.Join(root, "current")
		status.LastError = err.Error()
		return opts, status, err
	}

	sourceURL, err := normalizeWebSourceURL(opts.SourceURL)
	if err != nil {
		status.InstallRoot = root
		status.InstallPath = filepath.Join(root, "current")
		status.LastError = err.Error()
		return opts, status, err
	}

	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.InstallRoot = root
	opts.Version = version
	opts.SourceURL = sourceURL
	status = webComponentStatus(root, filepath.Join(root, "current"), version, sourceURL)
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

	webOperationsMu.Lock()
	defer webOperationsMu.Unlock()

	if existing := webOperations[root]; existing != nil && existing.State == WebComponentOperationRunning {
		return copyWebOperation(existing), ErrWebComponentOperationActive
	}

	file, err := os.OpenFile(filepath.Join(root, webInstallLock), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			existing := readWebOperationState(root)
			if isRecoverableWebOperation(existing) {
				if recoverErr := recoverStaleWebOperation(root, existing); recoverErr != nil {
					return copyWebOperation(existing), recoverErr
				}
				file, err = os.OpenFile(filepath.Join(root, webInstallLock), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err == nil {
					goto writeOperation
				}
				if !os.IsExist(err) {
					return nil, err
				}
			}
			return copyWebOperation(existing), ErrWebComponentOperationActive
		}
		return nil, err
	}

writeOperation:
	if err := json.NewEncoder(file).Encode(op); err != nil {
		_ = file.Close()
		_ = os.Remove(filepath.Join(root, webInstallLock))
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(filepath.Join(root, webInstallLock))
		return nil, err
	}

	webOperations[root] = op
	return copyWebOperation(op), nil
}

func isRecoverableWebOperation(op *WebComponentOperationStatus) bool {
	if op == nil {
		return true
	}
	if op.State != "" && op.State != WebComponentOperationRunning {
		return true
	}
	if op.PID <= 0 {
		return true
	}
	if op.Process != "" {
		if token := processToken(op.PID); token != "" {
			return op.Process != token
		}
	}
	return !processIsRunning(op.PID)
}

func recoverStaleWebOperation(root string, op *WebComponentOperationStatus) error {
	operationLabel := "unknown Jellyfin Web operation"
	if op != nil && op.Kind != "" {
		operationLabel = fmt.Sprintf("stale Jellyfin Web %s operation", op.Kind)
	}
	err := fmt.Errorf("recovered %s lock after process restart", operationLabel)
	writeWebInstallError(root, err)
	clearWebInstallState(root)
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

func finishWebOperation(root, id string, err error) {
	now := time.Now().UTC().Format(time.RFC3339)

	webOperationsMu.Lock()
	op := webOperations[root]
	if op != nil && op.ID == id {
		op.CompletedAt = now
		if err != nil {
			op.State = WebComponentOperationFailed
			op.Error = err.Error()
		} else {
			op.State = WebComponentOperationSucceeded
			op.Error = ""
		}
	}
	webOperationsMu.Unlock()

	clearWebInstallState(root)
	if err != nil {
		writeWebInstallError(root, err)
	} else {
		clearWebInstallError(root)
	}
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
		_ = recoverStaleWebOperation(root, op)
		return nil
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
	data, err := os.ReadFile(filepath.Join(root, webInstallLock))
	if err != nil {
		return nil
	}
	var op WebComponentOperationStatus
	if err := json.Unmarshal(data, &op); err == nil && op.Kind != "" {
		if op.State == "" {
			op.State = WebComponentOperationRunning
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
		ID:    "legacy",
		Kind:  kind,
		State: WebComponentOperationRunning,
		Error: "",
	}
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
		webDir = filepath.Join(normalizedRoot, "current")
	}

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
	if !operationRunning {
		status.WebState = WebComponentInstalled
	}
	metadata, err := readWebMetadata(webDir)
	if err == nil {
		status.InstalledVersion = metadata.Version
		status.Tag = metadata.Tag
		status.CommitSHA = metadata.CommitSHA
		status.Checksum = metadata.Checksum
		status.InstalledAt = metadata.InstalledAt
		status.SourceURL = firstNonEmptyString(metadata.SourceURL, status.SourceURL)
		if !operationRunning && normalizeWebVersion(metadata.Version) != pinnedVersion {
			status.WebState = WebComponentUpdateAvailable
		}
	}
	status.LicensePresent = filePresent(filepath.Join(webDir, "LICENSE"))
	status.ProvenancePresent = filePresent(filepath.Join(webDir, webMetadataFile)) &&
		filePresent(filepath.Join(webDir, webSourceFile))
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

func markWebInstallState(root, value string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, webInstallLock), []byte(value), 0o644)
}

func clearWebInstallState(root string) {
	_ = os.Remove(filepath.Join(root, webInstallLock))
}

func isWebInstalling(root string) bool {
	return filePresent(filepath.Join(root, webInstallLock))
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

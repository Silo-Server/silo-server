package plugins

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"

	"google.golang.org/protobuf/encoding/protojson"
)

type InstallArchiveRequest struct {
	ArchivePath  string
	ArchiveURL   string
	RepositoryID *int
	// PluginID and Version, when set, are what the catalog lists; see
	// InstallBinaryRequest.
	PluginID string
	Version  string
}

type InstallBinaryRequest struct {
	BinaryURL    string
	Checksum     string // expected SHA-256 hex checksum
	Manifest     *pluginv1.PluginManifest
	RepositoryID *int
	// PluginID and Version, when set, are what the catalog lists. A binary
	// whose own manifest names another plugin or version is refused, so a
	// catalog entry cannot install or replace a different plugin.
	PluginID string
	Version  string
}

type InstallResult struct {
	Installation *Installation
	Manifest     *pluginv1.PluginManifest
	BinaryPath   string
	ManifestPath string
}

type InstallerOptions struct {
	BaseDir    string
	HTTPClient *http.Client
}

type installationStore interface {
	Create(ctx context.Context, input CreateInstallationInput) (*Installation, error)
	SaveArchive(ctx context.Context, installationID int, manifestJSON []byte, checksum string, archiveBytes []byte) error
	Update(ctx context.Context, id int, input UpdateInstallationInput) error
	Delete(ctx context.Context, id int) error
}

type Installer struct {
	installations installationStore
	baseDir       string
	httpClient    *http.Client
}

func NewInstaller(installations installationStore, opts InstallerOptions) *Installer {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	baseDir := opts.BaseDir
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "silo-plugins")
	}
	return &Installer{
		installations: installations,
		baseDir:       baseDir,
		httpClient:    httpClient,
	}
}

func (i *Installer) InstallLocal(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive path is required")
	}
	data, err := os.ReadFile(req.ArchivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive %q: %w", req.ArchivePath, err)
	}
	return i.installArchive(ctx, data, req)
}

func (i *Installer) ReplaceLocal(ctx context.Context, existing *Installation, req InstallArchiveRequest) (*InstallResult, error) {
	if existing == nil {
		return nil, fmt.Errorf("existing installation is required")
	}
	if req.ArchivePath == "" {
		return nil, fmt.Errorf("archive path is required")
	}
	data, err := os.ReadFile(req.ArchivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive %q: %w", req.ArchivePath, err)
	}
	return i.replaceArchive(ctx, existing, data, req)
}

func (i *Installer) InstallRemote(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchiveURL == "" {
		return nil, fmt.Errorf("archive url is required")
	}
	data, err := i.downloadArchive(ctx, req.ArchiveURL)
	if err != nil {
		return nil, err
	}
	return i.installArchive(ctx, data, req)
}

func (i *Installer) ReplaceRemote(ctx context.Context, existing *Installation, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchiveURL == "" {
		return nil, fmt.Errorf("archive url is required")
	}
	data, err := i.downloadArchive(ctx, req.ArchiveURL)
	if err != nil {
		return nil, err
	}
	return i.replaceArchive(ctx, existing, data, req)
}

func (i *Installer) downloadArchive(ctx context.Context, archiveURL string) ([]byte, error) {
	return fetchPackageResource(ctx, i.httpClient, archiveURL, "the plugin")
}

func (i *Installer) InstallBinary(ctx context.Context, req InstallBinaryRequest) (*InstallResult, error) {
	if req.BinaryURL == "" {
		return nil, fmt.Errorf("binary url is required")
	}
	if req.Checksum == "" {
		return nil, fmt.Errorf("binary checksum is required")
	}
	binaryData, manifest, actualChecksum, err := i.downloadBinary(ctx, req)
	if err != nil {
		return nil, err
	}
	return i.installBinary(ctx, binaryData, actualChecksum, manifest, req.RepositoryID)
}

func (i *Installer) ReplaceBinary(ctx context.Context, existing *Installation, req InstallBinaryRequest) (*InstallResult, error) {
	if req.BinaryURL == "" {
		return nil, fmt.Errorf("binary url is required")
	}
	if req.Checksum == "" {
		return nil, fmt.Errorf("binary checksum is required")
	}
	binaryData, manifest, actualChecksum, err := i.downloadBinary(ctx, req)
	if err != nil {
		return nil, err
	}
	return i.replaceBinary(ctx, existing, binaryData, actualChecksum, manifest, req.RepositoryID)
}

func (i *Installer) downloadBinary(
	ctx context.Context,
	req InstallBinaryRequest,
) ([]byte, *pluginv1.PluginManifest, string, error) {
	binaryData, err := fetchPackageResource(ctx, i.httpClient, req.BinaryURL, "the plugin")
	if err != nil {
		return nil, nil, "", err
	}

	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])
	if actualChecksum != req.Checksum {
		return nil, nil, "", packageError(fmt.Errorf("binary checksum mismatch: expected %s, got %s", req.Checksum, actualChecksum),
			"The downloaded plugin doesn't match the SHA-256 checksum its catalog lists, so Silo didn't install it.")
	}

	manifest := req.Manifest
	if manifest == nil {
		manifest, err = loadManifestFromBinary(ctx, binaryData)
		if err != nil {
			return nil, nil, "", err
		}
	} else if err := ValidateManifest(manifest); err != nil {
		return nil, nil, "", err
	}
	if err := checkCatalogIdentity(manifest, req.PluginID, req.Version); err != nil {
		return nil, nil, "", err
	}

	return binaryData, manifest, actualChecksum, nil
}

func (i *Installer) InstallBinaryUpload(ctx context.Context, binaryData []byte) (*InstallResult, error) {
	if len(binaryData) == 0 {
		return nil, fmt.Errorf("binary data is required")
	}

	manifest, err := loadManifestFromBinary(ctx, binaryData)
	if err != nil {
		return nil, err
	}

	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])

	return i.installBinary(ctx, binaryData, actualChecksum, manifest, nil)
}

// replaceBinary replaces existing in place with a binary from repositoryID,
// nil for an uploaded binary. The row then names the new source: trust that
// depends on where a package came from, such as a first-party watch provider
// keeping its built-in key, must not carry over to a package from elsewhere.
func (i *Installer) replaceBinary(
	ctx context.Context,
	existing *Installation,
	binaryData []byte,
	checksum string,
	manifest *pluginv1.PluginManifest,
	repositoryID *int,
) (*InstallResult, error) {
	if existing == nil {
		return nil, fmt.Errorf("existing installation is required")
	}
	if len(binaryData) == 0 {
		return nil, fmt.Errorf("binary data is required")
	}
	if checksum == "" {
		return nil, fmt.Errorf("binary checksum is required")
	}
	if manifest == nil {
		return nil, fmt.Errorf("plugin manifest is required")
	}
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}

	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize plugin manifest: %w", err)
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
	if err != nil {
		return nil, fmt.Errorf("package plugin archive: %w", err)
	}

	installRoot := filepath.Join(i.baseDir, sanitizeFilesystemSegment(manifest.GetPluginId()), sanitizeFilesystemSegment(manifest.GetVersion()))
	if err := os.MkdirAll(installRoot, 0755); err != nil {
		return nil, fmt.Errorf("create plugin install root %q: %w", installRoot, err)
	}

	installDir, err := os.MkdirTemp(installRoot, "install-")
	if err != nil {
		return nil, fmt.Errorf("create plugin install dir in %q: %w", installRoot, err)
	}
	keepInstallDir := false
	defer func() {
		if keepInstallDir {
			return
		}
		_ = os.RemoveAll(installDir)
	}()

	binaryPath := filepath.Join(installDir, "plugin")
	manifestPath := filepath.Join(installDir, "manifest.json")

	if err := os.WriteFile(binaryPath, binaryData, 0755); err != nil {
		return nil, fmt.Errorf("write plugin binary %q: %w", binaryPath, err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
		return nil, fmt.Errorf("write plugin manifest %q: %w", manifestPath, err)
	}

	capabilities, err := CapabilityRecordsFromManifest(manifest)
	if err != nil {
		return nil, err
	}

	if err := i.installations.SaveArchive(ctx, existing.ID, manifestBytes, checksum, archiveBytes); err != nil {
		return nil, fmt.Errorf("persist replacement plugin archive: %w", err)
	}

	version := manifest.GetVersion()
	enabled := existing.Enabled
	availableVersion := ""
	if err := i.installations.Update(ctx, existing.ID, UpdateInstallationInput{
		Version:          &version,
		InstallPath:      &binaryPath,
		Enabled:          &enabled,
		AvailableVersion: &availableVersion,
		Capabilities:     capabilities,
		SetRepository:    true,
		RepositoryID:     repositoryID,
	}); err != nil {
		return nil, fmt.Errorf("update replacement plugin installation: %w", err)
	}

	oldInstallDir := filepath.Dir(existing.InstallPath)
	if oldInstallDir != "" && filepath.Clean(oldInstallDir) != filepath.Clean(installDir) {
		_ = os.RemoveAll(oldInstallDir)
	}

	keepInstallDir = true

	updated := *existing
	updated.Version = version
	updated.InstallPath = binaryPath
	updated.Enabled = enabled
	updated.RepositoryID = repositoryID

	return &InstallResult{
		Installation: &updated,
		Manifest:     manifest,
		BinaryPath:   binaryPath,
		ManifestPath: manifestPath,
	}, nil
}

func (i *Installer) replaceArchive(
	ctx context.Context,
	existing *Installation,
	data []byte,
	req InstallArchiveRequest,
) (*InstallResult, error) {
	if existing == nil {
		return nil, fmt.Errorf("existing installation is required")
	}
	repositoryID := req.RepositoryID

	reader, manifestBytes, manifest, err := openInstallArchive(data, req)
	if err != nil {
		return nil, err
	}

	installRoot := filepath.Join(i.baseDir, sanitizeFilesystemSegment(manifest.GetPluginId()), sanitizeFilesystemSegment(manifest.GetVersion()))
	if err := os.MkdirAll(installRoot, 0755); err != nil {
		return nil, fmt.Errorf("create plugin install root %q: %w", installRoot, err)
	}

	installDir, err := os.MkdirTemp(installRoot, "install-")
	if err != nil {
		return nil, fmt.Errorf("create plugin install dir in %q: %w", installRoot, err)
	}
	keepInstallDir := false
	defer func() {
		if keepInstallDir {
			return
		}
		_ = os.RemoveAll(installDir)
	}()

	binaryPath := filepath.Join(installDir, "plugin")
	manifestPath := filepath.Join(installDir, "manifest.json")
	capabilities, err := CapabilityRecordsFromManifest(manifest)
	if err != nil {
		return nil, err
	}

	if err := i.installations.SaveArchive(ctx, existing.ID, manifestBytes, manifest.GetChecksum(), data); err != nil {
		return nil, fmt.Errorf("persist replacement plugin archive: %w", err)
	}

	if err := extractArchiveFiles(reader, installDir); err != nil {
		return nil, err
	}

	version := manifest.GetVersion()
	enabled := existing.Enabled
	availableVersion := ""
	if err := i.installations.Update(ctx, existing.ID, UpdateInstallationInput{
		Version:          &version,
		InstallPath:      &binaryPath,
		Enabled:          &enabled,
		AvailableVersion: &availableVersion,
		Capabilities:     capabilities,
		SetRepository:    true,
		RepositoryID:     repositoryID,
	}); err != nil {
		return nil, fmt.Errorf("update replacement plugin installation: %w", err)
	}

	oldInstallDir := filepath.Dir(existing.InstallPath)
	if oldInstallDir != "" && filepath.Clean(oldInstallDir) != filepath.Clean(installDir) {
		_ = os.RemoveAll(oldInstallDir)
	}

	keepInstallDir = true

	updated := *existing
	updated.Version = version
	updated.InstallPath = binaryPath
	updated.Enabled = enabled
	updated.RepositoryID = repositoryID

	return &InstallResult{
		Installation: &updated,
		Manifest:     manifest,
		BinaryPath:   binaryPath,
		ManifestPath: manifestPath,
	}, nil
}

func (i *Installer) installBinary(ctx context.Context, binaryData []byte, checksum string, manifest *pluginv1.PluginManifest, repositoryID *int) (*InstallResult, error) {
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize plugin manifest: %w", err)
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
	if err != nil {
		return nil, fmt.Errorf("package plugin archive: %w", err)
	}

	installRoot := filepath.Join(i.baseDir, sanitizeFilesystemSegment(manifest.GetPluginId()), sanitizeFilesystemSegment(manifest.GetVersion()))
	if err := os.MkdirAll(installRoot, 0755); err != nil {
		return nil, fmt.Errorf("create plugin install root %q: %w", installRoot, err)
	}

	installDir, err := os.MkdirTemp(installRoot, "install-")
	if err != nil {
		return nil, fmt.Errorf("create plugin install dir in %q: %w", installRoot, err)
	}
	keepInstallDir := false
	defer func() {
		if keepInstallDir {
			return
		}
		_ = os.RemoveAll(installDir)
	}()

	binaryPath := filepath.Join(installDir, "plugin")
	manifestPath := filepath.Join(installDir, "manifest.json")

	if err := os.WriteFile(binaryPath, binaryData, 0755); err != nil {
		return nil, fmt.Errorf("write plugin binary %q: %w", binaryPath, err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
		return nil, fmt.Errorf("write plugin manifest %q: %w", manifestPath, err)
	}

	capabilities, err := CapabilityRecordsFromManifest(manifest)
	if err != nil {
		return nil, err
	}

	createInput := CreateInstallationInput{
		PluginID:     manifest.GetPluginId(),
		Version:      manifest.GetVersion(),
		InstallPath:  binaryPath,
		Enabled:      false,
		Capabilities: capabilities,
	}
	if repositoryID != nil {
		createInput.RepositoryID = *repositoryID
	}

	installation, err := i.installations.Create(ctx, createInput)
	if err != nil {
		return nil, fmt.Errorf("persist plugin installation: %w", err)
	}

	if err := i.installations.SaveArchive(ctx, installation.ID, manifestBytes, checksum, archiveBytes); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("persist plugin archive: %w (cleanup failed: %v)", err, deleteErr)
		}
		return nil, fmt.Errorf("persist plugin archive: %w", err)
	}

	enabled := true
	if err := i.installations.Update(ctx, installation.ID, UpdateInstallationInput{
		Enabled: &enabled,
	}); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("enable plugin installation: %w (cleanup failed: %v)", err, deleteErr)
		}
		return nil, fmt.Errorf("enable plugin installation: %w", err)
	}
	installation.Enabled = true

	keepInstallDir = true

	return &InstallResult{
		Installation: installation,
		Manifest:     manifest,
		BinaryPath:   binaryPath,
		ManifestPath: manifestPath,
	}, nil
}

func (i *Installer) installArchive(ctx context.Context, data []byte, req InstallArchiveRequest) (*InstallResult, error) {
	repositoryID := req.RepositoryID
	reader, manifestBytes, manifest, err := openInstallArchive(data, req)
	if err != nil {
		return nil, err
	}

	installRoot := filepath.Join(i.baseDir, sanitizeFilesystemSegment(manifest.GetPluginId()), sanitizeFilesystemSegment(manifest.GetVersion()))
	if err := os.MkdirAll(installRoot, 0755); err != nil {
		return nil, fmt.Errorf("create plugin install root %q: %w", installRoot, err)
	}

	installDir, err := os.MkdirTemp(installRoot, "install-")
	if err != nil {
		return nil, fmt.Errorf("create plugin install dir in %q: %w", installRoot, err)
	}
	keepInstallDir := false
	defer func() {
		if keepInstallDir {
			return
		}
		_ = os.RemoveAll(installDir)
	}()

	binaryPath := filepath.Join(installDir, "plugin")
	manifestPath := filepath.Join(installDir, "manifest.json")
	capabilities, err := CapabilityRecordsFromManifest(manifest)
	if err != nil {
		return nil, err
	}

	createInput := CreateInstallationInput{
		PluginID:     manifest.GetPluginId(),
		Version:      manifest.GetVersion(),
		InstallPath:  binaryPath,
		Enabled:      false,
		Capabilities: capabilities,
	}
	if repositoryID != nil {
		createInput.RepositoryID = *repositoryID
	}

	installation, err := i.installations.Create(ctx, createInput)
	if err != nil {
		return nil, fmt.Errorf("persist plugin installation: %w", err)
	}

	if err := i.installations.SaveArchive(ctx, installation.ID, manifestBytes, manifest.GetChecksum(), data); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("persist plugin archive: %w (cleanup failed: %v)", err, deleteErr)
		}
		return nil, fmt.Errorf("persist plugin archive: %w", err)
	}

	if err := extractArchiveFiles(reader, installDir); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("extract plugin archive: %w (cleanup failed: %v)", err, deleteErr)
		}
		return nil, err
	}

	enabled := true
	if err := i.installations.Update(ctx, installation.ID, UpdateInstallationInput{
		Enabled: &enabled,
	}); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("enable plugin installation: %w (cleanup failed: %v)", err, deleteErr)
		}
		return nil, fmt.Errorf("enable plugin installation: %w", err)
	}
	installation.Enabled = true

	keepInstallDir = true

	return &InstallResult{
		Installation: installation,
		Manifest:     manifest,
		BinaryPath:   binaryPath,
		ManifestPath: manifestPath,
	}, nil
}

func LoadManifestBytes(data []byte) (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(data)
	if err != nil {
		return nil, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func loadManifestFromBinary(ctx context.Context, binaryData []byte) (*pluginv1.PluginManifest, error) {
	tmpDir, err := os.MkdirTemp("", "plugin-manifest-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir for binary manifest: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpBinary := filepath.Join(tmpDir, "plugin")
	if err := os.WriteFile(tmpBinary, binaryData, 0755); err != nil {
		return nil, fmt.Errorf("write temp binary for manifest: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, tmpBinary, "manifest")
	manifestOut, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("execute plugin manifest command: %w", ctx.Err())
		}
		runErr := fmt.Errorf("execute plugin manifest command: %w", err)
		// Only a binary that ran and failed (or timed out), or that isn't an
		// executable for this platform, is the package's fault. Any other
		// start failure, such as a noexec temp directory, is the server's.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) && !errors.Is(err, syscall.ENOEXEC) {
			return nil, runErr
		}
		return nil, packageError(runErr,
			"Silo couldn't run the plugin to read its manifest. It may be built for another platform, or the file may be damaged.")
	}

	var manifest pluginv1.PluginManifest
	if err := protojson.Unmarshal(manifestOut, &manifest); err != nil {
		return nil, packageError(fmt.Errorf("parse plugin manifest output: %w", err),
			"The plugin's manifest couldn't be read.")
	}
	if err := ValidateManifest(&manifest); err != nil {
		return nil, packageError(err, "The plugin's manifest is invalid: %v.", err)
	}
	if manifest.GetSiloApiVersion() != DefaultSiloAPIVersion {
		return nil, unsupportedAPIVersionError(manifest.GetSiloApiVersion(), DefaultSiloAPIVersion)
	}
	return &manifest, nil
}

func extractZipFile(file *zip.File, root string) error {
	if err := validateRelativePath(file.Name, "plugin archive"); err != nil {
		return err
	}

	targetPath := filepath.Join(root, filepath.Clean(file.Name))
	if file.FileInfo().IsDir() {
		if err := os.MkdirAll(targetPath, 0755); err != nil {
			return fmt.Errorf("create plugin directory %q: %w", targetPath, err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("create plugin parent dir %q: %w", targetPath, err)
	}

	data, err := readZipFile(file)
	if err != nil {
		return err
	}

	mode := os.FileMode(0644)
	if filepath.Base(file.Name) == "plugin" {
		mode = 0755
	}
	if err := os.WriteFile(targetPath, data, mode); err != nil {
		return fmt.Errorf("write plugin file %q: %w", targetPath, err)
	}
	return nil
}

func readZipFile(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open plugin archive entry %q: %w", file.Name, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read plugin archive entry %q: %w", file.Name, err)
	}
	return data, nil
}

func sanitizeFilesystemSegment(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_")
	return replacer.Replace(value)
}

// checkCatalogIdentity refuses a binary whose own manifest is not the plugin
// and version the catalog listed. An empty expectation skips that half.
func checkCatalogIdentity(manifest *pluginv1.PluginManifest, pluginID, version string) error {
	if (pluginID == "" || manifest.GetPluginId() == pluginID) && (version == "" || manifest.GetVersion() == version) {
		return nil
	}
	return packageError(
		fmt.Errorf("binary manifest is %s@%s, catalog lists %s@%s", manifest.GetPluginId(), manifest.GetVersion(), pluginID, version),
		"The downloaded plugin identifies itself as %s %s, not the %s %s its catalog lists, so Silo didn't install it.",
		manifest.GetPluginId(), manifest.GetVersion(), pluginID, version,
	)
}

// openInstallArchive opens an archive being installed and refuses one the
// server can't run or that isn't the plugin its catalog lists. Stored
// archives of existing installations are opened with openPluginArchive.
func openInstallArchive(data []byte, req InstallArchiveRequest) (*zip.Reader, []byte, *pluginv1.PluginManifest, error) {
	reader, manifestBytes, manifest, err := openPluginArchive(data)
	if err != nil {
		return nil, nil, nil, packageError(err, "The plugin archive couldn't be read.")
	}
	if manifest.GetSiloApiVersion() != DefaultSiloAPIVersion {
		return nil, nil, nil, unsupportedAPIVersionError(manifest.GetSiloApiVersion(), DefaultSiloAPIVersion)
	}
	if err := checkCatalogIdentity(manifest, req.PluginID, req.Version); err != nil {
		return nil, nil, nil, err
	}
	return reader, manifestBytes, manifest, nil
}

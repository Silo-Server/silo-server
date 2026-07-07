package plugins

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"

	"google.golang.org/protobuf/encoding/protojson"
)

type InstallArchiveRequest struct {
	ArchivePath  string
	ArchiveURL   string
	RepositoryID *int
}

type InstallBinaryRequest struct {
	BinaryURL    string
	Checksum     string // expected SHA-256 hex checksum
	Manifest     *pluginv1.PluginManifest
	RepositoryID *int
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
	return i.installArchive(ctx, data, req.RepositoryID)
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
	return i.replaceArchive(ctx, existing, data, req.RepositoryID)
}

func (i *Installer) InstallRemote(ctx context.Context, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchiveURL == "" {
		return nil, fmt.Errorf("archive url is required")
	}
	data, err := i.downloadArchive(ctx, req.ArchiveURL)
	if err != nil {
		return nil, err
	}
	return i.installArchive(ctx, data, req.RepositoryID)
}

func (i *Installer) ReplaceRemote(ctx context.Context, existing *Installation, req InstallArchiveRequest) (*InstallResult, error) {
	if req.ArchiveURL == "" {
		return nil, fmt.Errorf("archive url is required")
	}
	data, err := i.downloadArchive(ctx, req.ArchiveURL)
	if err != nil {
		return nil, err
	}
	return i.replaceArchive(ctx, existing, data, req.RepositoryID)
}

func (i *Installer) downloadArchive(ctx context.Context, archiveURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build archive request: %w", err)
	}
	resp, err := i.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download archive %q: %w", archiveURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download archive %q: unexpected status %d", archiveURL, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read archive response: %w", err)
	}
	return data, nil
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
	return i.replaceBinary(ctx, existing, binaryData, actualChecksum, manifest)
}

func (i *Installer) downloadBinary(
	ctx context.Context,
	req InstallBinaryRequest,
) ([]byte, *pluginv1.PluginManifest, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, req.BinaryURL, nil)
	if err != nil {
		return nil, nil, "", fmt.Errorf("build binary request: %w", err)
	}
	resp, err := i.httpClient.Do(request)
	if err != nil {
		return nil, nil, "", fmt.Errorf("download binary %q: %w", req.BinaryURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, "", fmt.Errorf("download binary %q: unexpected status %d", req.BinaryURL, resp.StatusCode)
	}

	binaryData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read binary response: %w", err)
	}

	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])
	if actualChecksum != req.Checksum {
		return nil, nil, "", fmt.Errorf("binary checksum mismatch: expected %s, got %s", req.Checksum, actualChecksum)
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

func (i *Installer) replaceBinary(
	ctx context.Context,
	existing *Installation,
	binaryData []byte,
	checksum string,
	manifest *pluginv1.PluginManifest,
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
	repositoryID *int,
) (*InstallResult, error) {
	if existing == nil {
		return nil, fmt.Errorf("existing installation is required")
	}

	reader, manifestBytes, manifest, err := openPluginArchive(data)
	if err != nil {
		return nil, err
	}
	_ = repositoryID

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

func (i *Installer) installArchive(ctx context.Context, data []byte, repositoryID *int) (*InstallResult, error) {
	reader, manifestBytes, manifest, err := openPluginArchive(data)
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
		return nil, fmt.Errorf("execute plugin manifest command: %w", err)
	}

	var manifest pluginv1.PluginManifest
	if err := protojson.Unmarshal(manifestOut, &manifest); err != nil {
		return nil, fmt.Errorf("parse plugin manifest output: %w", err)
	}
	if err := ValidateManifest(&manifest); err != nil {
		return nil, err
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

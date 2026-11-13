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

	"github.com/Silo-Server/silo-server/internal/pluginhost"
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
	RepositoryID *int
	// PluginID and Version, when set, are what the catalog lists. A binary
	// whose own manifest names another plugin or version is refused, so a
	// catalog entry cannot install or replace a different plugin. They also
	// select the release signature policy.
	PluginID string
	Version  string
	// BundleURL is the release's Sigstore bundle; empty when the catalog
	// lists none.
	BundleURL string
	// SourceRepoURL is the catalog's repo_url, which the advisory signature
	// policy checks a third-party release against.
	SourceRepoURL string
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
	// Signatures is the release signature policy, shared with the catalog
	// service. Nil checks signatures without enforcing them.
	Signatures *SignatureChecker
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
	signatures    *SignatureChecker
}

func NewInstaller(installations installationStore, opts InstallerOptions) *Installer {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = newPackageFetchClient(defaultPackageFetchTimeouts)
	}
	baseDir := opts.BaseDir
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "silo-plugins")
	}
	signatures := opts.Signatures
	if signatures == nil {
		signatures = NewSignatureChecker(SignatureCheckerOptions{CacheRoot: baseDir})
	}
	return &Installer{
		installations: installations,
		baseDir:       baseDir,
		httpClient:    httpClient,
		signatures:    signatures,
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
	pkg, err := i.downloadBinary(ctx, req)
	if err != nil {
		return nil, err
	}
	return i.installBinary(ctx, pkg.data, pkg.checksum, pkg.manifest, pkg.manifestJSON, req.RepositoryID, pkg.signature)
}

func (i *Installer) ReplaceBinary(ctx context.Context, existing *Installation, req InstallBinaryRequest) (*InstallResult, error) {
	if req.BinaryURL == "" {
		return nil, fmt.Errorf("binary url is required")
	}
	if req.Checksum == "" {
		return nil, fmt.Errorf("binary checksum is required")
	}
	pkg, err := i.downloadBinary(ctx, req)
	if err != nil {
		return nil, err
	}
	return i.replaceBinary(ctx, existing, pkg.data, pkg.checksum, pkg.manifest, pkg.manifestJSON, req.RepositoryID, pkg.signature)
}

// downloadedBinary is a catalog binary that passed every check before
// installation.
type downloadedBinary struct {
	data         []byte
	checksum     string
	manifest     *pluginv1.PluginManifest
	manifestJSON []byte
	signature    Signature
}

// downloadBinary downloads a catalog binary and checks it in an order that
// keeps an unproven first-party binary from running: the SHA-256, then the
// release signature, and only then the manifest read, which executes the
// binary. The manifest's own plugin ID and version must then be the
// catalog's, which ties the binary to the signed tag the policy named.
func (i *Installer) downloadBinary(ctx context.Context, req InstallBinaryRequest) (*downloadedBinary, error) {
	binaryData, err := fetchPackageResource(ctx, i.httpClient, req.BinaryURL, "the plugin")
	if err != nil {
		return nil, err
	}

	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])
	if actualChecksum != req.Checksum {
		return nil, packageError(fmt.Errorf("binary checksum mismatch: expected %s, got %s", req.Checksum, actualChecksum),
			"The downloaded plugin doesn't match the SHA-256 checksum its catalog lists, so Silo didn't install it.")
	}

	signature, err := i.signatures.checkBinary(ctx, req, checksum[:], func(ctx context.Context, bundleURL string) ([]byte, error) {
		return fetchSignatureBundle(ctx, i.httpClient, bundleURL)
	})
	if err != nil {
		return nil, err
	}

	manifest, manifestJSON, err := loadManifestFromBinary(ctx, binaryData)
	if err != nil {
		return nil, err
	}
	if err := checkCatalogIdentity(manifest, req.PluginID, req.Version); err != nil {
		return nil, err
	}

	return &downloadedBinary{data: binaryData, checksum: actualChecksum, manifest: manifest, manifestJSON: manifestJSON, signature: signature}, nil
}

func (i *Installer) InstallBinaryUpload(ctx context.Context, binaryData []byte) (*InstallResult, error) {
	if len(binaryData) == 0 {
		return nil, fmt.Errorf("binary data is required")
	}

	manifest, manifestJSON, err := loadManifestFromBinary(ctx, binaryData)
	if err != nil {
		return nil, err
	}

	checksum := sha256.Sum256(binaryData)
	actualChecksum := hex.EncodeToString(checksum[:])

	return i.installBinary(ctx, binaryData, actualChecksum, manifest, manifestJSON, nil, unsignedSignature)
}

// replaceBinary replaces existing in place with a binary from repositoryID,
// nil for an uploaded binary. The row then names the new source and the new
// package's signature: trust that depends on where a package came from, such
// as a first-party watch provider keeping its built-in key, must not carry
// over to a package from elsewhere.
func (i *Installer) replaceBinary(
	ctx context.Context,
	existing *Installation,
	binaryData []byte,
	checksum string,
	manifest *pluginv1.PluginManifest,
	manifestJSON []byte,
	repositoryID *int,
	signature Signature,
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
	if err := checkReplacementIdentity(manifest, existing); err != nil {
		return nil, err
	}

	manifestBytes, err := binaryManifestBytes(manifest, manifestJSON)
	if err != nil {
		return nil, err
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
	if err != nil {
		return nil, fmt.Errorf("package plugin archive: %w", err)
	}

	installRoot, err := pluginVersionRoot(i.baseDir, manifest.GetPluginId(), manifest.GetVersion())
	if err != nil {
		return nil, err
	}
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
		Signature:        &signature,
	}); err != nil {
		return nil, fmt.Errorf("update replacement plugin installation: %w", err)
	}

	if filepath.Clean(filepath.Dir(existing.InstallPath)) != filepath.Clean(installDir) {
		_ = removeInstallationFiles(ctx, existing)
	}

	keepInstallDir = true

	updated := *existing
	updated.Version = version
	updated.InstallPath = binaryPath
	updated.Enabled = enabled
	updated.RepositoryID = repositoryID
	updated.Signature = signature
	// Changing the version cleared the stored refused update.
	updated.UpdateFailure = nil

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
	if err := checkReplacementIdentity(manifest, existing); err != nil {
		return nil, err
	}

	installRoot, err := pluginVersionRoot(i.baseDir, manifest.GetPluginId(), manifest.GetVersion())
	if err != nil {
		return nil, err
	}
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
	// An archive, uploaded or downloaded, carries no release signature.
	unsigned := unsignedSignature
	if err := i.installations.Update(ctx, existing.ID, UpdateInstallationInput{
		Version:          &version,
		InstallPath:      &binaryPath,
		Enabled:          &enabled,
		AvailableVersion: &availableVersion,
		Capabilities:     capabilities,
		SetRepository:    true,
		RepositoryID:     repositoryID,
		Signature:        &unsigned,
	}); err != nil {
		return nil, fmt.Errorf("update replacement plugin installation: %w", err)
	}

	if filepath.Clean(filepath.Dir(existing.InstallPath)) != filepath.Clean(installDir) {
		_ = removeInstallationFiles(ctx, existing)
	}

	keepInstallDir = true

	updated := *existing
	updated.Version = version
	updated.InstallPath = binaryPath
	updated.Enabled = enabled
	updated.RepositoryID = repositoryID
	updated.Signature = unsigned
	// Changing the version cleared the stored refused update.
	updated.UpdateFailure = nil

	return &InstallResult{
		Installation: &updated,
		Manifest:     manifest,
		BinaryPath:   binaryPath,
		ManifestPath: manifestPath,
	}, nil
}

func (i *Installer) installBinary(ctx context.Context, binaryData []byte, checksum string, manifest *pluginv1.PluginManifest, manifestJSON []byte, repositoryID *int, signature Signature) (*InstallResult, error) {
	manifestBytes, err := binaryManifestBytes(manifest, manifestJSON)
	if err != nil {
		return nil, err
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
	if err != nil {
		return nil, fmt.Errorf("package plugin archive: %w", err)
	}

	installRoot, err := pluginVersionRoot(i.baseDir, manifest.GetPluginId(), manifest.GetVersion())
	if err != nil {
		return nil, err
	}
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
		Signature:    signature,
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
			return nil, fmt.Errorf("persist plugin archive: %w (cleanup failed: %v)", err, deleteErr) //nolint:errorlint // callers classify the install error; the cleanup failure is only reported
		}
		return nil, fmt.Errorf("persist plugin archive: %w", err)
	}

	enabled := true
	if err := i.installations.Update(ctx, installation.ID, UpdateInstallationInput{
		Enabled: &enabled,
	}); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("enable plugin installation: %w (cleanup failed: %v)", err, deleteErr) //nolint:errorlint // callers classify the install error; the cleanup failure is only reported
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

// binaryManifestBytes returns the manifest.json to store for a binary install:
// the plugin's own manifest output when there is one, so fields this server's
// SDK doesn't know survive for a later server that does, and otherwise the
// manifest serialized.
func binaryManifestBytes(manifest *pluginv1.PluginManifest, manifestJSON []byte) ([]byte, error) {
	if manifestJSON != nil {
		return manifestJSON, nil
	}
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize plugin manifest: %w", err)
	}
	return manifestBytes, nil
}

func (i *Installer) installArchive(ctx context.Context, data []byte, req InstallArchiveRequest) (*InstallResult, error) {
	repositoryID := req.RepositoryID
	reader, manifestBytes, manifest, err := openInstallArchive(data, req)
	if err != nil {
		return nil, err
	}

	installRoot, err := pluginVersionRoot(i.baseDir, manifest.GetPluginId(), manifest.GetVersion())
	if err != nil {
		return nil, err
	}
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
		// An archive, uploaded or downloaded, carries no release signature.
		Signature: unsignedSignature,
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
			return nil, fmt.Errorf("persist plugin archive: %w (cleanup failed: %v)", err, deleteErr) //nolint:errorlint // callers classify the install error; the cleanup failure is only reported
		}
		return nil, fmt.Errorf("persist plugin archive: %w", err)
	}

	if err := extractArchiveFiles(reader, installDir); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("extract plugin archive: %w (cleanup failed: %v)", err, deleteErr) //nolint:errorlint // callers classify the install error; the cleanup failure is only reported
		}
		return nil, err
	}

	enabled := true
	if err := i.installations.Update(ctx, installation.ID, UpdateInstallationInput{
		Enabled: &enabled,
	}); err != nil {
		if deleteErr := i.installations.Delete(ctx, installation.ID); deleteErr != nil {
			return nil, fmt.Errorf("enable plugin installation: %w (cleanup failed: %v)", err, deleteErr) //nolint:errorlint // callers classify the install error; the cleanup failure is only reported
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

// loadManifestFromBinary runs the plugin's manifest command and returns the
// manifest it prints, both decoded and as printed.
func loadManifestFromBinary(ctx context.Context, binaryData []byte) (*pluginv1.PluginManifest, []byte, error) {
	tmpDir, err := os.MkdirTemp("", "plugin-manifest-")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir for binary manifest: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpBinary := filepath.Join(tmpDir, "plugin")
	if err := os.WriteFile(tmpBinary, binaryData, 0755); err != nil {
		return nil, nil, fmt.Errorf("write temp binary for manifest: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, tmpBinary, "manifest")
	cmd.Env = pluginhost.ProcessEnv()
	manifestOut, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("execute plugin manifest command: %w", ctx.Err())
		}
		runErr := fmt.Errorf("execute plugin manifest command: %w", err)
		// Only a binary that ran and failed (or timed out), or that isn't an
		// executable for this platform, is the package's fault. Any other
		// start failure, such as a noexec temp directory, is the server's.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) && !errors.Is(err, syscall.ENOEXEC) {
			return nil, nil, runErr
		}
		return nil, nil, packageError(runErr,
			"Silo couldn't run the plugin to read its manifest. It may be built for another platform, or the file may be damaged.")
	}

	// Fields this server's SDK doesn't know yet are dropped, as when an
	// archive's manifest.json is loaded, so a plugin built on a newer SDK
	// still installs.
	var manifest pluginv1.PluginManifest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(manifestOut, &manifest); err != nil {
		return nil, nil, packageError(fmt.Errorf("parse plugin manifest output: %w", err),
			"The plugin's manifest couldn't be read.")
	}
	if err := ValidateManifest(&manifest); err != nil {
		return nil, nil, packageError(err, "The plugin's manifest is invalid: %v.", err)
	}
	if manifest.GetSiloApiVersion() != DefaultSiloAPIVersion {
		return nil, nil, unsupportedAPIVersionError(manifest.GetSiloApiVersion(), DefaultSiloAPIVersion)
	}
	return &manifest, manifestOut, nil
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

// checkReplacementIdentity refuses a package that names a different plugin
// than the installation it would replace.
func checkReplacementIdentity(manifest *pluginv1.PluginManifest, existing *Installation) error {
	if manifest.GetPluginId() == existing.PluginID {
		return nil
	}
	return packageError(
		fmt.Errorf("replacement plugin id %q does not match installation %q", manifest.GetPluginId(), existing.PluginID),
		"The package is %s, not the installed %s, so Silo didn't replace it.",
		manifest.GetPluginId(), existing.PluginID,
	)
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

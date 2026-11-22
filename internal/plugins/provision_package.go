package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"
)

var (
	ErrProvisionPackageNeedsFetch = errors.New("plugin package requires an online catalog fetch")
	ErrProvisionPackageUntrusted  = errors.New("installed plugin package is not a verified official release")
)

const (
	maxProvisionBinaryBytes = 256 << 20
	provisionStageRoot      = "@provision-staging"
	provisionStagePrefix    = "install-"
	provisionStageOwner     = ".owner"
)

type ProvisionPackageRequest struct {
	PluginID string
	Version  string
	Existing *Installation
	Offline  bool
}

// PreparedProvisionPackage supplies the manifest verified from the package.
// Persist writes only through the writer's transaction. Call Abort after a
// failed attempt and Finish after the transaction commits.
type PreparedProvisionPackage struct {
	Manifest     *pluginv1.PluginManifest
	RepositoryID int
	Version      string

	existing         *Installation
	previousManifest *pluginv1.PluginManifest
	archiveChecksum  string
	archiveUpdated   time.Time
	root             string
	stage            *provisionPackageStage
	create           CreateInstallationInput
}

// PrepareProvisionPackage checks the installed package offline, then resolves
// and stages a download when a different release is needed.
func (s *CatalogService) PrepareProvisionPackage(ctx context.Context, installer *Installer, req ProvisionPackageRequest) (*PreparedProvisionPackage, error) {
	repoURL, err := validateOfficialReleaseRequest(req.PluginID, req.Version)
	if err != nil {
		return nil, err
	}
	repository, err := s.OfficialRepository(ctx)
	if err != nil {
		return nil, err
	}
	if installer == nil || installer.store == nil {
		return nil, errors.New("plugin installation store is required")
	}

	prepared := &PreparedProvisionPackage{RepositoryID: repository.ID, root: installer.baseDir}
	if existing := req.Existing; existing != nil {
		manifest, err := prepared.checkInstalled(ctx, installer.store, req, repoURL)
		if err != nil {
			return nil, err
		}
		reuse := req.Version == "" || req.Version == existing.Version
		if reuse && manifest.GetSiloApiVersion() != s.siloAPIVersion {
			return nil, unsupportedAPIVersionError(manifest.GetSiloApiVersion(), s.siloAPIVersion)
		}
		if reuse {
			prepared.Manifest = manifest
			prepared.Version = existing.Version
			return prepared, nil
		}
	}
	if req.Offline {
		return nil, ErrProvisionPackageNeedsFetch
	}
	target, err := s.resolveOfficialInstall(ctx, repository, req.PluginID, req.Version)
	if err != nil {
		return nil, err
	}
	download := target.BinaryRequest()
	download.requireVerified = true
	binary, err := installer.downloadBinary(ctx, download)
	if err != nil {
		return nil, err
	}
	if !verifiedOfficialSignature(binary.signature, repoURL) || binary.manifest.GetChecksum() != binary.checksum {
		return nil, packageError(errors.New("official plugin manifest and verified package do not match"), signatureInvalidMessage)
	}
	if err := prepared.stageBinary(ctx, binary); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (p *PreparedProvisionPackage) checkInstalled(ctx context.Context, store *InstallationStore, req ProvisionPackageRequest, repoURL string) (*pluginv1.PluginManifest, error) {
	existing := req.Existing
	_, invalidVersion := validateOfficialReleaseRequest(req.PluginID, existing.Version)
	if existing.PluginID != req.PluginID || existing.Version == "" || invalidVersion != nil ||
		existing.IsBuiltin() || existing.RepositoryID == nil || *existing.RepositoryID != p.RepositoryID ||
		!verifiedOfficialSignature(existing.Signature, repoURL) {
		return nil, ErrProvisionPackageUntrusted
	}

	archive, err := store.archiveForRelease(ctx, existing)
	if errors.Is(err, ErrArchiveNotFound) {
		return nil, ErrProvisionPackageUntrusted
	}
	if err != nil {
		return nil, err
	}
	_, manifestBytes, manifest, err := openPluginArchive(archive.Bytes)
	if err != nil {
		_, manifestBytes, manifest, _, err = recoverLegacyBinaryArchive(archive, err)
	}
	if err != nil || checkStoredArchive(existing, archive, manifestBytes, manifest) != nil || len(archive.ManifestJSON) == 0 {
		return nil, ErrProvisionPackageUntrusted
	}
	p.existing = new(*existing)
	p.previousManifest = manifest
	p.archiveChecksum = archive.Checksum
	p.archiveUpdated = archive.UpdatedAt
	return manifest, nil
}

func verifiedOfficialSignature(signature Signature, repoURL string) bool {
	return signature.State == SignatureStateVerified && signature.SourceRepo != nil && *signature.SourceRepo == repoURL &&
		signature.VerifiedAt != nil && !signature.VerifiedAt.IsZero()
}

func (p *PreparedProvisionPackage) stageBinary(ctx context.Context, binary *downloadedBinary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	capabilities, err := CapabilityRecordsFromManifest(binary.manifest)
	if err != nil {
		return err
	}
	archiveBytes, err := buildBinaryPluginArchive(binary.manifestJSON, binary.data)
	if err != nil {
		return fmt.Errorf("package plugin archive: %w", err)
	}
	versionRoot, err := pluginVersionRoot(p.root, binary.manifest.GetPluginId(), binary.manifest.GetVersion())
	if err != nil {
		return err
	}
	stage, err := newProvisionPackageStage(p.root)
	if err != nil {
		return fmt.Errorf("create plugin staging directory: %w", err)
	}
	p.stage = stage
	stage.payload = filepath.Join(stage.dir, "release")
	stage.target = filepath.Join(versionRoot, filepath.Base(stage.dir))
	if err := os.Mkdir(stage.payload, 0o700); err != nil {
		return errors.Join(err, p.Abort())
	}
	if err := os.WriteFile(filepath.Join(stage.payload, "plugin"), binary.data, 0o755); err != nil { //nolint:gosec // The plugin executable is inside an owner-only release directory.
		return errors.Join(err, p.Abort())
	}
	if err := os.WriteFile(filepath.Join(stage.payload, "manifest.json"), binary.manifestJSON, 0o600); err != nil {
		return errors.Join(err, p.Abort())
	}

	p.Manifest = binary.manifest
	p.Version = binary.manifest.GetVersion()
	p.create = CreateInstallationInput{
		RepositoryID: p.RepositoryID,
		PluginID:     binary.manifest.GetPluginId(),
		Version:      p.Version,
		InstallPath:  filepath.Join(stage.target, "plugin"),
		Enabled:      true,
		Capabilities: capabilities,
		Signature:    binary.signature,
		Archive:      ArchiveInput{ManifestJSON: binary.manifestJSON, Checksum: binary.checksum, Bytes: archiveBytes},
	}
	return nil
}

func (p *PreparedProvisionPackage) ReplacesPackage() bool {
	return p.stage != nil && p.existing != nil
}

func (p *PreparedProvisionPackage) Persist(ctx context.Context, store *InstallationStore, tx pgx.Tx) (*Installation, error) {
	scoped := store.WithTx(tx)
	if p.existing == nil {
		if p.stage == nil {
			return nil, ErrInstallationChanged
		}
		return scoped.Create(ctx, p.create)
	}

	current, err := p.lockCurrent(ctx, scoped, tx)
	if err != nil {
		return nil, err
	}
	if !p.ReplacesPackage() {
		return current, nil
	}

	err = scoped.updateProvisioned(ctx, current.ID, UpdateInstallationInput{
		Version:          &p.create.Version,
		InstallPath:      &p.create.InstallPath,
		AvailableVersion: new(""),
		Capabilities:     p.create.Capabilities,
		Signature:        &p.create.Signature,
		Archive:          &p.create.Archive,
		IfVersion:        &p.existing.Version,
		IfInstallPath:    &p.existing.InstallPath,
	})
	if err != nil {
		return nil, err
	}
	return scoped.GetByID(ctx, current.ID)
}

// lockCurrent verifies that the installation and archive are still the ones
// this package preparation used. The caller keeps the lock through its read
// of the prospective configuration.
func (p *PreparedProvisionPackage) lockCurrent(ctx context.Context, scoped *InstallationStore, tx pgx.Tx) (*Installation, error) {
	if err := lockPluginInstallation(ctx, tx, p.existing.ID); err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return nil, ErrInstallationChanged
		}
		return nil, err
	}
	current, err := scoped.GetByID(ctx, p.existing.ID)
	if err != nil {
		return nil, err
	}
	expectedRepo, _ := officialPluginRepository(p.existing.PluginID)
	if current.PluginID != p.existing.PluginID || current.Version != p.existing.Version ||
		current.InstallPath != p.existing.InstallPath || current.RuntimeGeneration != p.existing.RuntimeGeneration ||
		current.Enabled != p.existing.Enabled || current.RepositoryID == nil || *current.RepositoryID != p.RepositoryID ||
		!verifiedOfficialSignature(current.Signature, expectedRepo) ||
		!current.Signature.VerifiedAt.Equal(*p.existing.Signature.VerifiedAt) {
		return nil, ErrInstallationChanged
	}

	// Lock the stored archive without copying it back into the transaction.
	// This also catches a concurrent repair or restore of the same release.
	var checksum string
	var updated time.Time
	err = tx.QueryRow(ctx, `SELECT checksum, updated_at FROM plugin_archives WHERE plugin_installation_id = $1 FOR SHARE`, current.ID).Scan(&checksum, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInstallationChanged
	}
	if err != nil {
		return nil, err
	}
	if checksum != p.archiveChecksum || !updated.Equal(p.archiveUpdated) {
		return nil, ErrInstallationChanged
	}
	return current, nil
}

// Finish promotes the staged local files after commit. Another cache reader can
// already have rehydrated this release from the committed archive.
func (p *PreparedProvisionPackage) Finish(_ context.Context) error {
	if p.stage == nil {
		return nil
	}
	if err := p.stage.promote(); err != nil {
		return errors.Join(err, p.Abort())
	}
	var retireErr error
	if p.existing != nil {
		cache := &ArchiveCache{root: p.root}
		retireErr = cache.retire(p.existing, &Installation{
			PluginID: p.create.PluginID, Version: p.create.Version, InstallPath: p.create.InstallPath,
		})
	}
	return errors.Join(retireErr, p.Abort())
}

// Abort removes the private staging files. It can be called after Finish.
func (p *PreparedProvisionPackage) Abort() error {
	return p.stage.remove()
}

type provisionPackageStage struct {
	dir     string
	owner   *os.File
	payload string
	target  string
}

func (s *provisionPackageStage) remove() error {
	if s == nil || s.owner == nil {
		return nil
	}
	err := errors.Join(os.RemoveAll(s.dir), s.owner.Close())
	s.owner = nil
	return err
}

func newProvisionPackageStage(cacheRoot string) (*provisionPackageStage, error) {
	if err := os.MkdirAll(cacheRoot, 0o750); err != nil {
		return nil, err
	}
	parent := filepath.Join(cacheRoot, provisionStageRoot)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, provisionStagePrefix)
	if err != nil {
		return nil, err
	}
	owner, err := os.CreateTemp(dir, provisionStageOwner+"-")
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	// Publish the owner name only after taking the lock. A sweeper that
	// finds it can distinguish any active process from an abandoned stage.
	if err := unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.Join(err, owner.Close(), os.RemoveAll(dir))
	}
	if err := os.Rename(owner.Name(), filepath.Join(dir, provisionStageOwner)); err != nil {
		return nil, errors.Join(err, owner.Close(), os.RemoveAll(dir))
	}
	return &provisionPackageStage{dir: dir, owner: owner}, nil
}

func (s *provisionPackageStage) promote() error {
	if err := os.MkdirAll(filepath.Dir(s.target), 0o750); err != nil {
		return err
	}
	if err := os.Rename(s.payload, s.target); err != nil {
		if _, statErr := os.Stat(filepath.Join(s.target, "plugin")); statErr != nil {
			return err
		}
	}
	return nil
}

// CleanupProvisionStaging discards incomplete work from stopped local writers.
// A committed package can always rehydrate from its archive.
func (i *Installer) CleanupProvisionStaging() error {
	parent := filepath.Join(i.baseDir, provisionStageRoot)
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), provisionStagePrefix) {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		owner, err := os.OpenInRoot(parent, filepath.Join(entry.Name(), provisionStageOwner))
		if errors.Is(err, os.ErrNotExist) {
			// A creator may still be publishing the owner file. It writes
			// the package only after that file is in place and locked.
			continue
		}
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		err = unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(dir))
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		cleanupErr = errors.Join(cleanupErr, owner.Close())
	}
	return cleanupErr
}

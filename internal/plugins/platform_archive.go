package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/proto"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// An installation's stored archive (plugin_archives) holds the build the
// installing server downloaded, for that server's platform only. A server on
// another platform that shares the database, such as an arm64 node in a
// cluster of amd64 ones, can't run it. Such a server fetches the same
// version's build for its own platform from the catalog the plugin was
// installed from, through the same checks as an install, and stores it in
// plugin_platform_archives so it and the other servers on its platform
// don't fetch it again.

// foreignBuildError is a stored archive whose binary is for another
// platform than this host's. stored is that archive.
type foreignBuildError struct {
	installationID int
	err            error
	stored         *InstallationArchive
}

func (e *foreignBuildError) Error() string {
	return fmt.Sprintf("rehydrate plugin for installation %d: %v", e.installationID, e.err)
}

func (e *foreignBuildError) Unwrap() error { return e.err }

// checksPlatform reports whether the cache checks that a binary is for this
// host's platform. A proxy node's cache always does. An API server's does
// before running the binary (Ensure), when it can fetch this platform's
// build; a manifest read runs nothing and reads the same manifest from any
// platform's build.
func (c *ArchiveCache) checksPlatform(trustVerified bool) bool {
	return c.root != "" || (c.platforms != nil && !trustVerified)
}

// loadForHostPlatform is load, persisting, that rehydrates this host's
// build when the stored archive holds another platform's.
func (c *ArchiveCache) loadForHostPlatform(ctx context.Context, installation *Installation, trustVerified bool, archives archiveStore) (*pluginv1.PluginManifest, error) {
	manifest, err := c.load(ctx, installation, trustVerified, archives, true)
	foreign, ok := errors.AsType[*foreignBuildError](err)
	if !ok || c.platforms == nil {
		return manifest, err
	}
	archive, err := c.platforms.archiveFor(ctx, installation, foreign.stored, foreign.err)
	if err != nil {
		return nil, err
	}
	manifest, _, err = c.rehydrate(ctx, installation, trustVerified, archive)
	return manifest, err
}

// platformArchiveStore stores builds of installations for other platforms.
type platformArchiveStore interface {
	GetPlatformArchive(ctx context.Context, installationID int, goos, goarch, baseChecksum string) (*InstallationArchive, error)
	SavePlatformArchive(ctx context.Context, input SavePlatformArchiveInput) (bool, error)
}

// platformBuildFetcher downloads and checks the build of an installation's
// version for this host's platform from the catalog it was installed from.
type platformBuildFetcher func(ctx context.Context, installation *Installation) (*downloadedBinary, error)

// platformFetchRetryAfter is how long a server waits before it tries
// again to get a build it couldn't. Every start of the plugin fails
// meanwhile, without a catalog request or download each time.
const platformFetchRetryAfter = 10 * time.Minute

// platformArchives supplies an installation's build for this host's
// platform: the one stored for it, or one it fetches and stores.
type platformArchives struct {
	store platformArchiveStore
	fetch platformBuildFetcher

	mu sync.Mutex
	// failed holds, per installation and stored archive checksum, the last
	// failure to get this platform's build and when it happened.
	failed map[platformBuildKey]platformBuildFailure
}

type platformBuildKey struct {
	installationID int
	baseChecksum   string
}

type platformBuildFailure struct {
	err error
	at  time.Time
}

// archiveFor returns the installation's archive for this host's platform.
// stored is the installation's stored archive, which foreign says is for
// another platform.
func (p *platformArchives) archiveFor(ctx context.Context, installation *Installation, stored *InstallationArchive, foreign error) (*InstallationArchive, error) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	cannot := func(reason error) error {
		return fmt.Errorf("plugin %s %s can't run on this server: %w, and Silo couldn't get a %s/%s build: %w",
			installation.PluginID, installation.Version, foreign, goos, goarch, reason)
	}
	storedManifest, err := LoadManifestBytes(stored.ManifestJSON)
	if err != nil {
		return nil, cannot(fmt.Errorf("read the stored manifest: %w", err))
	}
	archive, err := p.store.GetPlatformArchive(ctx, installation.ID, goos, goarch, stored.Checksum)
	switch {
	case err == nil:
		// Two packages can share a binary checksum, as legacy archives of
		// one binary under several versions do, so the build must also
		// declare the installed release. Otherwise fetch it again.
		if manifest, err := LoadManifestBytes(archive.ManifestJSON); err == nil && sameRelease(storedManifest, manifest) {
			return archive, nil
		}
	case !errors.Is(err, ErrArchiveNotFound):
		return nil, fmt.Errorf("load stored %s/%s build of plugin %s %s: %w", goos, goarch, installation.PluginID, installation.Version, err)
	}

	key := platformBuildKey{installationID: installation.ID, baseChecksum: stored.Checksum}
	if err := p.recentFailure(key); err != nil {
		return nil, err
	}
	build, err := p.fetchBuild(ctx, installation, storedManifest)
	if err != nil {
		err = cannot(err)
		p.recordFailure(key, err)
		return nil, err
	}
	archive = build.archive

	// Rehydrate from it whether or not it is stored: it is this
	// installation's release either way. A failed or skipped write only
	// costs a fetch on the next load.
	saved, err := p.store.SavePlatformArchive(ctx, SavePlatformArchiveInput{
		Installation: installation,
		OS:           goos,
		Arch:         goarch,
		BaseChecksum: stored.Checksum,
		Archive:      archive,
	})
	switch {
	case err != nil:
		slog.WarnContext(ctx, "failed to store plugin build for this platform; it will be fetched again on the next load",
			"component", "plugins", "installation_id", installation.ID, "plugin_id", installation.PluginID,
			"version", installation.Version, "platform", goos+"/"+goarch, "error", err)
	case !saved:
		slog.InfoContext(ctx, "plugin installation changed while its build for this platform was fetched; not storing it",
			"component", "plugins", "installation_id", installation.ID, "plugin_id", installation.PluginID,
			"version", installation.Version, "platform", goos+"/"+goarch)
	default:
		slog.InfoContext(ctx, "fetched and stored plugin build for this platform",
			"component", "plugins", "installation_id", installation.ID, "plugin_id", installation.PluginID,
			"version", installation.Version, "platform", goos+"/"+goarch, "signature_state", build.signature.State)
	}
	return archive, nil
}

// fetchedBuild is this platform's build of an installation, checked and
// packaged as an archive.
type fetchedBuild struct {
	archive   *InstallationArchive
	signature Signature
}

// fetchBuild fetches this host's build of the installation and refuses one
// that isn't the installed release for this platform, or whose release
// signature proves less than the installed build's did.
func (p *platformArchives) fetchBuild(ctx context.Context, installation *Installation, storedManifest *pluginv1.PluginManifest) (*fetchedBuild, error) {
	if installation.RepositoryID == nil {
		return nil, errors.New("it has no catalog: it was uploaded, or its catalog was removed; install it from a catalog, or upload a build for this platform")
	}
	build, err := p.fetch(ctx, installation)
	if err != nil {
		return nil, err
	}
	if goos, goarch, ok := binaryPlatformOf(bytes.NewReader(build.data)); ok && (goos != runtime.GOOS || goarch != runtime.GOARCH) {
		return nil, fmt.Errorf("the catalog's build for this platform is a %s/%s binary", goos, goarch)
	}
	if !sameRelease(storedManifest, build.manifest) {
		return nil, errors.New("the catalog's build declares a different manifest than the installed one")
	}
	if installed := installation.Signature; installed.State == SignatureStateVerified {
		switch {
		case build.signature.State != SignatureStateVerified:
			return nil, fmt.Errorf("the installed build's release signature was verified, and this build's is %s", build.signature.State)
		case installed.SourceRepo != nil && (build.signature.SourceRepo == nil || *build.signature.SourceRepo != *installed.SourceRepo):
			return nil, fmt.Errorf("this build's release signature names another source repository than the installed build's (%s)", *installed.SourceRepo)
		}
	}
	manifestBytes, err := binaryManifestBytes(build.manifest, build.manifestJSON)
	if err != nil {
		return nil, err
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, build.data)
	if err != nil {
		return nil, fmt.Errorf("package plugin archive: %w", err)
	}
	return &fetchedBuild{
		archive: &InstallationArchive{
			InstallationID: installation.ID,
			ManifestJSON:   manifestBytes,
			Checksum:       build.checksum,
			Bytes:          archiveBytes,
		},
		signature: build.signature,
	}, nil
}

// recentFailure returns the failure to get key's build within the last
// platformFetchRetryAfter, if any.
func (p *platformArchives) recentFailure(key platformBuildKey) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	failure, ok := p.failed[key]
	if !ok || time.Now().Sub(failure.at) >= platformFetchRetryAfter {
		return nil
	}
	return failure.err
}

func (p *platformArchives) recordFailure(key platformBuildKey, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed == nil {
		p.failed = map[platformBuildKey]platformBuildFailure{}
	}
	p.failed[key] = platformBuildFailure{err: err, at: time.Now()}
}

// sameRelease reports whether two platforms' builds declare the same
// manifest apart from their binary checksums and supported platforms, which
// a build may fill in with its own. The installation's capability
// rows and settings were derived from the stored build's manifest, so a
// build that declares anything else is not this installation's release.
func sameRelease(stored, fetched *pluginv1.PluginManifest) bool {
	a, b := pluginhost.KnownManifest(stored), pluginhost.KnownManifest(fetched)
	if a == nil || b == nil {
		return false
	}
	a.Checksum, b.Checksum = "", ""
	a.SupportedPlatforms, b.SupportedPlatforms = nil, nil
	return proto.Equal(a, b)
}

// fetchPlatformBuild resolves the installation's version in the catalog it
// was installed from, for this host's platform, and downloads it with the
// checks an install runs: the catalog's SHA-256, the release signature
// policy, and the binary's own plugin ID and version. It changes nothing
// about the installation: its recorded source and signature describe the
// stored build.
func (s *Service) fetchPlatformBuild(ctx context.Context, installation *Installation) (*downloadedBinary, error) {
	if installation.RepositoryID == nil {
		return nil, errors.New("the plugin was not installed from a catalog")
	}
	target, err := s.catalog.ResolveInstall(ctx, InstallCatalogRequest{
		RepositoryID: *installation.RepositoryID,
		PluginID:     installation.PluginID,
		Version:      installation.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("%w (update the plugin to a version its catalog lists to install a build for every platform from there)", err)
	}
	if target.LegacyArchive {
		return nil, errors.New("the catalog lists this version as a single archive, with no build per platform")
	}
	return s.installer.downloadBinary(ctx, target.BinaryRequest())
}

// SavePlatformArchiveInput is a build of an installation for one platform.
type SavePlatformArchiveInput struct {
	// Installation is the row the build was fetched for. The write applies
	// only while the row still holds its version and install path.
	Installation *Installation
	OS, Arch     string
	// BaseChecksum is the checksum of the installation's stored archive
	// the build was fetched for; the write applies only while it still is.
	BaseChecksum string
	Archive      *InstallationArchive
}

// GetPlatformArchive returns the installation's stored build for goos/goarch
// fetched for the stored archive whose checksum is baseChecksum, or
// ErrArchiveNotFound.
func (s *InstallationStore) GetPlatformArchive(ctx context.Context, installationID int, goos, goarch, baseChecksum string) (*InstallationArchive, error) {
	query := `SELECT ` + archiveColumns + ` FROM plugin_platform_archives
		WHERE plugin_installation_id = $1 AND os = $2 AND arch = $3 AND base_checksum = $4`
	return scanArchive(s.pool.QueryRow(ctx, query, installationID, goos, goarch, baseChecksum))
}

// SavePlatformArchive stores a build of an installation for one platform,
// replacing the one stored for that platform. It reports false, writing
// nothing, when the installation no longer holds the version, install path
// and stored archive the build was fetched for: another install or update
// replaced it meanwhile.
func (s *InstallationStore) SavePlatformArchive(ctx context.Context, input SavePlatformArchiveInput) (bool, error) {
	inst, archive := input.Installation, input.Archive
	if inst == nil || archive == nil {
		return false, errors.New("saving plugin platform archive: installation and archive are required")
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO plugin_platform_archives
			(plugin_installation_id, os, arch, base_checksum, manifest_json, checksum, archive_bytes)
		SELECT i.id, $2, $3, $4, $5, $6, $7
		FROM plugin_installations i
		JOIN plugin_archives a ON a.plugin_installation_id = i.id
		WHERE i.id = $1 AND i.version = $8 AND i.install_path = $9 AND a.checksum = $4
		ON CONFLICT (plugin_installation_id, os, arch) DO UPDATE SET
			base_checksum = EXCLUDED.base_checksum,
			manifest_json = EXCLUDED.manifest_json,
			checksum = EXCLUDED.checksum,
			archive_bytes = EXCLUDED.archive_bytes,
			updated_at = NOW()`,
		inst.ID, input.OS, input.Arch, input.BaseChecksum, archive.ManifestJSON, archive.Checksum, archive.Bytes,
		inst.Version, inst.InstallPath)
	if err != nil {
		return false, fmt.Errorf("saving plugin platform archive: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

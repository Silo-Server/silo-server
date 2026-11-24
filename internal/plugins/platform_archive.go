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
	"github.com/jackc/pgx/v5"
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
// host's platform. A cache that can fetch this platform's build (an API
// server's) checks before running the binary (Ensure), whether or not it has
// its own root; a manifest read runs nothing and reads the same manifest from
// any platform's build. A cache that can't (a proxy node's) always checks.
func (c *ArchiveCache) checksPlatform(trustVerified bool) bool {
	if c.platforms != nil {
		return !trustVerified
	}
	return c.root != ""
}

// loadForHostPlatform is load, persisting, that rehydrates this host's
// build when the stored archive holds another platform's.
func (c *ArchiveCache) loadForHostPlatform(ctx context.Context, installation *Installation, read archiveRead, trustVerified bool, archives archiveStore) (*pluginv1.PluginManifest, error) {
	if c.platforms != nil && !trustVerified {
		// Only a stored build for another platform records a failure, so
		// within the retry delay fail without reading, extracting and hashing
		// that build under mu again on every start.
		if err := c.platforms.recentFailure(platformBuildKeyOf(installation)); err != nil {
			return nil, err
		}
	}
	manifest, err := c.load(ctx, installation, read, trustVerified, archives, true)
	foreign, ok := errors.AsType[*foreignBuildError](err)
	if !ok || c.platforms == nil {
		return manifest, err
	}
	archive, err := c.platforms.archiveFor(ctx, installation, foreign.stored, foreign.err)
	if err != nil {
		return nil, err
	}
	manifest, _, err = c.rehydrate(ctx, installation, read, trustVerified, archive)
	return manifest, err
}

// platformArchiveStore stores builds of installations for other platforms.
type platformArchiveStore interface {
	GetPlatformArchive(ctx context.Context, installationID int, goos, goarch, baseChecksum string) (*PlatformArchive, error)
	SavePlatformArchive(ctx context.Context, input SavePlatformArchiveInput) (bool, error)
}

// platformBuildFetcher downloads and checks the build of an installation's
// version for this host's platform from the catalog it was installed from.
type platformBuildFetcher func(ctx context.Context, installation *Installation) (*downloadedBinary, error)

// platformFetchRetryAfter is how long a server waits before it tries
// again to get a build it couldn't. Every start of the plugin fails
// meanwhile, without a catalog request or download each time. It is shorter
// than the resident supervisor's restart budget (ten attempts over about four
// minutes), so a resident plugin gets fresh fetches before it is parked.
const platformFetchRetryAfter = time.Minute

// platformFetchTimeout bounds one fetch of a platform build. The fetch runs in
// the archive cache's shared load, which outlives the callers that started
// it, so it can't rely on their deadlines.
const platformFetchTimeout = 5 * time.Minute

// platformArchives supplies an installation's build for this host's
// platform: the one stored for it, or one it fetches and stores.
type platformArchives struct {
	store platformArchiveStore
	fetch platformBuildFetcher

	mu sync.Mutex
	// failed holds, per installed release, the last failure to get this
	// platform's build and when it happened.
	failed map[platformBuildKey]platformBuildFailure
}

// platformBuildKey is an installed release: the installation row's ID,
// version and install path, which an install or update changes.
type platformBuildKey struct {
	installationID int
	version        string
	installPath    string
}

func platformBuildKeyOf(installation *Installation) platformBuildKey {
	return platformBuildKey{installationID: installation.ID, version: installation.Version, installPath: installation.InstallPath}
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
	row, err := p.store.GetPlatformArchive(ctx, installation.ID, goos, goarch, stored.Checksum)
	switch {
	case err == nil:
		// Two packages can share a binary checksum, as legacy archives of
		// one binary under several versions do, so the build must also
		// declare the installed release, and its signature must prove as
		// much as the installed build's now does: a verified reinstall of
		// the same package doesn't keep a build fetched with less. Otherwise
		// fetch it again.
		manifest, err := LoadManifestBytes(row.Archive.ManifestJSON)
		if err == nil && sameRelease(storedManifest, manifest) && checkBuildSignature(installation, row.Signature) == nil {
			return row.Archive, nil
		}
	case !errors.Is(err, ErrArchiveNotFound):
		return nil, fmt.Errorf("load stored %s/%s build of plugin %s %s: %w", goos, goarch, installation.PluginID, installation.Version, err)
	}

	// loadForHostPlatform checked the retry delay before reading stored.
	key := platformBuildKeyOf(installation)
	build, err := p.fetchBuild(ctx, installation, storedManifest)
	if err != nil {
		err = cannot(err)
		p.recordFailure(key, err)
		return nil, err
	}

	// The write is guarded on the installation the build was fetched for,
	// and is the only check that the installation is still that one. When an
	// install or update replaced it meanwhile, or the write failed so nothing
	// shows it wasn't, this build may be for a release that is no longer
	// installed: refuse it rather than run it. The caller's next load reads
	// the current installation and fetches again, after the retry delay when
	// the write failed.
	saved, err := p.store.SavePlatformArchive(ctx, SavePlatformArchiveInput{
		Installation: installation,
		OS:           goos,
		Arch:         goarch,
		BaseChecksum: stored.Checksum,
		Archive:      build.archive,
		Signature:    build.signature,
	})
	switch {
	case err != nil:
		err = fmt.Errorf("store the %s/%s build of plugin %s %s: %w", goos, goarch, installation.PluginID, installation.Version, err)
		p.recordFailure(key, err)
		return nil, err
	case !saved:
		return nil, fmt.Errorf("plugin %s %s was replaced while this server fetched its %s/%s build: %w",
			installation.PluginID, installation.Version, goos, goarch, ErrInstallationChanged)
	default:
		slog.InfoContext(ctx, "fetched and stored plugin build for this platform",
			"component", "plugins", "installation_id", installation.ID, "plugin_id", installation.PluginID,
			"version", installation.Version, "platform", goos+"/"+goarch, "signature_state", build.signature.State)
	}
	return build.archive, nil
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
	// The fetcher runs these checks before the build runs; they are repeated
	// here for any fetcher that doesn't.
	if err := checkPlatformBuild(installation, build.data, build.signature); err != nil {
		return nil, err
	}
	if !sameRelease(storedManifest, build.manifest) {
		return nil, errors.New("the catalog's build declares a different manifest than the installed one")
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

// checkPlatformBuild refuses a fetched build that isn't a binary for this
// platform, or whose release signature proves less than the installed
// build's.
func checkPlatformBuild(installation *Installation, data []byte, signature Signature) error {
	if goos, goarch, ok := binaryPlatformOf(bytes.NewReader(data)); ok && (goos != runtime.GOOS || goarch != runtime.GOARCH) {
		return fmt.Errorf("the catalog's build for this platform is a %s/%s binary", goos, goarch)
	}
	return checkBuildSignature(installation, signature)
}

// checkBuildSignature refuses a build whose release signature proves less
// than the installed build's: when that was verified, the build's must be
// too, from the same source repository when one is recorded.
func checkBuildSignature(installation *Installation, signature Signature) error {
	installed := installation.Signature
	if installed.State != SignatureStateVerified {
		return nil
	}
	switch {
	case signature.State != SignatureStateVerified:
		return fmt.Errorf("the installed build's release signature was verified, and this build's is %s", signatureStateLabel(signature.State))
	case installed.SourceRepo != nil && (signature.SourceRepo == nil || *signature.SourceRepo != *installed.SourceRepo):
		return fmt.Errorf("this build's release signature names another source repository than the installed build's (%s)", *installed.SourceRepo)
	}
	return nil
}

func signatureStateLabel(state SignatureState) SignatureState {
	if state == "" {
		return SignatureStateUnknown
	}
	return state
}

// recentFailure returns the failure to get key's build within the last
// platformFetchRetryAfter, if any.
func (p *platformArchives) recentFailure(key platformBuildKey) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	failure, ok := p.failed[key]
	if !ok || time.Since(failure.at) >= platformFetchRetryAfter {
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
	ctx, cancel := context.WithTimeout(ctx, platformFetchTimeout)
	defer cancel()
	target, err := s.resolvePlatformBuild(ctx, installation)
	if err != nil {
		return nil, fmt.Errorf("%w (update the plugin to a version its catalog lists to install a build for every platform from there)", err)
	}
	if target.LegacyArchive {
		return nil, errors.New("the catalog lists this version as a single archive, with no build per platform")
	}
	// Refuse a build for another platform, or one signed with less than the
	// installed build, before it runs to read its manifest.
	return s.installer.downloadBinaryChecked(ctx, target.BinaryRequest(), func(data []byte, signature Signature) error {
		return checkPlatformBuild(installation, data, signature)
	})
}

// resolvePlatformBuild resolves the installation's version for this host's
// platform in the catalog it was installed from. The official catalog lists
// only each plugin's current release, so an official installation whose
// version the list no longer names resolves from that version's release
// descriptor, as provisioning an exact version does.
func (s *Service) resolvePlatformBuild(ctx context.Context, installation *Installation) (*ResolvedCatalogInstall, error) {
	target, err := s.catalog.ResolveInstall(ctx, InstallCatalogRequest{
		RepositoryID: *installation.RepositoryID,
		PluginID:     installation.PluginID,
		Version:      installation.Version,
	})
	if err == nil {
		return target, nil
	}
	if official, officialErr := s.catalog.OfficialRepository(ctx); officialErr == nil && official.ID == *installation.RepositoryID {
		target, exactErr := s.catalog.ResolveOfficialInstall(ctx, installation.PluginID, installation.Version)
		if exactErr == nil {
			return target, nil
		}
		return nil, fmt.Errorf("%w; its release descriptor: %w", err, exactErr)
	}
	return nil, err
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
	// Signature is the build's release signature check.
	Signature Signature
}

// PlatformArchive is a build of an installation stored for one platform.
type PlatformArchive struct {
	Archive *InstallationArchive
	// Signature is the build's release signature check, from when it was
	// fetched.
	Signature Signature
}

// GetPlatformArchive returns the installation's stored build for goos/goarch
// fetched for the stored archive whose checksum is baseChecksum, or
// ErrArchiveNotFound.
func (s *InstallationStore) GetPlatformArchive(ctx context.Context, installationID int, goos, goarch, baseChecksum string) (*PlatformArchive, error) {
	var archive InstallationArchive
	var state string
	var sourceRepo *string
	err := s.pool.QueryRow(ctx, `SELECT `+archiveColumns+`, signature_state, signature_source_repo
		FROM plugin_platform_archives
		WHERE plugin_installation_id = $1 AND os = $2 AND arch = $3 AND base_checksum = $4`,
		installationID, goos, goarch, baseChecksum).Scan(
		&archive.InstallationID, &archive.ManifestJSON, &archive.Checksum, &archive.Bytes,
		&archive.CreatedAt, &archive.UpdatedAt, &state, &sourceRepo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrArchiveNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning plugin platform archive: %w", err)
	}
	return &PlatformArchive{Archive: &archive, Signature: Signature{State: SignatureState(state), SourceRepo: sourceRepo}}, nil
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
			(plugin_installation_id, os, arch, base_checksum, manifest_json, checksum, archive_bytes,
			 signature_state, signature_source_repo)
		SELECT i.id, $2, $3, $4, $5, $6, $7, $10, $11
		FROM plugin_installations i
		JOIN plugin_archives a ON a.plugin_installation_id = i.id
		WHERE i.id = $1 AND i.version = $8 AND i.install_path = $9 AND a.checksum = $4
		ON CONFLICT (plugin_installation_id, os, arch) DO UPDATE SET
			base_checksum = EXCLUDED.base_checksum,
			manifest_json = EXCLUDED.manifest_json,
			checksum = EXCLUDED.checksum,
			archive_bytes = EXCLUDED.archive_bytes,
			signature_state = EXCLUDED.signature_state,
			signature_source_repo = EXCLUDED.signature_source_repo,
			updated_at = NOW()`,
		inst.ID, input.OS, input.Arch, input.BaseChecksum, archive.ManifestJSON, archive.Checksum, archive.Bytes,
		inst.Version, inst.InstallPath, string(signatureStateLabel(input.Signature.State)), input.Signature.SourceRepo)
	if err != nil {
		return false, fmt.Errorf("saving plugin platform archive: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

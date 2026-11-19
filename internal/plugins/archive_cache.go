package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"golang.org/x/sync/singleflight"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type archiveStore interface {
	GetArchive(ctx context.Context, installationID int) (*InstallationArchive, error)
	SaveArchive(ctx context.Context, installationID int, manifestJSON []byte, checksum string, archiveBytes []byte) error
}

// releaseArchiveReader pairs the archived bytes with the installation's
// identity in one database snapshot.
type releaseArchiveReader interface {
	archiveForRelease(context.Context, *Installation) (*InstallationArchive, error)
}

type ArchiveCache struct {
	// mu protects rehydration and pruning from concurrent status and
	// reconcile calls, including cache-hit validation during extraction, and
	// guards verified and retirement state.
	mu sync.Mutex
	// loads shares one archive read among concurrent loads of an
	// installation from archives; mu is never held across one.
	loads    singleflight.Group
	archives archiveStore
	// root, when set, is this host's own plugin cache dir. Installations are
	// then rehydrated under it (see LocalInstallPath) instead of at the
	// install path the API server recorded, which on a proxy node names a
	// directory on another machine.
	root string
	// verified holds, per installation, the binary this cache last hashed
	// and found matching its manifest. Only Manifest consults it.
	verified map[int]verifiedBinary
	// Keep a generation for each retired path, even after reuse, so a read
	// that crossed a retirement cannot put its old archive back.
	pathStates         map[string]archivePathState
	pendingRetirements map[string]*Installation
	// newHash, when set, replaces sha256.New for checking an installed
	// binary against its manifest checksum. Tests use it to count the bytes a
	// read hashes.
	newHash func() hash.Hash
}

type archivePathState struct {
	generation uint64
	retired    bool
}

type archiveRead struct {
	binaryPath string
	generation uint64
}

// verifiedBinary is a binary whose hash matched checksum: its path and the
// file info taken from the open file before hashing.
type verifiedBinary struct {
	path     string
	checksum string
	info     os.FileInfo
}

func NewArchiveCache(archives archiveStore) *ArchiveCache {
	if archives == nil {
		return nil
	}
	return &ArchiveCache{archives: archives}
}

// NewArchiveCacheAt returns a cache that keeps its copies of every
// installation under root, the host's own plugin cache dir. Recorded paths
// identify releases and can name a directory on another host.
func NewArchiveCacheAt(archives archiveStore, root string) *ArchiveCache {
	cache := NewArchiveCache(archives)
	if cache != nil {
		cache.root = filepath.Clean(root)
		if root == "" {
			cache.root = ""
		}
	}
	return cache
}

// LocalInstallPath is where this host keeps the installation's binary. Without
// a root it is the recorded install path. With one it is
// <root>/<plugin id>/<version>/<install dir name>/plugin, where the install
// dir name is the unique directory the API server's installer created
// (install-XXXX), so a replaced binary — even at the same version — lands in
// a fresh directory here too and the resident supervisor's install-path
// change detection stays meaningful on this host.
// Invalid identities are rejected before callers can read the returned path.
func (c *ArchiveCache) LocalInstallPath(installation *Installation) (string, error) {
	if installation == nil {
		return "", errors.New("plugin installation is required")
	}
	if _, _, err := pluginIdentitySegments(installation.PluginID, installation.Version); err != nil {
		return "", err
	}
	if c == nil || c.root == "" {
		return installation.InstallPath, nil
	}
	id, err := cachePathSegment(installation.PluginID, "plugin id")
	if err != nil {
		return "", err
	}
	version, err := cachePathSegment(installation.Version, "plugin version")
	if err != nil {
		return "", err
	}
	release := filepath.Base(filepath.Dir(installation.InstallPath))
	if !strings.HasPrefix(release, "install-") || filepath.Base(installation.InstallPath) != pluginBinaryName {
		// Legacy packages put their binary directly in the version directory.
		// They get a separate cache directory so retirement can never remove
		// a replacement staged beside their binary.
		sum := sha256.Sum256([]byte(installation.InstallPath))
		release = "legacy-" + hex.EncodeToString(sum[:])
	}
	return pluginPathUnder(c.root, id, version, release, pluginBinaryName)
}

// Ensure makes the installation's files present at LocalInstallPath,
// rehydrating them from plugin_archives when they are missing, incomplete, or corrupted,
// and returns the installed manifest. It hashes the binary on every call, so
// every path that executes the binary goes through it.
func (c *ArchiveCache) Ensure(ctx context.Context, installation *Installation) (*pluginv1.PluginManifest, error) {
	return c.ensure(ctx, installation, false)
}

// Manifest is Ensure for callers that read the manifest or serve packaged
// assets and never execute the binary. It skips the hash when the binary is
// the same file, with the same size and modification time, that this cache
// last hashed against the checksum the manifest still names; any change sends
// it through Ensure's full check and repair.
//
// Skipping the hash here does not weaken what these callers get. The manifest
// is read and validated from disk on every call, and the binary hash never
// covered it: it proves only that the binary matches the manifest's checksum.
// A binary rewritten in place with its size and mtime restored is not
// executed by a manifest read, and the next launch's Ensure hashes it,
// rejects it, and restores the stored archive.
func (c *ArchiveCache) Manifest(ctx context.Context, installation *Installation) (*pluginv1.PluginManifest, error) {
	return c.ensure(ctx, installation, true)
}

// ManifestFrom is Manifest reading a stored archive, when it needs one,
// through archives (a store bound to the caller's transaction) and never
// writing one back: inside a transaction that holds a pool's last
// connection, nothing may wait on the pool.
func (c *ArchiveCache) ManifestFrom(ctx context.Context, installation *Installation, archives archiveStore) (*pluginv1.PluginManifest, error) {
	return c.ensureFrom(ctx, installation, true, archives, false)
}

func (c *ArchiveCache) ensure(
	ctx context.Context,
	installation *Installation,
	trustVerified bool,
) (*pluginv1.PluginManifest, error) {
	return c.ensureFrom(ctx, installation, trustVerified, c.archives, true)
}

// checkStoredArchive requires a stored archive to be the installation's:
// its checksum, manifest, plugin id, and version.
func checkStoredArchive(installation *Installation, archive *InstallationArchive, manifestBytes []byte, manifest *pluginv1.PluginManifest) error {
	if archive.Checksum != manifest.GetChecksum() {
		return unreadableArchivef("stored plugin archive checksum mismatch for installation %d", installation.ID)
	}
	if len(archive.ManifestJSON) > 0 && !bytes.Equal(archive.ManifestJSON, manifestBytes) {
		return unreadableArchivef("stored plugin manifest mismatch for installation %d", installation.ID)
	}
	if manifest.GetPluginId() != installation.PluginID {
		return unreadableArchivef("stored plugin archive plugin_id %q does not match installation %q", manifest.GetPluginId(), installation.PluginID)
	}
	if manifest.GetVersion() != installation.Version {
		return unreadableArchivef("stored plugin archive version %q does not match installation %q", manifest.GetVersion(), installation.Version)
	}
	return nil
}

// ensureFrom never holds mu while it reads or writes archives: a store on
// the pool may wait for a connection, and a caller inside a transaction
// that holds the pool's last connection (a restore) waits on mu. It checks
// the installed files under mu, reads the stored archive without it, and
// takes mu again to rehydrate, unless another caller already did. A read
// through another store (ManifestFrom's transaction) never joins a load
// from the cache's own, which may wait on the pool.
func (c *ArchiveCache) ensureFrom(
	ctx context.Context,
	installation *Installation,
	trustVerified bool,
	archives archiveStore,
	persist bool,
) (*pluginv1.PluginManifest, error) {
	if installation == nil {
		return nil, fmt.Errorf("plugin installation is required")
	}
	binaryPath, err := c.LocalInstallPath(installation)
	if err != nil {
		return nil, fmt.Errorf("plugin cache path for installation %d: %w", installation.ID, err)
	}
	// The cache's own pool can attest that a retired path returned. A
	// transaction's archive read may still see the pre-retirement snapshot.
	_, checksRelease := archives.(releaseArchiveReader)
	c.mu.Lock()
	read := archiveRead{binaryPath: binaryPath, generation: c.pathStates[binaryPath].generation}
	manifest, err := c.installed(ctx, installation, binaryPath, trustVerified, persist && checksRelease)
	c.mu.Unlock()
	if manifest != nil || err != nil {
		return manifest, err
	}
	if !persist {
		return c.load(ctx, installation, read, trustVerified, archives, false)
	}
	// Concurrent loads of one installation from the cache's own store share
	// one read of its archive if they began in the same cache generation.
	key := fmt.Sprintf("%d/%t/%q/%q/%d", installation.ID, trustVerified, installation.Version, installation.InstallPath, read.generation)
	result := c.loads.DoChan(key, func() (any, error) {
		ctx := context.WithoutCancel(ctx)
		c.mu.Lock()
		manifest, err := c.rehydratedMeanwhile(installation, read, trustVerified)
		c.mu.Unlock()
		if manifest != nil || err != nil {
			return manifest, err
		}
		return c.load(ctx, installation, read, trustVerified, archives, true)
	})
	select {
	case r := <-result:
		manifest, _ := r.Val.(*pluginv1.PluginManifest)
		return manifest, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// load reads the installation's stored archive, without mu, rehydrates
// its files, and, when persist is set, stores an archive it repaired.
func (c *ArchiveCache) load(ctx context.Context, installation *Installation, read archiveRead, trustVerified bool, archives archiveStore, persist bool) (*pluginv1.PluginManifest, error) {
	var archive *InstallationArchive
	var err error
	if store, ok := archives.(releaseArchiveReader); ok {
		archive, err = store.archiveForRelease(ctx, installation)
	} else {
		archive, err = archives.GetArchive(ctx, installation.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("load stored plugin archive for installation %d: %w", installation.ID, err)
	}
	manifest, repaired, err := c.rehydrate(ctx, installation, read, trustVerified, archive)
	if err != nil || repaired == nil || !persist {
		return manifest, err
	}
	// Persist the repaired archive so future preloads skip recovery, but don't
	// fail startup over a write error — the in-memory archive is already valid
	// and recovery will retry on the next preload.
	if store, ok := archives.(*InstallationStore); ok {
		err = store.repairArchive(ctx, archive, repaired.archive)
	} else {
		err = archives.SaveArchive(ctx, installation.ID, repaired.manifest, manifest.GetChecksum(), repaired.archive)
	}
	if err != nil {
		slog.WarnContext(ctx, "failed to persist recovered legacy plugin archive; will retry on next preload",
			"component", "plugins", "installation_id", installation.ID, "error", err)
	}
	return manifest, nil
}

// matchingInstalledManifest returns nil if the manifest is unusable or belongs
// to a different release.
func matchingInstalledManifest(path string, installation *Installation) *pluginv1.PluginManifest {
	manifest, err := LoadManifestFile(path)
	if err != nil || manifest.GetPluginId() != installation.PluginID || manifest.GetVersion() != installation.Version {
		return nil
	}
	return manifest
}

// installed returns the installed manifest when the files pass the check,
// and nil when they need rehydrating. The caller holds mu.
func (c *ArchiveCache) installed(ctx context.Context, installation *Installation, binaryPath string, trustVerified, mayReuseRetired bool) (*pluginv1.PluginManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.pathStates[binaryPath].retired {
		if !mayReuseRetired {
			return nil, ErrInstallationChanged
		}
		// Some files may remain after unsuccessful retirement. Load and
		// verify the committed archive before trusting this path again.
		return nil, nil
	}
	manifest := matchingInstalledManifest(InstalledManifestPath(binaryPath), installation)
	if manifest == nil {
		delete(c.verified, installation.ID)
		return nil, nil
	}
	if trustVerified && c.stillVerified(installation.ID, binaryPath, manifest) {
		return manifest, nil
	}
	if info, err := c.validateInstalledFiles(binaryPath, manifest); err == nil {
		if c.root != "" {
			if err := checkBinaryPlatform(binaryPath); err != nil {
				return nil, fmt.Errorf("cached plugin for installation %d: %w", installation.ID, err)
			}
		}
		c.markVerified(installation.ID, binaryPath, manifest, info)
		return manifest, nil
	}
	// The installed files failed the check; forget any earlier verification.
	delete(c.verified, installation.ID)
	return nil, nil
}

// rehydratedMeanwhile returns the installed manifest when another caller
// rehydrated the files since this one's check failed: that check forgot the
// installation's verification, so one recorded now is the other caller's.
// It hashes nothing for Manifest; Ensure still hashes the files itself.
// Callers hold mu.
func (c *ArchiveCache) rehydratedMeanwhile(installation *Installation, read archiveRead, trustVerified bool) (*pluginv1.PluginManifest, error) {
	binaryPath := read.binaryPath
	state := c.pathStates[binaryPath]
	if state.generation != read.generation {
		return nil, ErrInstallationChanged
	}
	if state.retired {
		return nil, nil
	}
	manifest, err := LoadManifestFile(InstalledManifestPath(binaryPath))
	if err != nil || !c.stillVerified(installation.ID, binaryPath, manifest) {
		return nil, nil //nolint:nilerr // no installed files: not rehydrated yet
	}
	if trustVerified {
		return manifest, nil
	}
	info, err := c.validateInstalledFiles(binaryPath, manifest)
	if err != nil {
		return nil, nil //nolint:nilerr // changed again since: rehydrate
	}
	c.markVerified(installation.ID, binaryPath, manifest, info)
	return manifest, nil
}

// repairedArchive is a legacy archive rebuilt in memory, for its store.
type repairedArchive struct{ manifest, archive []byte }

// rehydrate writes the installation's files from its stored archive under
// mu, unless a concurrent caller already put valid files in place. It
// returns the archive it repaired from a legacy one, if any, to persist.
func (c *ArchiveCache) rehydrate(ctx context.Context, installation *Installation, read archiveRead, trustVerified bool,
	archive *InstallationArchive) (*pluginv1.PluginManifest, *repairedArchive, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if manifest, err := c.rehydratedMeanwhile(installation, read, trustVerified); manifest != nil || err != nil {
		return manifest, nil, err
	}
	binaryPath := read.binaryPath
	var repaired *repairedArchive
	reader, manifestBytes, manifest, err := openPluginArchive(archive.Bytes)
	if err != nil {
		var archiveBytes []byte
		reader, manifestBytes, manifest, archiveBytes, err = recoverLegacyBinaryArchive(archive, err)
		if err != nil {
			return nil, nil, unreadableArchivef("open stored plugin archive for installation %d: %w", installation.ID, err)
		}
		repaired = &repairedArchive{manifest: manifestBytes, archive: archiveBytes}
	}
	if err := checkStoredArchive(installation, archive, manifestBytes, manifest); err != nil {
		return nil, nil, err
	}

	installDir := filepath.Dir(binaryPath)
	if err := os.RemoveAll(installDir); err != nil {
		return nil, nil, fmt.Errorf("clear plugin cache dir %q: %w", installDir, err)
	}
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("create plugin cache dir %q: %w", installDir, err)
	}
	if err := extractArchiveFiles(reader, installDir); err != nil {
		_ = os.RemoveAll(installDir)
		// Creating or writing the files fails with a *fs.PathError, a fault
		// of this host's disk. Anything else came from the archive: an entry
		// that doesn't decompress or match its CRC, or an unsafe entry name.
		if _, ok := errors.AsType[*fs.PathError](err); ok {
			return nil, nil, fmt.Errorf("extract stored plugin archive for installation %d: %w", installation.ID, err)
		}
		return nil, nil, unreadableArchivef("extract stored plugin archive for installation %d: %w", installation.ID, err)
	}
	info, err := c.validateInstalledFiles(binaryPath, manifest)
	if err != nil {
		_ = os.RemoveAll(installDir)
		// The files were just written from the archive, so a failure other
		// than this host's disk is the archive's too: the binary or a packaged
		// asset stored as a directory entry.
		if _, ok := errors.AsType[*fs.PathError](err); ok {
			return nil, nil, fmt.Errorf("validate rehydrated plugin cache for installation %d: %w", installation.ID, err)
		}
		return nil, nil, unreadableArchivef("validate rehydrated plugin cache for installation %d: %w", installation.ID, err)
	}
	if c.root != "" {
		// Proxy caches may contain a binary installed by an API host on a
		// different platform. Apply the same check to fresh and cached files.
		if err := checkBinaryPlatform(binaryPath); err != nil {
			_ = os.RemoveAll(installDir)
			return nil, nil, fmt.Errorf("rehydrate plugin for installation %d: %w", installation.ID, err)
		}
	}
	c.markVerified(installation.ID, binaryPath, manifest, info)
	if state := c.pathStates[binaryPath]; state.retired {
		state.retired = false
		c.pathStates[binaryPath] = state
		delete(c.pendingRetirements, binaryPath)
	}
	return manifest, repaired, nil
}

// stillVerified reports whether the installed files are present and the
// binary is the one markVerified last recorded for the installation: same
// path, same file, same size and modification time, and a manifest that still
// names the checksum it matched. Callers hold mu.
func (c *ArchiveCache) stillVerified(installationID int, binaryPath string, manifest *pluginv1.PluginManifest) bool {
	seen, ok := c.verified[installationID]
	if !ok || seen.path != binaryPath || seen.checksum != manifest.GetChecksum() {
		return false
	}
	info, err := installedFilesPresent(binaryPath, manifest)
	return err == nil && os.SameFile(seen.info, info) &&
		info.Size() == seen.info.Size() && info.ModTime().Equal(seen.info.ModTime())
}

// markVerified records that the binary at binaryPath, described by info,
// hashed to the manifest's checksum. Callers hold mu.
func (c *ArchiveCache) markVerified(installationID int, binaryPath string, manifest *pluginv1.PluginManifest, info os.FileInfo) {
	if c.verified == nil {
		c.verified = make(map[int]verifiedBinary)
	}
	c.verified[installationID] = verifiedBinary{path: binaryPath, checksum: manifest.GetChecksum(), info: info}
}

// retire removes only the observed retired release, after a committed row
// change. Other directories may hold packages another operation is staging.
func (c *ArchiveCache) retire(previous, current *Installation) error {
	if c == nil || c.root == "" {
		return nil
	}
	binaryPath, err := c.LocalInstallPath(previous)
	if err != nil {
		return err
	}
	if current != nil {
		if path, err := c.LocalInstallPath(current); err != nil || path == binaryPath {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pathStates == nil {
		c.pathStates = make(map[string]archivePathState)
	}
	c.pathStates[binaryPath] = archivePathState{generation: c.pathStates[binaryPath].generation + 1, retired: true}
	if c.verified[previous.ID].path == binaryPath {
		delete(c.verified, previous.ID)
	}
	return c.cleanRetired(binaryPath, previous)
}

// cleanRetired keeps the information needed to retry a filesystem error even
// after the database row has moved on. Callers hold c.mu.
func (c *ArchiveCache) cleanRetired(binaryPath string, installation *Installation) error {
	err := errors.Join(os.RemoveAll(filepath.Dir(binaryPath)), removeInstallationFiles(c.root, installation))
	if err == nil {
		delete(c.pendingRetirements, binaryPath)
	} else {
		if c.pendingRetirements == nil {
			c.pendingRetirements = make(map[string]*Installation)
		}
		c.pendingRetirements[binaryPath] = installation
	}
	return err
}

func (c *ArchiveCache) retryRetirements(ctx context.Context, current map[int]*Installation) {
	if c == nil || c.root == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pendingRetirements) == 0 {
		return
	}
	// A restored row may use a path whose earlier removal failed. Preserve
	// its files until a release-guarded read can rehydrate that path.
	activePaths := make(map[string]struct{}, len(c.pendingRetirements))
	for _, installation := range current {
		if path, err := c.LocalInstallPath(installation); err == nil {
			activePaths[path] = struct{}{}
		}
	}
	for binaryPath, installation := range c.pendingRetirements {
		if _, active := activePaths[binaryPath]; active {
			continue
		}
		if err := c.cleanRetired(binaryPath, installation); err != nil {
			slog.WarnContext(ctx, "remove retired plugin release from cache", "component", "plugins", "installation_id", installation.ID, "error", err)
		}
	}
}

// recoverLegacyBinaryArchive rebuilds a legacy archive (a raw binary
// beside its manifest) as a plugin archive, in memory; the caller persists
// it.
func recoverLegacyBinaryArchive(archive *InstallationArchive, openErr error) (*zip.Reader, []byte, *pluginv1.PluginManifest, []byte, error) {
	if archive == nil || len(archive.ManifestJSON) == 0 || len(archive.Bytes) == 0 {
		return nil, nil, nil, nil, openErr
	}

	manifest, err := LoadManifestBytes(archive.ManifestJSON)
	if err != nil {
		return nil, nil, nil, nil, openErr
	}

	checksum := sha256.Sum256(archive.Bytes)
	actualChecksum := hex.EncodeToString(checksum[:])
	if actualChecksum != archive.Checksum || actualChecksum != manifest.GetChecksum() {
		return nil, nil, nil, nil, openErr
	}

	archiveBytes, err := buildBinaryPluginArchive(archive.ManifestJSON, archive.Bytes)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%w; recover legacy raw binary archive: %v", openErr, err) //nolint:errorlint // callers classify the original open error; the recovery failure is only reported
	}

	reader, manifestBytes, manifest, err := openPluginArchive(archiveBytes)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%w; recover legacy raw binary archive: %v", openErr, err) //nolint:errorlint // callers classify the original open error; the recovery failure is only reported
	}
	return reader, manifestBytes, manifest, archiveBytes, nil
}

// unreadableArchiveError is a failure of a stored archive's own contents. It
// matches ErrArchiveUnreadable and keeps its cause's message and chain.
type unreadableArchiveError struct{ err error }

func (e *unreadableArchiveError) Error() string { return e.err.Error() }

func (e *unreadableArchiveError) Unwrap() []error { return []error{ErrArchiveUnreadable, e.err} }

// invalidManifestError is an archive whose manifest.json doesn't load or
// validate. Its message names what is wrong with the manifest and nothing
// about the server, so an install shows it to the administrator.
type invalidManifestError struct{ err error }

func (e *invalidManifestError) Error() string { return e.err.Error() }

func (e *invalidManifestError) Unwrap() error { return e.err }

func unreadableArchivef(format string, args ...any) error {
	return &unreadableArchiveError{err: fmt.Errorf(format, args...)}
}

func openPluginArchive(data []byte) (*zip.Reader, []byte, *pluginv1.PluginManifest, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open plugin archive: %w", err)
	}

	// The checks below read entries by name, but extraction writes each one to
	// its cleaned path. Refuse any layout extraction would not reproduce
	// exactly: two entries for the same file, on a case- or
	// normalization-insensitive filesystem too, or an entry inside a file.
	files := make(map[string]*zip.File, len(reader.File))
	extracted := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		if err := validateRelativePath(file.Name, "plugin archive"); err != nil {
			return nil, nil, nil, err
		}
		key, err := archivePathKey(file.Name)
		if err != nil {
			return nil, nil, nil, err
		}
		if other, ok := extracted[key]; ok && (!other.FileInfo().IsDir() || !file.FileInfo().IsDir()) {
			return nil, nil, nil, fmt.Errorf("plugin archive entries %q and %q extract to the same path", other.Name, file.Name)
		}
		extracted[key] = file
		files[file.Name] = file
	}
	for key, file := range extracted {
		for child, parent := key, filepath.Dir(key); parent != child; child, parent = parent, filepath.Dir(parent) {
			if entry, ok := extracted[parent]; ok && !entry.FileInfo().IsDir() {
				return nil, nil, nil, fmt.Errorf("plugin archive entry %q is inside file %q", file.Name, entry.Name)
			}
		}
	}

	manifestFile, err := regularArchiveEntry(files, "manifest.json")
	if err != nil {
		return nil, nil, nil, err
	}
	binaryFile, err := regularArchiveEntry(files, "plugin")
	if err != nil {
		return nil, nil, nil, err
	}

	manifestBytes, err := readZipFile(manifestFile)
	if err != nil {
		return nil, nil, nil, err
	}
	manifest, err := LoadManifestBytes(manifestBytes)
	if err != nil {
		return nil, nil, nil, &invalidManifestError{err: err}
	}

	binaryBytes, err := readZipFile(binaryFile)
	if err != nil {
		return nil, nil, nil, err
	}
	checksum := sha256.Sum256(binaryBytes)
	if manifest.GetChecksum() != hex.EncodeToString(checksum[:]) {
		return nil, nil, nil, fmt.Errorf("plugin binary checksum does not match manifest")
	}

	for _, asset := range manifest.GetAssets() {
		if _, err := regularArchiveEntry(files, asset.GetPath()); err != nil {
			return nil, nil, nil, err
		}
	}

	return reader, manifestBytes, manifest, nil
}

// archivePathKey is the path extraction writes an entry to, in a form where
// names a case- and normalization-insensitive filesystem (such as APFS) treats
// as one file compare equal: the Unicode canonical caseless match. It refuses
// a name no file can have (NUL), and one whose key holds a combining grapheme
// joiner: NFD inserts one after 30 combining marks in a row, so canonically
// equivalent names could then get different keys.
func archivePathKey(name string) (string, error) {
	key := norm.NFD.String(cases.Fold().String(norm.NFD.String(filepath.Clean(name))))
	if strings.ContainsAny(key, "\x00\u034f") {
		return "", fmt.Errorf("plugin archive entry %q has an unsupported file name", name)
	}
	return key, nil
}

func regularArchiveEntry(files map[string]*zip.File, name string) (*zip.File, error) {
	file, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("plugin archive is missing %q", name)
	}
	if !file.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin archive entry %q is not a regular file", name)
	}
	return file, nil
}

func buildBinaryPluginArchive(manifestBytes []byte, binaryData []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)

	if err := writeArchiveEntry(writer, "manifest.json", manifestBytes); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writeArchiveEntry(writer, "plugin", binaryData); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close plugin archive: %w", err)
	}

	archiveBytes := buffer.Bytes()
	if _, _, _, err := openPluginArchive(archiveBytes); err != nil {
		return nil, fmt.Errorf("validate plugin archive: %w", err)
	}

	return archiveBytes, nil
}

func writeArchiveEntry(writer *zip.Writer, name string, data []byte) error {
	entry, err := writer.Create(name)
	if err != nil {
		return fmt.Errorf("create plugin archive entry %q: %w", name, err)
	}
	if _, err := entry.Write(data); err != nil {
		return fmt.Errorf("write plugin archive entry %q: %w", name, err)
	}
	return nil
}

func extractArchiveFiles(reader *zip.Reader, root string) error {
	for _, file := range reader.File {
		if err := extractZipFile(file, root); err != nil {
			return err
		}
	}
	return nil
}

// validateInstalledFiles checks the installed files are present and hashes
// the binary against the manifest checksum. It returns the binary's file info,
// taken from the open file before hashing, so it describes the file that
// matched.
func (c *ArchiveCache) validateInstalledFiles(binaryPath string, manifest *pluginv1.PluginManifest) (os.FileInfo, error) {
	if _, err := installedFilesPresent(binaryPath, manifest); err != nil {
		return nil, err
	}

	binary, err := os.Open(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("read plugin binary %q: %w", binaryPath, err)
	}
	defer func() { _ = binary.Close() }()
	info, err := binary.Stat()
	if err != nil {
		return nil, fmt.Errorf("read plugin binary %q: %w", binaryPath, err)
	}
	digest := sha256.New()
	if c.newHash != nil {
		digest = c.newHash()
	}
	if _, err := io.Copy(digest, binary); err != nil {
		return nil, fmt.Errorf("read plugin binary %q: %w", binaryPath, err)
	}
	if manifest.GetChecksum() != hex.EncodeToString(digest.Sum(nil)) {
		return nil, fmt.Errorf("plugin binary checksum does not match manifest")
	}

	return info, nil
}

// installedFilesPresent checks the binary and every packaged asset exist, and
// returns the binary's file info.
func installedFilesPresent(binaryPath string, manifest *pluginv1.PluginManifest) (os.FileInfo, error) {
	binaryInfo, err := os.Stat(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("plugin binary %q: %w", binaryPath, err)
	}
	if binaryInfo.IsDir() {
		return nil, fmt.Errorf("plugin binary %q is a directory", binaryPath)
	}

	for _, asset := range manifest.GetAssets() {
		resolved := filepath.Join(filepath.Dir(binaryPath), asset.GetPath())
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("plugin asset %q: %w", asset.GetPath(), err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("plugin asset %q resolved to a directory", asset.GetPath())
		}
	}

	return binaryInfo, nil
}

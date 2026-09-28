package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type archiveStore interface {
	GetArchive(ctx context.Context, installationID int) (*InstallationArchive, error)
	SaveArchive(ctx context.Context, installationID int, manifestJSON []byte, checksum string, archiveBytes []byte) error
}

type ArchiveCache struct {
	// mu protects rehydration and pruning from concurrent status and
	// reconcile calls, including cache-hit validation during extraction, and
	// guards verified.
	mu       sync.Mutex
	archives archiveStore
	// root, when set, is this host's own plugin cache dir. Installations are
	// then rehydrated under it (see LocalInstallPath) instead of at the
	// install path the API server recorded, which on a proxy node names a
	// directory on another machine.
	root string
	// verified holds, per installation, the binary this cache last hashed
	// and found matching its manifest. Only Manifest consults it.
	verified map[int]verifiedBinary
	// newHash, when set, replaces sha256.New for checking an installed
	// binary against its manifest checksum. Tests use it to count the bytes a
	// read hashes.
	newHash func() hash.Hash
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
// installation under root, the host's own plugin cache dir. A proxy node uses
// it: the API server's install paths are release identities to it, never
// paths it reads or writes.
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
func (c *ArchiveCache) LocalInstallPath(installation *Installation) string {
	if installation == nil {
		return ""
	}
	if c == nil || c.root == "" {
		return installation.InstallPath
	}
	release := filepath.Base(filepath.Dir(installation.InstallPath))
	if release == "." || release == string(filepath.Separator) || release == "" {
		release = "install"
	}
	return filepath.Join(
		c.root,
		sanitizeFilesystemSegment(installation.PluginID),
		sanitizeFilesystemSegment(installation.Version),
		sanitizeFilesystemSegment(release),
		"plugin",
	)
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

func (c *ArchiveCache) ensure(
	ctx context.Context,
	installation *Installation,
	trustVerified bool,
) (*pluginv1.PluginManifest, error) {
	if installation == nil {
		return nil, fmt.Errorf("plugin installation is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binaryPath := c.LocalInstallPath(installation)

	if manifest, err := LoadManifestFile(InstalledManifestPath(binaryPath)); err == nil {
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
	}
	// The installed files failed the check; forget any earlier verification.
	delete(c.verified, installation.ID)

	archive, err := c.archives.GetArchive(ctx, installation.ID)
	if err != nil {
		return nil, fmt.Errorf("load stored plugin archive for installation %d: %w", installation.ID, err)
	}

	reader, manifestBytes, manifest, err := openPluginArchive(archive.Bytes)
	if err != nil {
		reader, manifestBytes, manifest, err = c.recoverLegacyBinaryArchive(ctx, installation.ID, archive, err)
		if err != nil {
			return nil, fmt.Errorf("open stored plugin archive for installation %d: %w", installation.ID, err)
		}
	}
	if archive.Checksum != manifest.GetChecksum() {
		return nil, fmt.Errorf("stored plugin archive checksum mismatch for installation %d", installation.ID)
	}
	if len(archive.ManifestJSON) > 0 && !bytes.Equal(archive.ManifestJSON, manifestBytes) {
		return nil, fmt.Errorf("stored plugin manifest mismatch for installation %d", installation.ID)
	}
	if installation.PluginID != "" && manifest.GetPluginId() != installation.PluginID {
		return nil, fmt.Errorf(
			"stored plugin archive plugin_id %q does not match installation %q",
			manifest.GetPluginId(),
			installation.PluginID,
		)
	}
	if installation.Version != "" && manifest.GetVersion() != installation.Version {
		return nil, fmt.Errorf(
			"stored plugin archive version %q does not match installation %q",
			manifest.GetVersion(),
			installation.Version,
		)
	}

	installDir := filepath.Dir(binaryPath)
	if err := os.RemoveAll(installDir); err != nil {
		return nil, fmt.Errorf("clear plugin cache dir %q: %w", installDir, err)
	}
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return nil, fmt.Errorf("create plugin cache dir %q: %w", installDir, err)
	}
	if err := extractArchiveFiles(reader, installDir); err != nil {
		_ = os.RemoveAll(installDir)
		return nil, fmt.Errorf("extract stored plugin archive for installation %d: %w", installation.ID, err)
	}
	info, err := c.validateInstalledFiles(binaryPath, manifest)
	if err != nil {
		_ = os.RemoveAll(installDir)
		return nil, fmt.Errorf("validate rehydrated plugin cache for installation %d: %w", installation.ID, err)
	}
	if c.root != "" {
		// Proxy caches may contain a binary installed by an API host on a
		// different platform. Apply the same check to fresh and cached files.
		if err := checkBinaryPlatform(binaryPath); err != nil {
			_ = os.RemoveAll(installDir)
			return nil, fmt.Errorf("rehydrate plugin for installation %d: %w", installation.ID, err)
		}
	}
	c.markVerified(installation.ID, binaryPath, manifest, info)
	c.pruneStaleReleases(ctx, installation, installDir)

	return manifest, nil
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

// pruneStaleReleases drops this host's copies of the plugin's other releases
// once a new one is in place, the way the API server's installer removes the
// previous install dir on replace. Only runs under an own root: without one
// the directories belong to the installer. Best effort; a failure is logged
// and costs disk, not correctness.
func (c *ArchiveCache) pruneStaleReleases(ctx context.Context, installation *Installation, keepDir string) {
	if c == nil || c.root == "" {
		return
	}
	pluginRoot := filepath.Join(c.root, sanitizeFilesystemSegment(installation.PluginID))
	versions, err := os.ReadDir(pluginRoot)
	if err != nil {
		return
	}
	for _, version := range versions {
		if !version.IsDir() {
			continue
		}
		versionDir := filepath.Join(pluginRoot, version.Name())
		releases, err := os.ReadDir(versionDir)
		if err != nil {
			continue
		}
		for _, release := range releases {
			releaseDir := filepath.Join(versionDir, release.Name())
			if !release.IsDir() || releaseDir == keepDir {
				continue
			}
			if err := os.RemoveAll(releaseDir); err != nil {
				slog.WarnContext(ctx, "remove stale plugin release from cache", "component", "plugins",
					"installation_id", installation.ID, "path", releaseDir, "error", err)
			}
		}
		// Drop the version dir once it is empty; a non-empty one stays.
		_ = os.Remove(versionDir)
	}
}

func (c *ArchiveCache) recoverLegacyBinaryArchive(
	ctx context.Context,
	installationID int,
	archive *InstallationArchive,
	openErr error,
) (*zip.Reader, []byte, *pluginv1.PluginManifest, error) {
	if archive == nil || len(archive.ManifestJSON) == 0 || len(archive.Bytes) == 0 {
		return nil, nil, nil, openErr
	}

	manifest, err := LoadManifestBytes(archive.ManifestJSON)
	if err != nil {
		return nil, nil, nil, openErr
	}

	checksum := sha256.Sum256(archive.Bytes)
	actualChecksum := hex.EncodeToString(checksum[:])
	if actualChecksum != archive.Checksum || actualChecksum != manifest.GetChecksum() {
		return nil, nil, nil, openErr
	}

	archiveBytes, err := buildBinaryPluginArchive(archive.ManifestJSON, archive.Bytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w; recover legacy raw binary archive: %v", openErr, err)
	}

	reader, manifestBytes, manifest, err := openPluginArchive(archiveBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w; recover legacy raw binary archive: %v", openErr, err)
	}

	// Persist the repaired archive so future preloads skip recovery, but don't
	// fail startup over a write error — the in-memory archive is already valid
	// and recovery will retry on the next preload.
	if err := c.archives.SaveArchive(ctx, installationID, manifestBytes, manifest.GetChecksum(), archiveBytes); err != nil {
		slog.WarnContext(
			ctx,
			"failed to persist recovered legacy plugin archive; will retry on next preload",
			"component", "plugins",
			"installation_id", installationID,
			"error", err,
		)
	}

	return reader, manifestBytes, manifest, nil
}

func openPluginArchive(data []byte) (*zip.Reader, []byte, *pluginv1.PluginManifest, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open plugin archive: %w", err)
	}

	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		files[file.Name] = file
	}

	manifestFile, ok := files["manifest.json"]
	if !ok {
		return nil, nil, nil, fmt.Errorf("plugin archive is missing manifest.json")
	}
	binaryFile, ok := files["plugin"]
	if !ok {
		return nil, nil, nil, fmt.Errorf("plugin archive is missing plugin binary")
	}

	manifestBytes, err := readZipFile(manifestFile)
	if err != nil {
		return nil, nil, nil, err
	}
	manifest, err := LoadManifestBytes(manifestBytes)
	if err != nil {
		return nil, nil, nil, err
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
		if _, ok := files[asset.GetPath()]; !ok {
			return nil, nil, nil, fmt.Errorf("plugin archive is missing packaged asset %q", asset.GetPath())
		}
	}

	return reader, manifestBytes, manifest, nil
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

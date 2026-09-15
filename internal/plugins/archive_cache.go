package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	// reconcile calls, including cache-hit validation during extraction.
	mu       sync.Mutex
	archives archiveStore
	// root, when set, is this host's own plugin cache dir. Installations are
	// then rehydrated under it (see LocalInstallPath) instead of at the
	// install path the API server recorded, which on a proxy node names a
	// directory on another machine.
	root string
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
// and returns the installed manifest.
func (c *ArchiveCache) Ensure(ctx context.Context, installation *Installation) (*pluginv1.PluginManifest, error) {
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
		if err := validateInstalledFiles(binaryPath, manifest); err == nil {
			if c.root != "" {
				if err := checkBinaryPlatform(binaryPath); err != nil {
					return nil, fmt.Errorf("cached plugin for installation %d: %w", installation.ID, err)
				}
			}
			return manifest, nil
		}
	}

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
	if err := validateInstalledFiles(binaryPath, manifest); err != nil {
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
	c.pruneStaleReleases(ctx, installation, installDir)

	return manifest, nil
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

func validateInstalledFiles(binaryPath string, manifest *pluginv1.PluginManifest) error {
	if err := installedFilesPresent(binaryPath, manifest); err != nil {
		return err
	}

	binaryBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read plugin binary %q: %w", binaryPath, err)
	}
	checksum := sha256.Sum256(binaryBytes)
	if manifest.GetChecksum() != hex.EncodeToString(checksum[:]) {
		return fmt.Errorf("plugin binary checksum does not match manifest")
	}

	return nil
}

func installedFilesPresent(binaryPath string, manifest *pluginv1.PluginManifest) error {
	binaryInfo, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("plugin binary %q: %w", binaryPath, err)
	}
	if binaryInfo.IsDir() {
		return fmt.Errorf("plugin binary %q is a directory", binaryPath)
	}

	for _, asset := range manifest.GetAssets() {
		resolved := filepath.Join(filepath.Dir(binaryPath), asset.GetPath())
		info, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("plugin asset %q: %w", asset.GetPath(), err)
		}
		if info.IsDir() {
			return fmt.Errorf("plugin asset %q resolved to a directory", asset.GetPath())
		}
	}

	return nil
}

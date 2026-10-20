package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// pluginIdentitySegments preserves the host's existing mapping. Empty and dot
// components cannot name a plugin directory, regardless of the configured root.
func pluginIdentitySegments(pluginID, version string) (string, string, error) {
	id, err := pluginPathSegment(pluginID, "plugin id")
	if err != nil {
		return "", "", err
	}
	v, err := pluginPathSegment(version, "plugin version")
	if err != nil {
		return "", "", err
	}
	return id, v, nil
}

func pluginPathSegment(value, label string) (string, error) {
	segment := sanitizeFilesystemSegment(value)
	if segment == "" || segment == "." || segment == ".." {
		return "", fmt.Errorf("%s %q is not a directory component", label, value)
	}
	return segment, nil
}

func pluginVersionRoot(root, pluginID, version string) (string, error) {
	id, v, err := pluginIdentitySegments(pluginID, version)
	if err != nil {
		return "", err
	}
	return pluginPathUnder(root, id, v)
}

// pluginPathUnder checks lexical containment of newly derived paths. Local roots
// and host-written stored paths are trusted; this does not guard against local
// writers changing symlinks between a check and a filesystem operation.
func pluginPathUnder(root string, parts ...string) (string, error) {
	path := filepath.Join(append([]string{root}, parts...)...)
	if _, err := pluginRelativeDescendant(root, path); err != nil {
		return "", err
	}
	return path, nil
}

func pluginRelativeDescendant(root, path string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin path %q is not below root %q", path, root)
	}
	return rel, nil
}

// removeInstallationFiles accepts paths written by the host, including legacy
// layouts and roots that differ from today's configuration. Invalid identities
// may have produced an unsafe path before admission checks existed; leave those
// files alone. The caller decides whether cleanup failure fails its operation.
func removeInstallationFiles(installation *Installation, removeAll func(string) error) error {
	if _, _, err := pluginIdentitySegments(installation.PluginID, installation.Version); err != nil {
		return err
	}
	return removeAll(filepath.Dir(installation.InstallPath))
}

func cleanupReplacedInstallation(ctx context.Context, existing *Installation, installDir string, removeAll func(string) error) {
	if filepath.Clean(filepath.Dir(existing.InstallPath)) == filepath.Clean(installDir) {
		return
	}
	if err := removeInstallationFiles(existing, removeAll); err != nil {
		slog.WarnContext(ctx, "remove replaced plugin installation files", "component", "plugins",
			"installation_id", existing.ID, "error", err)
	}
}

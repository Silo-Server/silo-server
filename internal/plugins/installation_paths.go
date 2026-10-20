package plugins

import (
	"fmt"
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
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin path %q is not below root %q", path, root)
	}
	return path, nil
}

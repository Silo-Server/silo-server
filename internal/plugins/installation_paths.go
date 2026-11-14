package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidPluginIdentity marks a plugin ID or version that cannot name a
// plugin directory. Admission refuses such packages; a row admitted before
// that check fails every load with it, which list reads tolerate per row.
var ErrInvalidPluginIdentity = errors.New("plugin identity is not a directory component")

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
		return "", fmt.Errorf("%s %q: %w", label, value, ErrInvalidPluginIdentity)
	}
	return segment, nil
}

// cacheSegmentEscaper names a plugin's directory under a node's own archive
// cache. The installer's mapping replaces / \ : @ with _, so acme/x and
// acme_x would share a directory, and each rehydration prunes every other
// release under it (#2800). Escaping them, and the escape character %, gives
// distinct identities distinct directories; an identity without these
// characters keeps its name.
var cacheSegmentEscaper = strings.NewReplacer("%", "%25", "/", "%2F", "\\", "%5C", ":", "%3A", "@", "%40")

// maxCacheSegmentBytes is the longest directory name the cache writes, the
// usual filesystem limit for one path component.
const maxCacheSegmentBytes = 255

func cachePathSegment(value, label string) (string, error) {
	segment := cacheSegmentEscaper.Replace(value)
	if segment == "" || segment == "." || segment == ".." {
		return "", fmt.Errorf("%s %q: %w", label, value, ErrInvalidPluginIdentity)
	}
	// Escaping can triple an identity's length past the limit. Such an
	// identity is named by its hash instead; "%h" never starts an escape, so
	// no escaped name can equal a hashed one.
	if len(segment) > maxCacheSegmentBytes {
		sum := sha256.Sum256([]byte(value))
		segment = "%h" + hex.EncodeToString(sum[:])
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

// removeInstallationFiles removes an installation's release directory. Stored
// paths are host-written and trusted for valid identities, including legacy
// layouts and roots from another replica or configuration. A row with an
// invalid identity may predate the admission check, and in a legacy layout its
// directory can sit above the plugin root, so its files are left in place: the
// row is still removed and the caller's operation still succeeds.
func removeInstallationFiles(ctx context.Context, installation *Installation) error {
	dir := filepath.Dir(installation.InstallPath)
	reason := ""
	if _, _, err := pluginIdentitySegments(installation.PluginID, installation.Version); err != nil {
		reason = err.Error()
	} else if dir == "." || dir == filepath.Dir(dir) {
		reason = "install path has no release directory"
	}
	if reason != "" {
		slog.WarnContext(ctx, "leaving plugin installation files in place", "component", "plugins",
			"installation_id", installation.ID, "plugin_id", installation.PluginID, "path", dir, "reason", reason)
		return nil
	}
	return os.RemoveAll(dir) //nolint:gosec // host-written install path of a row with a valid identity, checked above.
}

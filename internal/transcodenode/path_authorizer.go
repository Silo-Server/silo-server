package transcodenode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// InputPathAuthorizer approves a local media input before a node passes it to
// FFmpeg. Implementations must reject protocol URLs and paths outside the
// authoritative media catalog.
type InputPathAuthorizer interface {
	Allowed(ctx context.Context, path string) (bool, error)
}

type catalogPathSource interface {
	IsActivePath(ctx context.Context, path string) (bool, error)
}

// CatalogPathAuthorizer permits only existing regular files whose exact
// logical path is active in the media catalog. The scanner deliberately keeps
// logical paths for readable symlinks, so catalog membership is the correct
// authority: resolving the target and requiring it to remain under the logical
// library root would reject media layouts the scanner explicitly supports.
type CatalogPathAuthorizer struct {
	paths catalogPathSource
}

// NewCatalogPathAuthorizer creates an FFmpeg input authorizer backed by the
// authoritative media-file catalog.
func NewCatalogPathAuthorizer(paths catalogPathSource) *CatalogPathAuthorizer {
	return &CatalogPathAuthorizer{paths: paths}
}

// Allowed reports whether path is an active catalog entry that resolves to a
// regular file on this node. os.Stat follows scanner-approved symlinks while
// rejecting dangling links, directories, and other non-regular inputs.
func (a *CatalogPathAuthorizer) Allowed(ctx context.Context, path string) (bool, error) {
	if a == nil || a.paths == nil || !plainAbsolutePath(path) {
		return false, nil
	}
	active, err := a.paths.IsActivePath(ctx, path)
	if err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular(), nil
}

func plainAbsolutePath(path string) bool {
	path = strings.TrimSpace(path)
	return path != "" && !strings.ContainsRune(path, '\x00') && filepath.IsAbs(path)
}

// pathWithinRoot validates a not-yet-created output by resolving the root and
// target parent. The caller creates the basename only after this check.
func pathWithinRoot(root, target string) bool {
	if !plainAbsolutePath(root) || !plainAbsolutePath(target) {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return false
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(target)))
	if err != nil {
		return false
	}
	resolvedTarget := filepath.Join(resolvedParent, filepath.Base(target))
	return resolvedPathContained(resolvedRoot, resolvedTarget)
}

func resolvedPathContained(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

package transcodenode

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// InputPathAuthorizer approves a local media input before a node passes it to
// FFmpeg. Implementations must reject protocol URLs and paths outside managed
// library roots.
type InputPathAuthorizer interface {
	Allowed(ctx context.Context, path string) (bool, error)
}

type mediaFolderSource interface {
	List(ctx context.Context) ([]*models.MediaFolder, error)
}

// MediaRootAuthorizer resolves the deployment's current library roots and
// permits only existing, absolute filesystem paths contained by those roots.
type MediaRootAuthorizer struct {
	folders mediaFolderSource
}

// NewMediaRootAuthorizer creates an FFmpeg input authorizer backed by the
// authoritative media-folder repository.
func NewMediaRootAuthorizer(folders mediaFolderSource) *MediaRootAuthorizer {
	return &MediaRootAuthorizer{folders: folders}
}

// Allowed reports whether path resolves inside one of the configured media
// roots. Symlinks are resolved on both sides so a link cannot escape a root.
func (a *MediaRootAuthorizer) Allowed(ctx context.Context, path string) (bool, error) {
	if a == nil || a.folders == nil || !plainAbsolutePath(path) {
		return false, nil
	}
	folders, err := a.folders.List(ctx)
	if err != nil {
		return false, err
	}
	for _, folder := range folders {
		if folder == nil {
			continue
		}
		for _, root := range folder.Paths {
			if existingPathWithinRoot(root, path) {
				return true, nil
			}
		}
	}
	return false, nil
}

func plainAbsolutePath(path string) bool {
	path = strings.TrimSpace(path)
	return path != "" && !strings.ContainsRune(path, '\x00') && filepath.IsAbs(path)
}

func existingPathWithinRoot(root, target string) bool {
	if !plainAbsolutePath(root) || !plainAbsolutePath(target) {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(filepath.Clean(target))
	if err != nil {
		return false
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return resolvedPathContained(resolvedRoot, resolvedTarget)
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

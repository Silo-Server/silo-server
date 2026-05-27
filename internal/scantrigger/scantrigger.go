package scantrigger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

const (
	ModeLibrary = "library"
	ModeSubtree = "subtree"
	ModeFile    = "file"
)

type FolderRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
	List(ctx context.Context) ([]*models.MediaFolder, error)
}

type Queuer interface {
	EnqueueScan(ctx context.Context, folderID int, mode, path, trigger string) (bool, error)
	EnqueueScans(ctx context.Context, targets []Target) error
}

type Request struct {
	LibraryID *int
	Path      string
	Trigger   string
}

// Target is a fully-resolved scan request. Folder is always non-nil for
// targets returned by Resolver; callers should read the library ID via
// target.Folder.ID rather than tracking it separately.
type Target struct {
	Folder  *models.MediaFolder
	Mode    string
	Path    string
	Trigger string
}

type RequestError struct {
	Status  int
	Code    string
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}

type Resolver struct {
	folders FolderRepository
}

func NewResolver(folders FolderRepository) *Resolver {
	return &Resolver{folders: folders}
}

func (r *Resolver) ResolveAll(ctx context.Context, requests []Request) ([]Target, error) {
	targets := make([]Target, 0, len(requests))
	var pathFolders []*models.MediaFolder
	pathFoldersLoaded := false
	for _, req := range requests {
		usePathFolders := req.LibraryID == nil && strings.TrimSpace(req.Path) != ""
		if usePathFolders && !pathFoldersLoaded {
			if r == nil || r.folders == nil {
				return nil, &RequestError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Scanner not available"}
			}
			folders, listErr := r.folders.List(ctx)
			if listErr != nil {
				return nil, fmt.Errorf("listing libraries for scan: %w", listErr)
			}
			pathFolders = folders
			pathFoldersLoaded = true
		}

		target, err := r.resolve(ctx, req, pathFolders, usePathFolders)
		if err != nil {
			return nil, err
		}
		targets = append(targets, *target)
	}
	return targets, nil
}

func (r *Resolver) Resolve(ctx context.Context, req Request) (*Target, error) {
	return r.resolve(ctx, req, nil, false)
}

func (r *Resolver) resolve(ctx context.Context, req Request, pathFolders []*models.MediaFolder, usePathFolders bool) (*Target, error) {
	if r == nil || r.folders == nil {
		return nil, &RequestError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Scanner not available"}
	}
	if req.LibraryID == nil && strings.TrimSpace(req.Path) == "" {
		return nil, &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Either library_id or path is required"}
	}

	var folder *models.MediaFolder
	var err error
	if req.LibraryID != nil {
		folder, err = r.folders.GetByID(ctx, *req.LibraryID)
		if err != nil {
			if errors.Is(err, catalog.ErrFolderNotFound) {
				return nil, &RequestError{Status: http.StatusNotFound, Code: "not_found", Message: "Library not found"}
			}
			return nil, fmt.Errorf("fetching library for scan: %w", err)
		}
	}

	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	if strings.TrimSpace(req.Path) == "" {
		if folder != nil && !folder.Enabled {
			return nil, &RequestError{Status: http.StatusConflict, Code: "conflict", Message: "Library is disabled"}
		}
		return &Target{Folder: folder, Mode: ModeLibrary, Trigger: trigger}, nil
	}

	cleanPath := filepath.Clean(req.Path)
	var matchedRoot string
	if folder != nil {
		matchedRoot, err = LongestMatchingRoot(cleanPath, folder.Paths)
		if err != nil {
			return nil, err
		}
		if matchedRoot == "" {
			return nil, &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path does not belong to the specified library"}
		}
	} else {
		folders := pathFolders
		if !usePathFolders {
			var listErr error
			folders, listErr = r.folders.List(ctx)
			if listErr != nil {
				return nil, fmt.Errorf("listing libraries for scan: %w", listErr)
			}
		}
		folder, matchedRoot, err = MatchFolderForPath(cleanPath, folders)
		if err != nil {
			return nil, err
		}
	}
	if folder != nil && !folder.Enabled {
		return nil, &RequestError{Status: http.StatusConflict, Code: "conflict", Message: "Library is disabled"}
	}

	mode, err := ClassifyPath(cleanPath, matchedRoot)
	if err != nil {
		return nil, err
	}
	if trigger == "manual" {
		trigger = "path"
		if req.LibraryID != nil {
			trigger = "library_id_path"
		}
	}

	targetPath := cleanPath
	if mode == ModeLibrary {
		targetPath = ""
	}
	return &Target{Folder: folder, Mode: mode, Path: targetPath, Trigger: trigger}, nil
}

func EnqueueAll(ctx context.Context, queue Queuer, targets []Target) error {
	if queue == nil {
		return &RequestError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Scanner not available"}
	}
	if err := queue.EnqueueScans(ctx, targets); err != nil {
		return fmt.Errorf("queueing library scans: %w", err)
	}
	return nil
}

func LongestMatchingRoot(targetPath string, roots []string) (string, error) {
	bestRoot := ""
	bestLen := -1
	for _, root := range roots {
		if !PathWithinRoot(targetPath, root) {
			continue
		}
		cleanRoot := filepath.Clean(root)
		rootLen := len(cleanRoot)
		if rootLen > bestLen {
			bestRoot = cleanRoot
			bestLen = rootLen
		}
	}
	return bestRoot, nil
}

func MatchFolderForPath(targetPath string, folders []*models.MediaFolder) (*models.MediaFolder, string, error) {
	var bestFolder *models.MediaFolder
	bestRoot := ""
	bestLen := -1
	ambiguous := false

	for _, folder := range folders {
		if folder == nil {
			continue
		}
		root, err := LongestMatchingRoot(targetPath, folder.Paths)
		if err != nil {
			return nil, "", err
		}
		if root == "" {
			continue
		}
		rootLen := len(root)
		if rootLen > bestLen {
			bestFolder = folder
			bestRoot = root
			bestLen = rootLen
			ambiguous = false
			continue
		}
		if rootLen == bestLen && bestFolder != nil && folder.ID != bestFolder.ID {
			ambiguous = true
		}
	}

	if ambiguous {
		return nil, "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path matches multiple libraries"}
	}
	if bestFolder == nil {
		return nil, "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "No library matches the given path"}
	}
	return bestFolder, bestRoot, nil
}

func ClassifyPath(targetPath, matchedRoot string) (string, error) {
	if filepath.Clean(targetPath) == filepath.Clean(matchedRoot) {
		return ModeLibrary, nil
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path does not exist"}
		case errors.Is(err, os.ErrPermission):
			return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Permission denied for path"}
		default:
			return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path could not be inspected"}
		}
	}
	if info.IsDir() {
		return ModeSubtree, nil
	}
	if !info.Mode().IsRegular() {
		return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Path must be a file or directory"}
	}
	if !scanner.SupportsVideoFile(targetPath) {
		return "", &RequestError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Unsupported media file extension"}
	}
	return ModeFile, nil
}

func PathWithinRoot(targetPath, rootPath string) bool {
	cleanTarget := filepath.Clean(targetPath)
	cleanRoot := filepath.Clean(rootPath)
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return false
	}
	if rel == "." || rel == "" {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

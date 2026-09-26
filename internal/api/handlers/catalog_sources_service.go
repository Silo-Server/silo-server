package handlers

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

type CatalogImportSource struct {
	Key          string
	SizeBytes    int64
	LastModified *time.Time
}

func (h *CatalogSeedHandler) ListCatalogImportSourcesPage(ctx context.Context, token string, limit int) ([]CatalogImportSource, string, error) {
	store, ok := h.store.(interface {
		ListObjectInfosPage(context.Context, string, string, string, int) ([]s3client.ObjectInfo, string, error)
	})
	if !ok {
		return nil, "", apiError(http.StatusServiceUnavailable, "unavailable", "Catalog import storage is unavailable")
	}
	objects, next, err := store.ListObjectInfosPage(ctx, h.store.Bucket(), "catalog-seeds/", token, limit)
	if err != nil {
		return nil, "", err
	}
	sources := make([]CatalogImportSource, 0, len(objects))
	for _, obj := range objects {
		if strings.HasSuffix(strings.ToLower(obj.Key), ".json.gz") {
			sources = append(sources, CatalogImportSource{Key: obj.Key, SizeBytes: obj.SizeBytes, LastModified: obj.LastModified})
		}
	}
	return sources, next, nil
}

func (h *CatalogSeedHandler) ListLocalCatalogImportSourcesPage(ctx context.Context, after string, limit int) ([]CatalogImportSource, error) {
	dir := h.localImportDirectory()
	return scanCatalogDirectory(ctx, dir, after, limit, func(entry os.DirEntry) (CatalogImportSource, bool) {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json.gz") {
			return CatalogImportSource{}, false
		}
		info, err := entry.Info()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err = os.Stat(filepath.Join(dir, entry.Name()))
		}
		if err != nil || !info.Mode().IsRegular() {
			return CatalogImportSource{}, false
		}
		modified := info.ModTime()
		return CatalogImportSource{Key: filepath.Join(dir, entry.Name()), SizeBytes: info.Size(), LastModified: &modified}, true
	}, true)
}

type FilesystemDirectoryPage struct {
	Path    string
	Parent  string
	Entries []CatalogImportSource
}

func (h *FilesystemHandler) BrowseDirectoryPage(ctx context.Context, path, namePrefix, after string, limit int) (FilesystemDirectoryPage, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = string(filepath.Separator)
	}
	if !filepath.IsAbs(path) {
		return FilesystemDirectoryPage{}, apiError(http.StatusBadRequest, "bad_request", "path must be absolute")
	}
	path = filepath.Clean(path)
	entries, err := scanCatalogDirectory(ctx, path, after, limit, func(entry os.DirEntry) (CatalogImportSource, bool) {
		if !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(namePrefix)) {
			return CatalogImportSource{}, false
		}
		full := filepath.Join(path, entry.Name())
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink == 0 {
				return CatalogImportSource{}, false
			}
			info, err := os.Stat(full)
			if err != nil || !info.IsDir() {
				return CatalogImportSource{}, false
			}
		}
		return CatalogImportSource{Key: full}, true
	}, false)
	return FilesystemDirectoryPage{Path: path, Parent: filepath.Dir(path), Entries: entries}, err
}

// Directory enumeration is not ordered by the OS. Scan in fixed batches and keep
// only the smallest requested keys; memory stays bounded even in large folders.
// Concurrent directory changes are visible on subsequent pages, not a snapshot.
func scanCatalogDirectory(ctx context.Context, path, after string, limit int, include func(os.DirEntry) (CatalogImportSource, bool), missingIsEmpty bool) ([]CatalogImportSource, error) {
	if limit < 1 || limit > 201 {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Invalid page limit")
	}
	dir, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if missingIsEmpty {
				return []CatalogImportSource{}, nil
			}
			return nil, apiError(http.StatusNotFound, "not_found", "Directory not found")
		}
		return nil, apiError(http.StatusBadRequest, "bad_request", "Cannot open directory")
	}
	defer func() { _ = dir.Close() }()
	result := make([]CatalogImportSource, 0, limit+128)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, apiError(http.StatusBadRequest, "bad_request", "Cannot read directory")
		}
		for _, entry := range entries {
			if filepath.Join(path, entry.Name()) <= after {
				continue
			}
			if source, ok := include(entry); ok {
				result = append(result, source)
			}
		}
		slices.SortFunc(result, func(a, b CatalogImportSource) int { return cmp.Compare(a.Key, b.Key) })
		result = result[:min(len(result), limit)]
		if errors.Is(err, io.EOF) {
			return result, nil
		}
	}
}

package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type filesystemBrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type filesystemBrowseResponse struct {
	Path    string                  `json:"path"`
	Parent  string                  `json:"parent"`
	Entries []filesystemBrowseEntry `json:"entries"`
}

type FilesystemHandler struct{}

func NewFilesystemHandler() *FilesystemHandler { return &FilesystemHandler{} }

func (h *FilesystemHandler) HandleBrowse(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = string(filepath.Separator)
	}
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, "bad_request", "path must be an absolute path")
		return
	}

	cleaned := filepath.Clean(path)
	info, err := os.Stat(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not_found", "Directory not found")
		} else {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid path")
		}
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "bad_request", "path must point to a directory")
		return
	}

	entries, err := os.ReadDir(cleaned)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read directory")
		return
	}

	result := make([]filesystemBrowseEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		full := filepath.Join(cleaned, name)

		// os.ReadDir reports lstat semantics, so a symlink to a directory has
		// IsDir() == false. Follow symlink entries with os.Stat and keep them only
		// when the target is a directory; skip links whose target cannot be stat'd
		// (broken or looping). Non-symlink, non-directory entries (regular files)
		// are skipped without the extra stat so large media folders stay fast.
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Stat(full)
			if err != nil || !target.IsDir() {
				continue
			}
		}

		result = append(result, filesystemBrowseEntry{
			Name: name,
			Path: full,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Path < result[j].Path
		}
		return result[i].Name < result[j].Name
	})

	parent := filepath.Dir(cleaned)
	if cleaned == string(filepath.Separator) || parent == "." || parent == cleaned {
		parent = cleaned
	}

	writeJSON(w, http.StatusOK, filesystemBrowseResponse{
		Path:    cleaned,
		Parent:  parent,
		Entries: result,
	})
}

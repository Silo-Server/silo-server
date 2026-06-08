package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// EbookReaderHandler serves ebook files for the in-app reader.
type EbookReaderHandler struct {
	FileAuthorizer *MediaFileAuthorizer
}

func NewEbookReaderHandler(authorizer *MediaFileAuthorizer) *EbookReaderHandler {
	return &EbookReaderHandler{FileAuthorizer: authorizer}
}

// HandleReadFile serves an ebook file inline with byte-range support.
func (h *EbookReaderHandler) HandleReadFile(w http.ResponseWriter, r *http.Request) {
	if apimw.GetUserID(r.Context()) == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.FileAuthorizer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ebook reader is not configured")
		return
	}

	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	fileID, err := strconv.Atoi(chi.URLParam(r, "file_id"))
	if contentID == "" || err != nil || fileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id and file_id are required")
		return
	}

	file, err := h.FileAuthorizer.Authorize(r, fileID)
	if err != nil {
		h.writeReadError(w, err)
		return
	}
	if file == nil || file.ContentID != contentID || !isEbookFile(file) {
		writeError(w, http.StatusNotFound, "not_found", "Ebook file not found")
		return
	}

	if err := serveEbookInline(w, r, file); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Ebook file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to serve ebook file")
	}
}

func (h *EbookReaderHandler) writeReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrItemNotFound), errors.Is(err, catalog.ErrEpisodeNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Ebook file not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize ebook file")
	}
}

func serveEbookInline(w http.ResponseWriter, r *http.Request, file *models.MediaFile) error {
	f, err := os.Open(file.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return catalog.ErrItemNotFound
		}
		return fmt.Errorf("opening ebook file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat ebook file: %w", err)
	}

	filename := sanitizeInlineFilename(filepath.Base(file.FilePath))
	w.Header().Set("Content-Type", ebookMimeType(file.FilePath, file.Container))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
	return nil
}

func isEbookFile(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if !strings.EqualFold(file.BaseType, "ebook") {
		return false
	}
	return isEbookReaderFormat(file.Container) || isEbookReaderFormat(filepath.Ext(file.FilePath))
}

func isEbookReaderFormat(value string) bool {
	format := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	switch format {
	case "epub", "pdf", "mobi", "azw", "azw3", "cbz", "cbr", "fb2", "fbz", "txt", "md":
		return true
	default:
		return false
	}
}

func ebookMimeType(path, container string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" && container != "" {
		ext = "." + strings.TrimPrefix(strings.ToLower(container), ".")
	}
	switch ext {
	case ".epub":
		return "application/epub+zip"
	case ".pdf":
		return "application/pdf"
	case ".mobi":
		return "application/x-mobipocket-ebook"
	case ".azw":
		return "application/vnd.amazon.ebook"
	case ".azw3":
		return "application/vnd.amazon.mobi8-ebook"
	case ".cbz":
		return "application/vnd.comicbook+zip"
	case ".cbr":
		return "application/vnd.comicbook-rar"
	case ".fb2":
		return "application/x-fictionbook+xml"
	case ".fbz":
		return "application/x-zip-compressed-fb2"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func sanitizeInlineFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "ebook"
	}
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	return name
}

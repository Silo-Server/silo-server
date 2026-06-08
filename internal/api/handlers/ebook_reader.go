package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// EbookReaderProgress is the persisted reader position for one profile and ebook.
type EbookReaderProgress struct {
	UserID    int       `json:"-"`
	ProfileID string    `json:"-"`
	ContentID string    `json:"content_id"`
	FileID    int       `json:"file_id"`
	Location  string    `json:"location"`
	Progress  float64   `json:"progress"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EbookReaderProgressStore interface {
	Get(ctx context.Context, userID int, profileID string, contentID string) (*EbookReaderProgress, error)
	Upsert(ctx context.Context, progress EbookReaderProgress) error
}

// EbookReaderHandler serves ebook files for the in-app reader.
type EbookReaderHandler struct {
	FileAuthorizer *MediaFileAuthorizer
	ProgressStore  EbookReaderProgressStore
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

type ebookReaderProgressRequest struct {
	FileID   int     `json:"file_id"`
	Location string  `json:"location"`
	Progress float64 `json:"progress"`
}

func (h *EbookReaderHandler) HandleGetProgress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.ProgressStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ebook reader progress is not configured")
		return
	}

	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}
	if err := h.FileAuthorizer.ItemAccess.EnsureAccessible(r.Context(), contentID, requestAccessFilter(r)); err != nil {
		h.writeReadError(w, err)
		return
	}

	progress, err := h.ProgressStore.Get(r.Context(), userID, profileID, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load ebook progress")
		return
	}
	if progress == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (h *EbookReaderHandler) HandleSaveProgress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.ProgressStore == nil || h.FileAuthorizer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ebook reader progress is not configured")
		return
	}

	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}

	var req ebookReaderProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.Location = strings.TrimSpace(req.Location)
	if req.FileID <= 0 || req.Location == "" || req.Progress < 0 || req.Progress > 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "file_id, location, and progress are required")
		return
	}

	file, err := h.FileAuthorizer.Authorize(r, req.FileID)
	if err != nil {
		h.writeReadError(w, err)
		return
	}
	if file == nil || file.ContentID != contentID || !isEbookFile(file) {
		writeError(w, http.StatusNotFound, "not_found", "Ebook file not found")
		return
	}

	progress := EbookReaderProgress{
		UserID:    userID,
		ProfileID: profileID,
		ContentID: contentID,
		FileID:    req.FileID,
		Location:  req.Location,
		Progress:  req.Progress,
		UpdatedAt: time.Now().UTC(),
	}
	if err := h.ProgressStore.Upsert(r.Context(), progress); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save ebook progress")
		return
	}
	writeJSON(w, http.StatusOK, progress)
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

type PGEbookReaderProgressStore struct {
	pool *pgxpool.Pool
}

func NewPGEbookReaderProgressStore(pool *pgxpool.Pool) *PGEbookReaderProgressStore {
	return &PGEbookReaderProgressStore{pool: pool}
}

func (s *PGEbookReaderProgressStore) Get(ctx context.Context, userID int, profileID string, contentID string) (*EbookReaderProgress, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook reader progress store is not configured")
	}
	var progress EbookReaderProgress
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, profile_id, content_id, file_id, location, progress, updated_at
		FROM ebook_reader_progress
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`,
		userID, profileID, contentID,
	).Scan(
		&progress.UserID,
		&progress.ProfileID,
		&progress.ContentID,
		&progress.FileID,
		&progress.Location,
		&progress.Progress,
		&progress.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ebook reader progress: %w", err)
	}
	return &progress, nil
}

func (s *PGEbookReaderProgressStore) Upsert(ctx context.Context, progress EbookReaderProgress) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("ebook reader progress store is not configured")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ebook_reader_progress
			(user_id, profile_id, content_id, file_id, location, progress, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, profile_id, content_id) DO UPDATE SET
			file_id = EXCLUDED.file_id,
			location = EXCLUDED.location,
			progress = EXCLUDED.progress,
			updated_at = EXCLUDED.updated_at`,
		progress.UserID,
		progress.ProfileID,
		progress.ContentID,
		progress.FileID,
		progress.Location,
		progress.Progress,
		progress.UpdatedAt,
	); err != nil {
		return fmt.Errorf("upsert ebook reader progress: %w", err)
	}
	return nil
}

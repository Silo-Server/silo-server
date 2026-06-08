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
	"github.com/google/uuid"
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

// EbookReaderConfig is persisted reader configuration for one profile and ebook.
type EbookReaderConfig struct {
	UserID    int             `json:"-"`
	ProfileID string          `json:"-"`
	ContentID string          `json:"content_id"`
	Config    json.RawMessage `json:"config"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type EbookReaderConfigStore interface {
	Get(ctx context.Context, userID int, profileID string, contentID string) (*EbookReaderConfig, error)
	Upsert(ctx context.Context, config EbookReaderConfig) error
}

// EbookReaderAnnotation is a persisted reader annotation or bookmark.
type EbookReaderAnnotation struct {
	ID           string          `json:"id"`
	UserID       int             `json:"-"`
	ProfileID    string          `json:"-"`
	ContentID    string          `json:"content_id"`
	Kind         string          `json:"kind"`
	CFIRange     string          `json:"cfi_range,omitempty"`
	Location     string          `json:"location,omitempty"`
	SelectedText string          `json:"selected_text"`
	Note         string          `json:"note"`
	Style        string          `json:"style"`
	Color        string          `json:"color"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type EbookReaderAnnotationStore interface {
	List(ctx context.Context, userID int, profileID string, contentID string) ([]EbookReaderAnnotation, error)
	Create(ctx context.Context, annotation EbookReaderAnnotation) error
	Update(ctx context.Context, annotation EbookReaderAnnotation) (*EbookReaderAnnotation, error)
	Delete(ctx context.Context, userID int, profileID string, contentID string, annotationID string) error
}

// EbookReaderHandler serves ebook files for the in-app reader.
type EbookReaderHandler struct {
	FileAuthorizer  *MediaFileAuthorizer
	ProgressStore   EbookReaderProgressStore
	ConfigStore     EbookReaderConfigStore
	AnnotationStore EbookReaderAnnotationStore
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

type ebookReaderConfigRequest struct {
	Config json.RawMessage `json:"config"`
}

type ebookReaderAnnotationRequest struct {
	Kind         string          `json:"kind"`
	CFIRange     string          `json:"cfi_range"`
	Location     string          `json:"location"`
	SelectedText string          `json:"selected_text"`
	Note         string          `json:"note"`
	Style        string          `json:"style"`
	Color        string          `json:"color"`
	Metadata     json.RawMessage `json:"metadata"`
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

func (h *EbookReaderHandler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.ConfigStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ebook reader config is not configured")
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

	config, err := h.ConfigStore.Get(r.Context(), userID, profileID, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load ebook reader config")
		return
	}
	if config == nil {
		writeJSON(w, http.StatusOK, map[string]any{"config": map[string]any{}})
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (h *EbookReaderHandler) HandleSaveConfig(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.ConfigStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ebook reader config is not configured")
		return
	}

	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}

	var req ebookReaderConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if !jsonObject(req.Config) {
		writeError(w, http.StatusBadRequest, "bad_request", "config must be a JSON object")
		return
	}
	if err := h.FileAuthorizer.ItemAccess.EnsureAccessible(r.Context(), contentID, requestAccessFilter(r)); err != nil {
		h.writeReadError(w, err)
		return
	}

	config := EbookReaderConfig{
		UserID:    userID,
		ProfileID: profileID,
		ContentID: contentID,
		Config:    req.Config,
		UpdatedAt: time.Now().UTC(),
	}
	if err := h.ConfigStore.Upsert(r.Context(), config); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save ebook reader config")
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func jsonObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value map[string]any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func (h *EbookReaderHandler) HandleListAnnotations(w http.ResponseWriter, r *http.Request) {
	userID, profileID, contentID, ok := h.annotationRequestScope(w, r)
	if !ok {
		return
	}
	items, err := h.AnnotationStore.List(r.Context(), userID, profileID, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load ebook annotations")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *EbookReaderHandler) HandleCreateAnnotation(w http.ResponseWriter, r *http.Request) {
	userID, profileID, contentID, ok := h.annotationRequestScope(w, r)
	if !ok {
		return
	}
	var req ebookReaderAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	annotation, err := buildEbookReaderAnnotation(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	now := time.Now().UTC()
	annotation.ID = uuid.NewString()
	annotation.UserID = userID
	annotation.ProfileID = profileID
	annotation.ContentID = contentID
	annotation.CreatedAt = now
	annotation.UpdatedAt = now
	if err := h.AnnotationStore.Create(r.Context(), annotation); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create ebook annotation")
		return
	}
	writeJSON(w, http.StatusCreated, annotation)
}

func (h *EbookReaderHandler) HandleUpdateAnnotation(w http.ResponseWriter, r *http.Request) {
	userID, profileID, contentID, ok := h.annotationRequestScope(w, r)
	if !ok {
		return
	}
	annotationID := strings.TrimSpace(chi.URLParam(r, "annotation_id"))
	if annotationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "annotation_id is required")
		return
	}
	var req ebookReaderAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	annotation, err := buildEbookReaderAnnotationPatch(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	annotation.ID = annotationID
	annotation.UserID = userID
	annotation.ProfileID = profileID
	annotation.ContentID = contentID
	annotation.UpdatedAt = time.Now().UTC()
	updated, err := h.AnnotationStore.Update(r.Context(), annotation)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update ebook annotation")
		return
	}
	if updated == nil {
		writeError(w, http.StatusNotFound, "not_found", "Ebook annotation not found")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *EbookReaderHandler) HandleDeleteAnnotation(w http.ResponseWriter, r *http.Request) {
	userID, profileID, contentID, ok := h.annotationRequestScope(w, r)
	if !ok {
		return
	}
	annotationID := strings.TrimSpace(chi.URLParam(r, "annotation_id"))
	if annotationID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "annotation_id is required")
		return
	}
	if err := h.AnnotationStore.Delete(r.Context(), userID, profileID, contentID, annotationID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete ebook annotation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EbookReaderHandler) annotationRequestScope(w http.ResponseWriter, r *http.Request) (int, string, string, bool) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return 0, "", "", false
	}
	if h == nil || h.AnnotationStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ebook annotations are not configured")
		return 0, "", "", false
	}
	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return 0, "", "", false
	}
	if err := h.FileAuthorizer.ItemAccess.EnsureAccessible(r.Context(), contentID, requestAccessFilter(r)); err != nil {
		h.writeReadError(w, err)
		return 0, "", "", false
	}
	return userID, profileID, contentID, true
}

func buildEbookReaderAnnotation(req ebookReaderAnnotationRequest) (EbookReaderAnnotation, error) {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = "highlight"
	}
	if kind != "highlight" && kind != "note" && kind != "bookmark" {
		return EbookReaderAnnotation{}, fmt.Errorf("kind must be highlight, note, or bookmark")
	}
	cfiRange := strings.TrimSpace(req.CFIRange)
	location := strings.TrimSpace(req.Location)
	if kind == "bookmark" {
		if location == "" {
			return EbookReaderAnnotation{}, fmt.Errorf("location is required for bookmarks")
		}
	} else if cfiRange == "" {
		return EbookReaderAnnotation{}, fmt.Errorf("cfi_range is required for annotations")
	}
	metadata := req.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if !jsonObject(metadata) {
		return EbookReaderAnnotation{}, fmt.Errorf("metadata must be a JSON object")
	}
	style := strings.TrimSpace(req.Style)
	if style == "" {
		style = "highlight"
	}
	color := strings.TrimSpace(req.Color)
	if color == "" {
		color = "#facc15"
	}
	return EbookReaderAnnotation{
		Kind:         kind,
		CFIRange:     cfiRange,
		Location:     location,
		SelectedText: strings.TrimSpace(req.SelectedText),
		Note:         strings.TrimSpace(req.Note),
		Style:        style,
		Color:        color,
		Metadata:     metadata,
	}, nil
}

func buildEbookReaderAnnotationPatch(req ebookReaderAnnotationRequest) (EbookReaderAnnotation, error) {
	metadata := req.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if !jsonObject(metadata) {
		return EbookReaderAnnotation{}, fmt.Errorf("metadata must be a JSON object")
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "" && kind != "highlight" && kind != "note" && kind != "bookmark" {
		return EbookReaderAnnotation{}, fmt.Errorf("kind must be highlight, note, or bookmark")
	}
	return EbookReaderAnnotation{
		Kind:         kind,
		CFIRange:     strings.TrimSpace(req.CFIRange),
		Location:     strings.TrimSpace(req.Location),
		SelectedText: strings.TrimSpace(req.SelectedText),
		Note:         strings.TrimSpace(req.Note),
		Style:        strings.TrimSpace(req.Style),
		Color:        strings.TrimSpace(req.Color),
		Metadata:     metadata,
	}, nil
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
	if strings.HasSuffix(strings.ToLower(file.FilePath), ".fb2.zip") {
		return true
	}
	return isEbookReaderFormat(file.Container) || isEbookReaderFormat(filepath.Ext(file.FilePath))
}

func isEbookReaderFormat(value string) bool {
	format := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	switch format {
	case "epub", "pdf", "mobi", "azw", "azw3", "cbz", "cbr", "fb2", "fbz", "md":
		return true
	default:
		return false
	}
}

func ebookMimeType(path, container string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if strings.HasSuffix(strings.ToLower(path), ".fb2.zip") {
		ext = ".fbz"
	} else if ext == "" && container != "" {
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

type PGEbookReaderConfigStore struct {
	pool *pgxpool.Pool
}

func NewPGEbookReaderConfigStore(pool *pgxpool.Pool) *PGEbookReaderConfigStore {
	return &PGEbookReaderConfigStore{pool: pool}
}

func (s *PGEbookReaderConfigStore) Get(ctx context.Context, userID int, profileID string, contentID string) (*EbookReaderConfig, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook reader config store is not configured")
	}
	var config EbookReaderConfig
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, profile_id, content_id, config, updated_at
		FROM ebook_reader_config
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`,
		userID, profileID, contentID,
	).Scan(
		&config.UserID,
		&config.ProfileID,
		&config.ContentID,
		&config.Config,
		&config.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ebook reader config: %w", err)
	}
	return &config, nil
}

func (s *PGEbookReaderConfigStore) Upsert(ctx context.Context, config EbookReaderConfig) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("ebook reader config store is not configured")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ebook_reader_config
			(user_id, profile_id, content_id, config, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5)
		ON CONFLICT (user_id, profile_id, content_id) DO UPDATE SET
			config = EXCLUDED.config,
			updated_at = EXCLUDED.updated_at`,
		config.UserID,
		config.ProfileID,
		config.ContentID,
		config.Config,
		config.UpdatedAt,
	); err != nil {
		return fmt.Errorf("upsert ebook reader config: %w", err)
	}
	return nil
}

type PGEbookReaderAnnotationStore struct {
	pool *pgxpool.Pool
}

func NewPGEbookReaderAnnotationStore(pool *pgxpool.Pool) *PGEbookReaderAnnotationStore {
	return &PGEbookReaderAnnotationStore{pool: pool}
}

func (s *PGEbookReaderAnnotationStore) List(ctx context.Context, userID int, profileID string, contentID string) ([]EbookReaderAnnotation, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook reader annotation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, content_id, kind,
		       COALESCE(cfi_range, ''), COALESCE(location, ''),
		       selected_text, note, style, color, metadata, created_at, updated_at
		FROM ebook_reader_annotations
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3
		ORDER BY updated_at DESC`,
		userID, profileID, contentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list ebook reader annotations: %w", err)
	}
	defer rows.Close()
	var items []EbookReaderAnnotation
	for rows.Next() {
		annotation, err := scanEbookReaderAnnotation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, annotation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ebook reader annotations: %w", err)
	}
	return items, nil
}

func (s *PGEbookReaderAnnotationStore) Create(ctx context.Context, annotation EbookReaderAnnotation) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("ebook reader annotation store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ebook_reader_annotations
			(id, user_id, profile_id, content_id, kind, cfi_range, location,
			 selected_text, note, style, color, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''),
		        $8, $9, $10, $11, $12::jsonb, $13, $14)`,
		annotation.ID,
		annotation.UserID,
		annotation.ProfileID,
		annotation.ContentID,
		annotation.Kind,
		annotation.CFIRange,
		annotation.Location,
		annotation.SelectedText,
		annotation.Note,
		annotation.Style,
		annotation.Color,
		annotation.Metadata,
		annotation.CreatedAt,
		annotation.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create ebook reader annotation: %w", err)
	}
	return nil
}

func (s *PGEbookReaderAnnotationStore) Update(ctx context.Context, annotation EbookReaderAnnotation) (*EbookReaderAnnotation, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook reader annotation store is not configured")
	}
	updated, err := scanEbookReaderAnnotation(s.pool.QueryRow(ctx, `
		UPDATE ebook_reader_annotations
		SET kind = COALESCE(NULLIF($5, ''), kind),
		    cfi_range = COALESCE(NULLIF($6, ''), cfi_range),
		    location = COALESCE(NULLIF($7, ''), location),
		    selected_text = COALESCE(NULLIF($8, ''), selected_text),
		    note = COALESCE(NULLIF($9, ''), note),
		    style = COALESCE(NULLIF($10, ''), style),
		    color = COALESCE(NULLIF($11, ''), color),
		    metadata = $12::jsonb,
		    updated_at = $13
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND content_id = $4
		RETURNING id, user_id, profile_id, content_id, kind,
		          COALESCE(cfi_range, ''), COALESCE(location, ''),
		          selected_text, note, style, color, metadata, created_at, updated_at`,
		annotation.ID,
		annotation.UserID,
		annotation.ProfileID,
		annotation.ContentID,
		annotation.Kind,
		annotation.CFIRange,
		annotation.Location,
		annotation.SelectedText,
		annotation.Note,
		annotation.Style,
		annotation.Color,
		annotation.Metadata,
		annotation.UpdatedAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update ebook reader annotation: %w", err)
	}
	return &updated, nil
}

func (s *PGEbookReaderAnnotationStore) Delete(ctx context.Context, userID int, profileID string, contentID string, annotationID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("ebook reader annotation store is not configured")
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM ebook_reader_annotations
		WHERE id = $1 AND user_id = $2 AND profile_id = $3 AND content_id = $4`,
		annotationID, userID, profileID, contentID,
	); err != nil {
		return fmt.Errorf("delete ebook reader annotation: %w", err)
	}
	return nil
}

func scanEbookReaderAnnotation(scanner interface{ Scan(dest ...any) error }) (EbookReaderAnnotation, error) {
	var annotation EbookReaderAnnotation
	if err := scanner.Scan(
		&annotation.ID,
		&annotation.UserID,
		&annotation.ProfileID,
		&annotation.ContentID,
		&annotation.Kind,
		&annotation.CFIRange,
		&annotation.Location,
		&annotation.SelectedText,
		&annotation.Note,
		&annotation.Style,
		&annotation.Color,
		&annotation.Metadata,
		&annotation.CreatedAt,
		&annotation.UpdatedAt,
	); err != nil {
		return EbookReaderAnnotation{}, fmt.Errorf("scan ebook reader annotation: %w", err)
	}
	return annotation, nil
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

func (s *PGEbookReaderProgressStore) ListByContentIDs(ctx context.Context, userID int, profileID string, contentIDs []string) (map[string]EbookReaderProgress, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook reader progress store is not configured")
	}
	if userID <= 0 || profileID == "" || len(contentIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, profile_id, content_id, file_id, location, progress, updated_at
		FROM ebook_reader_progress
		WHERE user_id = $1 AND profile_id = $2 AND content_id = ANY($3::text[])`,
		userID, profileID, contentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list ebook reader progress: %w", err)
	}
	defer rows.Close()

	progressByContentID := make(map[string]EbookReaderProgress, len(contentIDs))
	for rows.Next() {
		var progress EbookReaderProgress
		if err := rows.Scan(
			&progress.UserID,
			&progress.ProfileID,
			&progress.ContentID,
			&progress.FileID,
			&progress.Location,
			&progress.Progress,
			&progress.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan ebook reader progress: %w", err)
		}
		progressByContentID[progress.ContentID] = progress
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ebook reader progress: %w", err)
	}
	return progressByContentID, nil
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

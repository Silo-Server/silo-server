package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
)

// ReaderFont is a persisted custom reader font uploaded by a profile.
type ReaderFont struct {
	ID        int64     `json:"id"`
	UserID    int       `json:"-"`
	ProfileID string    `json:"-"`
	Name      string    `json:"name"`
	Filename  string    `json:"filename"`
	Format    string    `json:"format"`
	SizeBytes int64     `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// ReaderFontStore persists reader font metadata scoped to (user, profile).
type ReaderFontStore interface {
	List(ctx context.Context, userID int, profileID string) ([]ReaderFont, error)
	Count(ctx context.Context, userID int, profileID string) (int, error)
	Insert(ctx context.Context, font ReaderFont) (ReaderFont, error)
	Get(ctx context.Context, userID int, profileID string, id int64) (*ReaderFont, error)
	Delete(ctx context.Context, userID int, profileID string, id int64) (bool, error)
}

const (
	// readerFontMaxBytes caps an individual font upload's file size.
	readerFontMaxBytes = 5 << 20
	// readerFontMaxPerProfile caps the number of custom fonts a single
	// profile may store.
	readerFontMaxPerProfile = 10
)

// ReaderFontsHandler serves per-profile custom reader font uploads: list,
// upload, delete, and blob serving. Metadata lives in Store; font blobs are
// written to disk under Dir, namespaced by user ID and profile ID.
type ReaderFontsHandler struct {
	Store ReaderFontStore
	Dir   string
}

type readerFontsListResponse struct {
	Fonts []ReaderFont `json:"fonts"`
}

// readerFontFormat sniffs a font's format from its magic bytes. Font
// name-table parsing is intentionally not performed here — the display name
// falls back to the uploaded filename (see HandleUpload).
func readerFontFormat(data []byte) (format, contentType string, ok bool) {
	switch {
	case len(data) >= 4 && string(data[:4]) == "wOF2":
		return "woff2", "font/woff2", true
	case len(data) >= 4 && string(data[:4]) == "wOFF":
		return "woff", "font/woff", true
	case len(data) >= 4 && string(data[:4]) == "OTTO":
		return "otf", "font/otf", true
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x01, 0x00, 0x00}):
		return "ttf", "font/ttf", true
	}
	return "", "", false
}

// readerFontBlobPath returns the on-disk path for a stored font blob. The
// filename is <id>.<format> — content-addressed by primary key, not by the
// user-supplied filename, so it never needs sanitizing on its own.
func readerFontBlobPath(dir string, userID int, profileID string, id int64, format string) string {
	return filepath.Join(dir, strconv.Itoa(userID), profileID, fmt.Sprintf("%d.%s", id, format))
}

// safeReaderFontPathComponent reports whether s is safe to use as a single
// path component (user ID, profile ID) when building on-disk font paths.
// profile_id in particular is populated straight from the client-supplied
// X-Profile-Id header — RequireProfile only checks that it's non-empty — so
// every handler MUST validate it with this function before it reaches any
// filepath.Join, filepath.Glob, or filesystem call. An empty string, ".",
// "..", any substring of "..", or any character outside [A-Za-z0-9._-] is
// rejected.
func safeReaderFontPathComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// sanitizeReaderFontFilename strips any directory components from a
// user-supplied upload filename, keeping only the base name. It removes
// NUL and control characters (0x00-0x1F, 0x7F), trims whitespace, and caps
// at 255 runes. This is a display-only field; the actual blob is stored
// content-addressed as <id>.<format> (see readerFontBlobPath). Rejects to
// an empty string only if the result is empty, ".", or ".." so callers
// apply their default naming.
func sanitizeReaderFontFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}

	// Remove NUL and control characters (0x00-0x1F, 0x7F) to prevent
	// filesystem or display issues.
	var filtered strings.Builder
	for _, r := range name {
		if (r >= 0x00 && r <= 0x1F) || r == 0x7F {
			continue
		}
		filtered.WriteRune(r)
	}
	name = filtered.String()
	name = strings.TrimSpace(name)

	// Cap at 255 runes (typical filesystem limit).
	if len([]rune(name)) > 255 {
		runes := []rune(name)
		name = string(runes[:255])
	}

	// Reject if the result is empty, ".", or ".."
	if name == "" || name == "." || name == ".." {
		return ""
	}

	return name
}

// HandleList returns the custom reader fonts uploaded by the active profile.
func (h *ReaderFontsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reader fonts are not configured")
		return
	}
	if !safeReaderFontPathComponent(profileID) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid profile")
		return
	}

	fonts, err := h.Store.List(r.Context(), userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list reader fonts")
		return
	}
	if fonts == nil {
		fonts = []ReaderFont{}
	}
	writeJSON(w, http.StatusOK, readerFontsListResponse{Fonts: fonts})
}

// HandleUpload accepts a multipart font upload (field "font"), validates its
// size and format, enforces the per-profile font cap, and persists both the
// metadata row and the blob. The row is inserted first so its ID can be used
// to build the blob path; if writing the blob fails, the row is removed so
// metadata and disk state never diverge.
func (h *ReaderFontsHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil || h.Dir == "" {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reader fonts are not configured")
		return
	}
	if !safeReaderFontPathComponent(profileID) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid profile")
		return
	}

	if err := r.ParseMultipartForm(readerFontMaxBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}
	file, header, err := r.FormFile("font")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing font field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, readerFontMaxBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}
	if int64(len(data)) > readerFontMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Font file exceeds the maximum size")
		return
	}

	format, _, ok := readerFontFormat(data)
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_type", "Unsupported font file type")
		return
	}

	count, err := h.Store.Count(r.Context(), userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check reader font limit")
		return
	}
	if count >= readerFontMaxPerProfile {
		writeError(w, http.StatusConflict, "limit_reached", "Reader font limit reached")
		return
	}

	filename := sanitizeReaderFontFilename(header.Filename)
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if name == "" {
		name = "Untitled"
	}
	if filename == "" {
		filename = name
	}

	inserted, err := h.Store.Insert(r.Context(), ReaderFont{
		UserID:    userID,
		ProfileID: profileID,
		Name:      name,
		Filename:  filename,
		Format:    format,
		SizeBytes: int64(len(data)),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save reader font")
		return
	}

	path := readerFontBlobPath(h.Dir, userID, profileID, inserted.ID, format)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_, _ = h.Store.Delete(r.Context(), userID, profileID, inserted.ID)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store reader font")
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		_, _ = h.Store.Delete(r.Context(), userID, profileID, inserted.ID)
		_ = os.Remove(path)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store reader font")
		return
	}

	writeJSON(w, http.StatusCreated, inserted)
}

// HandleDelete removes a reader font's metadata row and best-effort deletes
// its blob from disk. Scoped to the active (user, profile); a font owned by
// another profile is reported as not found.
func (h *ReaderFontsHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reader fonts are not configured")
		return
	}
	if !safeReaderFontPathComponent(profileID) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid profile")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "font_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "font_id is required")
		return
	}

	deleted, err := h.Store.Delete(r.Context(), userID, profileID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete reader font")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "Reader font not found")
		return
	}

	if h.Dir != "" {
		pattern := filepath.Join(h.Dir, strconv.Itoa(userID), profileID, fmt.Sprintf("%d.*", id))
		if matches, globErr := filepath.Glob(pattern); globErr == nil {
			for _, match := range matches {
				_ = os.Remove(match)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// readerFontContentType maps a stored format to its serving Content-Type.
func readerFontContentType(format string) string {
	switch format {
	case "woff2":
		return "font/woff2"
	case "woff":
		return "font/woff"
	case "otf":
		return "font/otf"
	case "ttf":
		return "font/ttf"
	default:
		return "application/octet-stream"
	}
}

// HandleServeFile streams a reader font's blob. Scoped to the active (user,
// profile); a font owned by another profile is reported as not found. Blobs
// are content-addressed and immutable once uploaded, so they are served with
// a long-lived private cache lifetime.
func (h *ReaderFontsHandler) HandleServeFile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reader fonts are not configured")
		return
	}
	if !safeReaderFontPathComponent(profileID) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid profile")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "font_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "font_id is required")
		return
	}

	font, err := h.Store.Get(r.Context(), userID, profileID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load reader font")
		return
	}
	if font == nil {
		writeError(w, http.StatusNotFound, "not_found", "Reader font not found")
		return
	}

	path := readerFontBlobPath(h.Dir, userID, profileID, id, font.Format)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "not_found", "Reader font file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to open reader font")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to stat reader font")
		return
	}

	w.Header().Set("Content-Type", readerFontContentType(font.Format))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}

// PGReaderFontStore is the Postgres-backed ReaderFontStore.
type PGReaderFontStore struct {
	pool *pgxpool.Pool
}

// NewPGReaderFontStore constructs a PGReaderFontStore.
func NewPGReaderFontStore(pool *pgxpool.Pool) *PGReaderFontStore {
	return &PGReaderFontStore{pool: pool}
}

func (s *PGReaderFontStore) List(ctx context.Context, userID int, profileID string) ([]ReaderFont, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reader font store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, filename, format, size_bytes, created_at
		FROM reader_fonts
		WHERE user_id = $1 AND profile_id = $2
		ORDER BY created_at, id`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("list reader fonts: %w", err)
	}
	defer rows.Close()

	var fonts []ReaderFont
	for rows.Next() {
		var font ReaderFont
		if err := rows.Scan(
			&font.ID,
			&font.UserID,
			&font.ProfileID,
			&font.Name,
			&font.Filename,
			&font.Format,
			&font.SizeBytes,
			&font.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan reader font: %w", err)
		}
		fonts = append(fonts, font)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reader fonts: %w", err)
	}
	return fonts, nil
}

func (s *PGReaderFontStore) Count(ctx context.Context, userID int, profileID string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("reader font store is not configured")
	}
	var count int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM reader_fonts WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count reader fonts: %w", err)
	}
	return count, nil
}

func (s *PGReaderFontStore) Insert(ctx context.Context, font ReaderFont) (ReaderFont, error) {
	if s == nil || s.pool == nil {
		return ReaderFont{}, fmt.Errorf("reader font store is not configured")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO reader_fonts (user_id, profile_id, name, filename, format, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`,
		font.UserID, font.ProfileID, font.Name, font.Filename, font.Format, font.SizeBytes,
	).Scan(&font.ID, &font.CreatedAt)
	if err != nil {
		return ReaderFont{}, fmt.Errorf("insert reader font: %w", err)
	}
	return font, nil
}

func (s *PGReaderFontStore) Get(ctx context.Context, userID int, profileID string, id int64) (*ReaderFont, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reader font store is not configured")
	}
	var font ReaderFont
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, filename, format, size_bytes, created_at
		FROM reader_fonts
		WHERE user_id = $1 AND profile_id = $2 AND id = $3`,
		userID, profileID, id,
	).Scan(
		&font.ID,
		&font.UserID,
		&font.ProfileID,
		&font.Name,
		&font.Filename,
		&font.Format,
		&font.SizeBytes,
		&font.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reader font: %w", err)
	}
	return &font, nil
}

func (s *PGReaderFontStore) Delete(ctx context.Context, userID int, profileID string, id int64) (bool, error) {
	if s == nil || s.pool == nil {
		return false, fmt.Errorf("reader font store is not configured")
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM reader_fonts WHERE user_id = $1 AND profile_id = $2 AND id = $3`,
		userID, profileID, id,
	)
	if err != nil {
		return false, fmt.Errorf("delete reader font: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

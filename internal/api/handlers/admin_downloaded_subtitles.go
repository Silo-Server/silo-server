package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// AdminDownloadedSubtitle is the admin-facing view of a stored subtitle record.
type AdminDownloadedSubtitle struct {
	ID               int       `json:"id"`
	MediaFileID      int       `json:"media_file_id"`
	MediaContentID   string    `json:"media_content_id,omitempty"`
	Provider         string    `json:"provider"`
	Language         string    `json:"language"`
	Format           string    `json:"format"`
	ReleaseName      string    `json:"release_name"`
	Score            float64   `json:"score"`
	HearingImpaired  bool      `json:"hearing_impaired"`
	CreatedAt        time.Time `json:"created_at"`
	DownloadedBy     *int      `json:"downloaded_by,omitempty"`
	UploaderUsername string    `json:"uploader_username"`
	MediaTitle       string    `json:"media_title"`
	MediaType        string    `json:"media_type"`
	FilePath         string    `json:"file_path"`
}

type adminDownloadedSubtitlesResponse struct {
	Subtitles []AdminDownloadedSubtitle `json:"subtitles"`
	Total     int                       `json:"total"`
	Uploads   int                       `json:"uploads"`
	Provider  int                       `json:"provider_downloads"`
}

type patchDownloadedSubtitleRequest struct {
	Language        *string `json:"language"`
	ReleaseName     *string `json:"release_name"`
	HearingImpaired *bool   `json:"hearing_impaired"`
}

// SetDownloadedSubtitleDeps wires optional dependencies for downloaded subtitle admin routes.
func (h *AdminSubtitleHandler) SetDownloadedSubtitleDeps(pool *pgxpool.Pool, manager *subtitles.Manager) {
	h.pool = pool
	h.manager = manager
}

// HandleListDownloadedSubtitles handles GET /api/v1/admin/subtitles.
func (h *AdminSubtitleHandler) HandleListDownloadedSubtitles(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}

	limit, offset := parsePagination(r)
	q := r.URL.Query()

	var (
		args       []any
		conditions []string
		argIndex   = 1
	)

	if provider := strings.TrimSpace(q.Get("provider")); provider != "" {
		conditions = append(conditions, "ds.provider = $"+strconv.Itoa(argIndex))
		args = append(args, provider)
		argIndex++
	}

	if language := strings.TrimSpace(q.Get("language")); language != "" {
		conditions = append(conditions, "ds.language = $"+strconv.Itoa(argIndex))
		args = append(args, language)
		argIndex++
	}

	if userIDStr := strings.TrimSpace(q.Get("user_id")); userIDStr != "" {
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid user_id")
			return
		}
		conditions = append(conditions, "ds.downloaded_by = $"+strconv.Itoa(argIndex))
		args = append(args, userID)
		argIndex++
	}

	if mediaFileIDStr := strings.TrimSpace(q.Get("media_file_id")); mediaFileIDStr != "" {
		mediaFileID, err := strconv.Atoi(mediaFileIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid media_file_id")
			return
		}
		conditions = append(conditions, "ds.media_file_id = $"+strconv.Itoa(argIndex))
		args = append(args, mediaFileID)
		argIndex++
	}

	if search := strings.TrimSpace(q.Get("q")); search != "" {
		conditions = append(conditions, "ds.release_name ILIKE $"+strconv.Itoa(argIndex))
		args = append(args, "%"+search+"%")
		argIndex++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := `SELECT COUNT(*) FROM downloaded_subtitles ds` + whereClause
	var total int
	if err := h.pool.QueryRow(r.Context(), countQuery, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count subtitles")
		return
	}

	statsQuery := `
		SELECT
			COUNT(*) FILTER (WHERE ds.provider = 'upload'),
			COUNT(*) FILTER (WHERE ds.provider <> 'upload')
		FROM downloaded_subtitles ds` + whereClause
	var uploads, providerDownloads int
	if err := h.pool.QueryRow(r.Context(), statsQuery, args...).Scan(&uploads, &providerDownloads); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count subtitle stats")
		return
	}

	listQuery := `
		SELECT
			ds.id,
			ds.media_file_id,
			COALESCE(mf.content_id, ''),
			ds.provider,
			ds.language,
			ds.format,
			ds.release_name,
			ds.score,
			ds.hearing_impaired,
			ds.created_at,
			ds.downloaded_by,
			COALESCE(u.username, ''),
			COALESCE(ep.title, mi.title, ''),
			COALESCE(CASE WHEN ep.content_id IS NOT NULL THEN 'episode' ELSE mi.type END, ''),
			COALESCE(mf.file_path, '')
		FROM downloaded_subtitles ds
		LEFT JOIN users u ON u.id = ds.downloaded_by
		LEFT JOIN media_files mf ON mf.id = ds.media_file_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		LEFT JOIN episodes ep ON ep.content_id = mf.content_id` + whereClause + `
		ORDER BY ds.created_at DESC
		LIMIT $` + strconv.Itoa(argIndex) + ` OFFSET $` + strconv.Itoa(argIndex+1)

	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.pool.Query(r.Context(), listQuery, listArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list subtitles")
		return
	}
	defer rows.Close()

	subtitlesList := make([]AdminDownloadedSubtitle, 0)
	for rows.Next() {
		var row AdminDownloadedSubtitle
		if err := rows.Scan(
			&row.ID,
			&row.MediaFileID,
			&row.MediaContentID,
			&row.Provider,
			&row.Language,
			&row.Format,
			&row.ReleaseName,
			&row.Score,
			&row.HearingImpaired,
			&row.CreatedAt,
			&row.DownloadedBy,
			&row.UploaderUsername,
			&row.MediaTitle,
			&row.MediaType,
			&row.FilePath,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan subtitle row")
			return
		}
		subtitlesList = append(subtitlesList, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to iterate subtitles")
		return
	}

	writeJSON(w, http.StatusOK, adminDownloadedSubtitlesResponse{
		Subtitles: subtitlesList,
		Total:     total,
		Uploads:   uploads,
		Provider:  providerDownloads,
	})
}

// HandlePatchDownloadedSubtitle handles PATCH /api/v1/admin/subtitles/{id}.
func (h *AdminSubtitleHandler) HandlePatchDownloadedSubtitle(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Subtitle manager not configured")
		return
	}

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid subtitle ID")
		return
	}

	var req patchDownloadedSubtitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}
	if req.Language == nil && req.ReleaseName == nil && req.HearingImpaired == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "No fields to update")
		return
	}

	updated, err := h.manager.UpdateDownloadedSubtitle(r.Context(), id, subtitles.SubtitleMetadataPatch{
		Language:        req.Language,
		ReleaseName:     req.ReleaseName,
		HearingImpaired: req.HearingImpaired,
	})
	if err != nil {
		switch {
		case errors.Is(err, subtitles.ErrSubtitleNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Subtitle not found")
		case errors.Is(err, subtitles.ErrSubtitleLanguageConflict):
			writeError(w, http.StatusConflict, "conflict", "Subtitle with this language already exists for this file")
		default:
			if strings.Contains(err.Error(), "invalid subtitle language") {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "update_error", "Failed to update subtitle")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"subtitle": updated})
}

// HandleDownloadDownloadedSubtitle handles GET /api/v1/admin/subtitles/{id}/download.
func (h *AdminSubtitleHandler) HandleDownloadDownloadedSubtitle(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Subtitle manager not configured")
		return
	}

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid subtitle ID")
		return
	}

	sub, data, err := h.manager.GetSubtitleContent(r.Context(), id)
	if err != nil {
		if errors.Is(err, subtitles.ErrSubtitleNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Subtitle not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "download_error", "Failed to download subtitle")
		return
	}

	filename := subtitleDownloadFilename(sub)
	w.Header().Set("Content-Type", subtitles.SubtitleContentType(sub.Format))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// HandleDeleteDownloadedSubtitle handles DELETE /api/v1/admin/subtitles/{id}.
func (h *AdminSubtitleHandler) HandleDeleteDownloadedSubtitle(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Subtitle manager not configured")
		return
	}

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid subtitle ID")
		return
	}

	sub, err := h.repo.GetDownloadedSubtitle(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup_error", "Failed to look up subtitle")
		return
	}
	if sub == nil {
		writeError(w, http.StatusNotFound, "not_found", "Subtitle not found")
		return
	}

	if err := h.manager.DeleteSubtitle(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete_error", "Failed to delete subtitle")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func subtitleDownloadFilename(sub *subtitles.DownloadedSubtitle) string {
	base := strings.TrimSpace(sub.ReleaseName)
	if base == "" {
		base = fmt.Sprintf("subtitle-%d", sub.ID)
	}
	base = path.Base(base)
	base = strings.TrimSuffix(base, path.Ext(base))
	if base == "" || base == "." {
		base = fmt.Sprintf("subtitle-%d", sub.ID)
	}
	return fmt.Sprintf("%s.%s", base, sub.Format)
}

package handlers

import (
	"context"
	"net/http"
	"path/filepath"
)

type AdminItemFileView = itemFileResponse

func (h *AdminSplitHandler) ListAdminItemFiles(ctx context.Context, id string, limit, after int) ([]AdminItemFileView, bool, error) {
	if h == nil || h.items == nil || h.pool == nil {
		return nil, false, apiError(http.StatusServiceUnavailable, "unavailable", "Item files are not configured")
	}
	if _, err := h.items.GetByID(ctx, id); err != nil {
		return nil, false, apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	if limit < 1 || limit > 200 || after < 0 {
		return nil, false, apiError(http.StatusBadRequest, "bad_request", "Invalid file page")
	}
	rows, err := h.pool.Query(ctx, `SELECT id,media_folder_id,file_path,COALESCE(observed_root_path,''),COALESCE(season_number,0),COALESCE(episode_number,0) FROM media_files WHERE content_id=$1 AND id>$2 ORDER BY id LIMIT $3`, id, after, limit+1)
	if err != nil {
		return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to load item files")
	}
	defer rows.Close()
	out := []AdminItemFileView{}
	for rows.Next() {
		var f AdminItemFileView
		if err := rows.Scan(&f.ID, &f.LibraryID, &f.FilePath, &f.ObservedRootPath, &f.SeasonNumber, &f.EpisodeNumber); err != nil {
			return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to load item files")
		}
		if f.ObservedRootPath == "" {
			f.ObservedRootPath = filepath.Dir(f.FilePath)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to load item files")
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

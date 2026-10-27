package handlers

import (
	"time"

	"github.com/jackc/pgx/v5"
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
	// Timing is the stored timing correction; it stays out of the frozen v1 JSON.
	Timing subtitles.Timing `json:"-"`
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

// scanAdminDownloadedSubtitle scans one row selected with adminDownloadedSubtitleSelect.
func scanAdminDownloadedSubtitle(row pgx.Row) (AdminDownloadedSubtitle, error) {
	var out AdminDownloadedSubtitle
	err := row.Scan(&out.ID, &out.MediaFileID, &out.MediaContentID, &out.Provider, &out.Language,
		&out.Format, &out.ReleaseName, &out.Score, &out.HearingImpaired, &out.CreatedAt,
		&out.DownloadedBy, &out.UploaderUsername, &out.MediaTitle, &out.MediaType, &out.FilePath,
		&out.Timing.OffsetMS, &out.Timing.Scale)
	return out, err
}

const adminDownloadedSubtitleSelect = `
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
			COALESCE(mf.file_path, ''),
			ds.timing_offset_ms,
			ds.timing_scale
		FROM downloaded_subtitles ds
		LEFT JOIN users u ON u.id = ds.downloaded_by
		LEFT JOIN media_files mf ON mf.id = ds.media_file_id
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		LEFT JOIN episodes ep ON ep.content_id = mf.content_id`

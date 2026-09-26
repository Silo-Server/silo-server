package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// AdminSubtitleListFilter carries the existing stored-subtitle inspection filters.
type AdminSubtitleListFilter struct {
	Provider    string
	Language    string
	UserID      int
	MediaFileID int
	Search      string
}

type AdminSubtitlePageKey struct {
	CreatedAt time.Time `json:"created_at"`
	ID        int       `json:"id"`
}

type AdminSubtitlePage struct {
	Items             []AdminDownloadedSubtitle
	Total             int
	Uploads           int
	ProviderDownloads int
	HasMore           bool
}

// ListAdminSubtitlesPage reads a consistent page and filtered counts. Cursors
// identify a position in a live collection, not a snapshot across requests.
func (h *AdminSubtitleHandler) ListAdminSubtitlesPage(ctx context.Context, filter AdminSubtitleListFilter, after *AdminSubtitlePageKey, limit int) (AdminSubtitlePage, error) {
	var out AdminSubtitlePage
	if h.pool == nil {
		return out, fmt.Errorf("subtitle database unavailable")
	}
	if limit < 1 || limit > 200 {
		return out, fmt.Errorf("invalid subtitle page limit")
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var conditions []string
	var args []any
	add := func(sql string, value any) {
		args = append(args, value)
		conditions = append(conditions, sql+"$"+strconv.Itoa(len(args)))
	}
	if filter.Provider != "" {
		add("ds.provider = ", filter.Provider)
	}
	if filter.Language != "" {
		canonical := subtitles.NormalizeProviderLanguage("", filter.Language)
		if canonical == "" {
			canonical = strings.TrimSpace(filter.Language)
		}
		args = append(args, canonical, subtitles.LanguageAliases(canonical))
		conditions = append(conditions, fmt.Sprintf("(ds.language = $%d OR lower(btrim(ds.language)) = ANY($%d::text[]))", len(args)-1, len(args)))
	}
	if filter.UserID != 0 {
		add("ds.downloaded_by = ", filter.UserID)
	}
	if filter.MediaFileID != 0 {
		add("ds.media_file_id = ", filter.MediaFileID)
	}
	if filter.Search != "" {
		add("ds.release_name ILIKE ", "%"+filter.Search+"%")
	}
	where := func() string {
		if len(conditions) == 0 {
			return ""
		}
		return " WHERE " + strings.Join(conditions, " AND ")
	}
	err = tx.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE ds.provider = 'upload'), COUNT(*) FILTER (WHERE ds.provider <> 'upload') FROM downloaded_subtitles ds`+where(), args...).Scan(&out.Total, &out.Uploads, &out.ProviderDownloads)
	if err != nil {
		return out, err
	}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		conditions = append(conditions, fmt.Sprintf("(ds.created_at, ds.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, adminDownloadedSubtitleSelect+where()+" ORDER BY ds.created_at DESC, ds.id DESC LIMIT $"+strconv.Itoa(len(args)), args...)
	if err != nil {
		return out, err
	}
	out.Items = make([]AdminDownloadedSubtitle, 0)
	for rows.Next() {
		var row AdminDownloadedSubtitle
		err = rows.Scan(&row.ID, &row.MediaFileID, &row.MediaContentID, &row.Provider, &row.Language, &row.Format, &row.ReleaseName, &row.Score, &row.HearingImpaired, &row.CreatedAt, &row.DownloadedBy, &row.UploaderUsername, &row.MediaTitle, &row.MediaType, &row.FilePath)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.HasMore = true
		out.Items = out.Items[:limit]
	}
	if err = tx.Commit(ctx); err != nil {
		return AdminSubtitlePage{}, err
	}
	return out, nil
}

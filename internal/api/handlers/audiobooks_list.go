package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AudiobookHandler serves silo-native audiobook endpoints under
// /api/v1/audiobooks/*. Sub-plan 3 implements list/detail/progress;
// sub-plan 4's ABS-compat layer is separate.
type AudiobookHandler struct {
	Items         *catalog.ItemRepository
	Files         *scanner.FileRepository
	StoreProvider userstore.UserStoreProvider
	Detail        *catalog.DetailService
}

// HandleListAudiobooks serves GET /api/v1/audiobooks?limit=&offset=.
// Paginated catalog of media_items with type='audiobook'.
func (h *AudiobookHandler) HandleListAudiobooks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	pool := h.Files.Pool()
	ctx := r.Context()
	var total int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_items WHERE type='audiobook'`).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list audiobooks count failed")
		return
	}
	rows, err := pool.Query(ctx, `
		SELECT
			mi.content_id,
			mi.title,
			COALESCE(mi.year, 0),
			COALESCE(mi.poster_path, ''),
			COALESCE(SUM(mf.duration), 0)::int           AS duration_seconds,
			COALESCE(MIN(mf.container), '')              AS container,
			COALESCE(MIN(mf.codec_audio), '')            AS codec_audio,
			COUNT(mf.id)                                 AS file_count
		FROM media_items mi
		LEFT JOIN media_files mf ON mf.content_id = mi.content_id
		WHERE mi.type='audiobook'
		GROUP BY mi.content_id, mi.title, mi.year, mi.poster_path, mi.sort_title
		ORDER BY LOWER(mi.sort_title), LOWER(mi.title)
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list audiobooks failed")
		return
	}
	defer rows.Close()
	items := make([]audiobookListItem, 0, limit)
	for rows.Next() {
		var it audiobookListItem
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &it.PosterURL, &it.DurationSeconds, &it.Container, &it.CodecAudio, &it.FileCount); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "scan audiobook row failed")
			return
		}
		if h.Detail != nil && it.PosterURL != "" {
			it.PosterURL = h.Detail.PresignURL(ctx, it.PosterURL, "card")
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "iterate audiobook rows failed")
		return
	}
	resp := struct {
		Items  []audiobookListItem `json:"items"`
		Total  int                 `json:"total"`
		Limit  int                 `json:"limit"`
		Offset int                 `json:"offset"`
	}{Items: items, Total: total, Limit: limit, Offset: offset}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type audiobookListItem struct {
	ContentID       string `json:"content_id"`
	Title           string `json:"title"`
	Year            int    `json:"year"`
	PosterURL       string `json:"poster_url,omitempty"`
	DurationSeconds int    `json:"duration_seconds"`
	Container       string `json:"container,omitempty"`
	CodecAudio      string `json:"codec_audio,omitempty"`
	FileCount       int    `json:"file_count"`
}

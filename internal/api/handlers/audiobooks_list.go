package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
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
	// Recs is optional. When set, the detail handler uses embedding-based
	// nearest-neighbor search for the "You might also like" rail and falls
	// back to shared-genre matching only when no embedding exists for the
	// source book yet.
	Recs *recommendations.Repo
}

// HandleListAudiobooks serves GET /api/v1/audiobooks?limit=&offset=.
// Paginated catalog of media_items with type='audiobook' scoped to the
// caller's accessible libraries.
func (h *AudiobookHandler) HandleListAudiobooks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	ctx := r.Context()
	access := requestAccessFilter(r)
	genre := r.URL.Query().Get("genre")
	conditions, args, argIdx, empty := audiobookListConditions(access, genre)
	if empty {
		writeAudiobookListResponse(w, nil, 0, limit, offset)
		return
	}

	pool := h.Files.Pool()
	var total int
	countSQL := "SELECT COUNT(*) FROM media_items mi WHERE " + strings.Join(conditions, " AND ")
	if err := pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list audiobooks count failed")
		return
	}

	dataArgs := append([]any(nil), args...)
	limitIdx := argIdx
	dataArgs = append(dataArgs, limit)
	offsetIdx := argIdx + 1
	dataArgs = append(dataArgs, offset)

	rows, err := pool.Query(ctx, fmt.Sprintf(`
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
		WHERE %s
		GROUP BY mi.content_id, mi.title, mi.year, mi.poster_path, mi.sort_title
		ORDER BY LOWER(mi.sort_title), LOWER(mi.title)
		LIMIT $%d OFFSET $%d
	`, strings.Join(conditions, " AND "), limitIdx, offsetIdx), dataArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list audiobooks failed")
		return
	}
	defer rows.Close()

	items := make([]audiobookListItem, 0, limit)
	for rows.Next() {
		var it audiobookListItem
		var posterPath string
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &posterPath, &it.DurationSeconds, &it.Container, &it.CodecAudio, &it.FileCount); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "scan audiobook row failed")
			return
		}
		it.PosterURL = h.presignAudiobookPoster(ctx, posterPath)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "iterate audiobook rows failed")
		return
	}
	writeAudiobookListResponse(w, items, total, limit, offset)
}

func audiobookListConditions(access catalog.AccessFilter, genre string) ([]string, []any, int, bool) {
	conditions := []string{"mi.type = 'audiobook'"}
	var args []any
	argIdx := 1

	if genre != "" {
		conditions = append(conditions, fmt.Sprintf("$%d = ANY(mi.genres)", argIdx))
		args = append(args, genre)
		argIdx++
	}
	if access.AllowedLibraryIDs != nil {
		if len(access.AllowedLibraryIDs) == 0 {
			return nil, nil, 1, true
		}
		conditions = append(conditions, fmt.Sprintf(`
			EXISTS (
				SELECT 1 FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id = ANY($%d)
			)`, argIdx))
		args = append(args, access.AllowedLibraryIDs)
		argIdx++
	}
	if len(access.DisabledLibraryIDs) > 0 {
		if access.AllowedLibraryIDs == nil {
			conditions = append(conditions, `
				EXISTS (
					SELECT 1 FROM media_item_libraries mil
					WHERE mil.content_id = mi.content_id
				)`)
		}
		conditions = append(conditions, fmt.Sprintf(`
			NOT EXISTS (
				SELECT 1 FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				  AND mil.media_folder_id = ANY($%d)
			)`, argIdx))
		args = append(args, access.DisabledLibraryIDs)
		argIdx++
	}
	catalog.ApplySectionAccessFilter("mi", catalog.AccessFilter{MaxContentRating: access.MaxContentRating}, &conditions, &args, &argIdx)
	return conditions, args, argIdx, false
}

func writeAudiobookListResponse(w http.ResponseWriter, items []audiobookListItem, total, limit, offset int) {
	if items == nil {
		items = []audiobookListItem{}
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

func (h *AudiobookHandler) presignAudiobookPoster(ctx context.Context, path string) string {
	if h == nil || h.Detail == nil || path == "" {
		return ""
	}
	return h.Detail.PresignURL(ctx, cardThumbnailPath(path), "card")
}

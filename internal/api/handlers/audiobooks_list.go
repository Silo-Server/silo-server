package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AudiobookHandler serves silo-native audiobook endpoints under
// /api/v1/audiobooks/*. Sub-plan 3 implements list/detail/progress;
// sub-plan 4's ABS-compat layer is separate.
type AudiobookHandler struct {
	Items         *catalog.ItemRepository
	Files         *scanner.FileRepository
	Detail        *catalog.DetailService
	StoreProvider userstore.UserStoreProvider
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
	filter := requestAccessFilter(r)
	items, total, err := h.Items.Search(r.Context(), "", []string{"audiobook"}, limit, offset, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list audiobooks failed")
		return
	}
	resp := struct {
		Items  []audiobookListItem `json:"items"`
		Total  int                 `json:"total"`
		Limit  int                 `json:"limit"`
		Offset int                 `json:"offset"`
	}{
		Items:  h.audiobookListItems(r.Context(), items),
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type audiobookListItem struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	PosterURL string `json:"poster_url,omitempty"`
}

func (h *AudiobookHandler) audiobookListItems(ctx context.Context, items []*models.MediaItem) []audiobookListItem {
	out := make([]audiobookListItem, 0, len(items))
	for _, it := range items {
		out = append(out, audiobookListItem{
			ContentID: it.ContentID,
			Title:     it.Title,
			Year:      it.Year,
			PosterURL: h.presignAudiobookPoster(ctx, it.PosterPath),
		})
	}
	return out
}

func (h *AudiobookHandler) presignAudiobookPoster(ctx context.Context, path string) string {
	if h == nil || h.Detail == nil || path == "" {
		return ""
	}
	return h.Detail.PresignURL(ctx, cardThumbnailPath(path), "card")
}

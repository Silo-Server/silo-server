package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AudiobookHandler serves silo-native audiobook endpoints under
// /api/v1/audiobooks/*. Sub-plan 3 implements list/detail/progress;
// sub-plan 4's ABS-compat layer is separate.
type AudiobookHandler struct {
	Items *catalog.ItemRepository
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
		Items:  audiobookListItems(items),
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

func audiobookListItems(items []*models.MediaItem) []audiobookListItem {
	out := make([]audiobookListItem, 0, len(items))
	for _, it := range items {
		out = append(out, audiobookListItem{
			ContentID: it.ContentID,
			Title:     it.Title,
			Year:      it.Year,
			PosterURL: it.PosterPath,
		})
	}
	return out
}

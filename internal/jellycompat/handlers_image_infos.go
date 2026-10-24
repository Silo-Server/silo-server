package jellycompat

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
)

// imageInfoDTO is one entry of Jellyfin's GET /Items/{id}/Images response.
// Silo does not record artwork dimensions or byte sizes, so Width and Height
// are left out, as Jellyfin does for an unknown size.
type imageInfoDTO struct {
	ImageType  string `json:"ImageType"`
	ImageIndex *int   `json:"ImageIndex,omitempty"`
	ImageTag   string `json:"ImageTag"`
	BlurHash   string `json:"BlurHash,omitempty"`
	Size       int64  `json:"Size"`
}

// HandleItemImageInfos lists an item's artwork. Roku clients read it to pick
// the poster on a details screen, for movies, series, episodes, collections
// and people alike. The entries come from the DTO /Items/{id} serves for the
// same id, so the types and tags always match the item's ImageTags.
func (h *ItemsHandler) HandleItemImageInfos(w http.ResponseWriter, r *http.Request) {
	item := &capturedResponse{header: http.Header{}, status: http.StatusOK}
	h.HandleItem(item, r)
	if item.status != http.StatusOK {
		maps.Copy(w.Header(), item.header)
		w.WriteHeader(item.status)
		_, _ = w.Write(item.body.Bytes())
		return
	}
	var dto struct {
		ImageTags         map[string]string            `json:"ImageTags"`
		BackdropImageTags []string                     `json:"BackdropImageTags"`
		ImageBlurHashes   map[string]map[string]string `json:"ImageBlurHashes"`
	}
	if err := json.Unmarshal(item.body.Bytes(), &dto); err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", "Failed to read item images")
		return
	}
	writeJSON(w, http.StatusOK, imageInfosFromDTO(baseItemDTO{
		ImageTags: dto.ImageTags, BackdropImageTags: dto.BackdropImageTags, ImageBlurHashes: dto.ImageBlurHashes,
	}))
}

// capturedResponse buffers a handler's response so another handler can
// reuse it.
type capturedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capturedResponse) Header() http.Header         { return c.header }
func (c *capturedResponse) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *capturedResponse) WriteHeader(status int)      { c.status = status }

func imageInfosFromDTO(dto baseItemDTO) []imageInfoDTO {
	infos := make([]imageInfoDTO, 0, len(dto.ImageTags)+len(dto.BackdropImageTags))
	for _, imageType := range slices.Sorted(maps.Keys(dto.ImageTags)) {
		tag := dto.ImageTags[imageType]
		infos = append(infos, imageInfoDTO{
			ImageType: imageType,
			ImageTag:  tag,
			BlurHash:  dto.ImageBlurHashes[imageType][tag],
		})
	}
	for i, tag := range dto.BackdropImageTags {
		infos = append(infos, imageInfoDTO{
			ImageType:  compatBackdropImageType,
			ImageIndex: &i,
			ImageTag:   tag,
			BlurHash:   dto.ImageBlurHashes[compatBackdropImageType][tag],
		})
	}
	return infos
}

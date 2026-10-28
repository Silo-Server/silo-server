package handlers

import (
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// SearchHandler handles the search endpoint.
type SearchHandler struct {
	itemRepo *catalog.ItemRepository
	itemsH   *ItemsHandler // reuse toItemListResponse
}

// NewSearchHandler creates a new SearchHandler.
func NewSearchHandler(
	itemRepo *catalog.ItemRepository,
	itemsH *ItemsHandler,
) *SearchHandler {
	return &SearchHandler{
		itemRepo: itemRepo,
		itemsH:   itemsH,
	}
}

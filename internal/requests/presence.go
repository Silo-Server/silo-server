package requests

import (
	"context"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type PresenceResolver interface {
	LookupTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]bool, error)
}

type CatalogPresence struct {
	items *catalog.ItemRepository
}

func NewCatalogPresence(items *catalog.ItemRepository) *CatalogPresence {
	return &CatalogPresence{items: items}
}

func (p *CatalogPresence) LookupTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]bool, error) {
	out := map[int]bool{}
	if p == nil || p.items == nil || len(tmdbIDs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(tmdbIDs))
	for _, id := range tmdbIDs {
		if id > 0 {
			ids = append(ids, strconv.Itoa(id))
		}
	}
	internalType := string(mediaType)
	rows, err := p.items.LookupTMDBIDs(ctx, internalType, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id, err := strconv.Atoi(row.TMDBID)
		if err == nil {
			out[id] = true
		}
	}
	return out, nil
}

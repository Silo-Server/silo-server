package catalog

import (
	"github.com/Silo-Server/silo-server/internal/models"
)

func contentIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ContentID
	}
	return ids
}

package sections

import (
	"github.com/Silo-Server/silo-server/internal/models"
)

func contentIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

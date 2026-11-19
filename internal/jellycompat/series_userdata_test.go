package jellycompat

import (
	"github.com/Silo-Server/silo-server/internal/models"
)

func ep(id string) *models.Episode {
	return &models.Episode{ContentID: id}
}

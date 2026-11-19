package intromarkers

import (
	"github.com/Silo-Server/silo-server/internal/models"
)

func chapter(title string, start, end float64) models.MediaChapter {
	return models.MediaChapter{Title: title, StartSeconds: start, EndSeconds: end}
}

package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type MarkerPopulationService interface {
	Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error)
}

const markerReadTimeout = 5 * time.Second

func populateFileMarkers(ctx context.Context, population MarkerPopulationService, file *models.MediaFile) *models.MediaFile {
	if population == nil || file == nil {
		return file
	}
	lookupCtx, cancel := context.WithTimeout(ctx, markerReadTimeout)
	defer cancel()
	populated, _, err := population.Populate(lookupCtx, file)
	if err != nil {
		slog.WarnContext(ctx, "marker lookup failed", "file_id", file.ID, "error", err)
	}
	if populated == nil {
		return file
	}
	return populated
}

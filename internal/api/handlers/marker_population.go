package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/ctxerr"
	"github.com/Silo-Server/silo-server/internal/models"
)

type MarkerPopulationService interface {
	Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error)
}

const markerReadTimeout = 5 * time.Second

func populateFileMarkers(ctx context.Context, population MarkerPopulationService, file *models.MediaFile) *models.MediaFile {
	populated, _ := lookupFileMarkers(ctx, population, file)
	return populated
}

// lookupFileMarkers is populateFileMarkers that also reports a failed lookup,
// after which the result may lack provider markers a player was shown.
func lookupFileMarkers(ctx context.Context, population MarkerPopulationService, file *models.MediaFile) (*models.MediaFile, error) {
	if population == nil || file == nil {
		return file, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, markerReadTimeout)
	defer cancel()
	populated, _, err := population.Populate(lookupCtx, file)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "marker lookup failed", "file_id", file.ID, "error", err)
	}
	if populated == nil {
		return file, err
	}
	return populated, err
}

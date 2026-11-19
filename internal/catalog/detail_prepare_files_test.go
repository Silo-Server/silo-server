package catalog

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingProbeEnsurer records which half of the ensurer contract each
// prepare path asks for.
type recordingProbeEnsurer struct {
	probeCalls  []int
	cachedCalls []int
}

func (e *recordingProbeEnsurer) EnsureProbeOnly(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	e.probeCalls = append(e.probeCalls, file.ID)
	return file, nil
}

func (e *recordingProbeEnsurer) EnsureCopySafetyCached(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	e.cachedCalls = append(e.cachedCalls, file.ID)
	return file, nil
}

type recordingCopySafetyRacer struct {
	raced []int
}

func (r *recordingCopySafetyRacer) RaceScan(fileID int) {
	r.raced = append(r.raced, fileID)
}

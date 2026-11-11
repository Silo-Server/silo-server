package scanner

import (
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// scanRootInference infers content roots for the files of one scan scope a
// batch at a time. add returns the assignments of the files it is given, which
// the caller keeps only while it processes that batch; the root snapshots and
// observations in result cover every file added, and equal the ones a single
// inferRootAssignments call over all of them returns. Files must be added in
// walk order.
type scanRootInference struct {
	inference       *naming.RootInference
	fileTaggedRoots map[string]bool
}

func newScanRootInference(
	libraryType string,
	folderID int,
	overrides map[string]models.MediaRootOverride,
	libraryRoots ...string,
) *scanRootInference {
	return &scanRootInference{
		inference:       naming.NewRootInference(libraryType, folderID, overrides, libraryRoots...),
		fileTaggedRoots: make(map[string]bool),
	}
}

func (a *scanRootInference) add(filePaths []string) map[string]fileRootAssignment {
	assignments := make(map[string]fileRootAssignment, len(filePaths))
	for _, rawPath := range filePaths {
		assignment := a.inference.Add(rawPath)
		assignments[filepath.Clean(rawPath)] = assignment
		if assignment.HasFileIDs {
			a.fileTaggedRoots[assignment.RootPath] = true
		}
	}
	return assignments
}

// result returns the snapshots and observations of the roots seen so far. Its
// Assignments are nil: add already handed each batch's out.
func (a *scanRootInference) result() rootInferenceResult {
	snapshots := a.inference.Snapshots()
	observations := make([]RootObservation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		observations = append(observations, observationFromSnapshot(snapshot, a.fileTaggedRoots[snapshot.RootPath]))
	}
	return rootInferenceResult{
		Observations: observations,
		Snapshots:    snapshots,
	}
}

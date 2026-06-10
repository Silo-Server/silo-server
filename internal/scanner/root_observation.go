package scanner

import (
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

const (
	RootObservationReasonMatchable        = "matchable"
	RootObservationReasonMissingFolderIDs = "missing_folder_ids"
)

// RootObservation summarizes one scanned content root and whether it is
// eligible for scanner-driven matching.
type RootObservation struct {
	RootPath       string
	SampleFilePath string
	FileCount      int
	HasFolderIDs   bool
	Reason         string
}

type fileRootAssignment = naming.RootAssignment

type rootInferenceResult struct {
	Observations []RootObservation
	Snapshots    []models.ScannedMediaRoot
	Assignments  map[string]fileRootAssignment
}

// ObserveRoot derives the logical content root for a media file path.
func ObserveRoot(filePath string, libraryType string) (RootObservation, bool) {
	result := inferRootAssignments([]string{filePath}, libraryType, 0, nil)
	assignment, ok := result.Assignments[filepath.Clean(filePath)]
	if !ok {
		return RootObservation{}, false
	}
	return observationFromAssignment(assignment), true
}

func collectRootObservations(filePaths []string, libraryType string) []RootObservation {
	return inferRootAssignments(filePaths, libraryType, 0, nil).Observations
}

func collectScannedRoots(
	filePaths []string,
	libraryType string,
	folderID int,
	overrides map[string]models.MediaRootOverride,
) []models.ScannedMediaRoot {
	return inferRootAssignments(filePaths, libraryType, folderID, overrides).Snapshots
}

func inferRootAssignments(
	filePaths []string,
	libraryType string,
	folderID int,
	overrides map[string]models.MediaRootOverride,
) rootInferenceResult {
	snapshots, assignments := naming.InferRootAssignments(filePaths, libraryType, folderID, overrides)
	observations := make([]RootObservation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		observations = append(observations, observationFromSnapshot(snapshot))
	}
	return rootInferenceResult{
		Observations: observations,
		Snapshots:    snapshots,
		Assignments:  assignments,
	}
}

func observationFromAssignment(assignment fileRootAssignment) RootObservation {
	observation := RootObservation{
		RootPath:       assignment.RootPath,
		SampleFilePath: assignment.FilePath,
		FileCount:      1,
		HasFolderIDs:   assignment.HasFolderIDs,
		Reason:         RootObservationReasonMissingFolderIDs,
	}
	if assignment.HasFolderIDs {
		observation.Reason = RootObservationReasonMatchable
	}
	return observation
}

func observationFromSnapshot(snapshot models.ScannedMediaRoot) RootObservation {
	hasFolderIDs := naming.ParseFolderIDs(filepath.Base(snapshot.RootPath)) != nil
	observation := RootObservation{
		RootPath:       snapshot.RootPath,
		SampleFilePath: snapshot.SampleFilePath,
		FileCount:      snapshot.ObservedFileCount,
		HasFolderIDs:   hasFolderIDs,
		Reason:         RootObservationReasonMissingFolderIDs,
	}
	if hasFolderIDs {
		observation.Reason = RootObservationReasonMatchable
	}
	return observation
}

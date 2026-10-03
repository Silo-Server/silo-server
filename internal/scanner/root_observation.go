package scanner

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

const (
	RootObservationReasonMatchable        = "matchable"
	RootObservationReasonMissingFolderIDs = "missing_folder_ids"

	// movieRootType is naming's inferred type for a movie root.
	movieRootType = "movie"
)

// RootObservation summarizes one scanned content root and whether it is
// eligible for scanner-driven matching. HasProviderIDs is true when the root
// folder's name carries provider IDs or, for a movie root, any of its files'
// names does.
type RootObservation struct {
	RootPath       string
	SampleFilePath string
	FileCount      int
	HasProviderIDs bool
	Reason         string
}

type fileRootAssignment = naming.RootAssignment

type rootInferenceResult struct {
	Observations []RootObservation
	Snapshots    []models.ScannedMediaRoot
	Assignments  map[string]fileRootAssignment
}

// ObserveRoot derives the logical content root for a media file path.
func ObserveRoot(filePath string, libraryType string, libraryRoots ...string) (RootObservation, bool) {
	assignment, ok := observeRootAssignment(filePath, libraryType, libraryRoots...)
	if !ok {
		return RootObservation{}, false
	}
	return observationFromAssignment(assignment), true
}

// observedRootFileLister lists the cataloged files of one observed root.
type observedRootFileLister interface {
	ListByObservedRootPath(ctx context.Context, folderID int, observedRootPath string) ([]*models.MediaFile, error)
}

// ObserveFileRoot is ObserveRoot for a single-file scan. A full scan counts a
// movie root as tagged when any of its files' names carries a provider tag, so
// an untagged file also consults the root's cataloged siblings. Otherwise a
// newly added untagged version would re-flag a root the full scan cleared.
func (s *Scanner) ObserveFileRoot(ctx context.Context, folderID int, filePath, libraryType string, libraryRoots ...string) (RootObservation, bool, error) {
	var lister observedRootFileLister
	if s != nil && s.fileRepo != nil {
		lister = s.fileRepo
	}
	return observeFileRoot(ctx, lister, folderID, filePath, libraryType, libraryRoots...)
}

func observeFileRoot(
	ctx context.Context,
	lister observedRootFileLister,
	folderID int,
	filePath, libraryType string,
	libraryRoots ...string,
) (RootObservation, bool, error) {
	assignment, ok := observeRootAssignment(filePath, libraryType, libraryRoots...)
	if !ok {
		return RootObservation{}, false, nil
	}
	observation := observationFromAssignment(assignment)
	if observation.HasProviderIDs || assignment.InferredType != movieRootType || lister == nil {
		return observation, true, nil
	}
	siblings, err := lister.ListByObservedRootPath(ctx, folderID, assignment.RootPath)
	if err != nil {
		return RootObservation{}, false, fmt.Errorf("listing files of root %q: %w", assignment.RootPath, err)
	}
	if naming.AnyFileNameHasProviderTag(siblings) {
		observation = newRootObservation(observation.RootPath, observation.SampleFilePath, observation.FileCount, true)
	}
	return observation, true, nil
}

func observeRootAssignment(filePath string, libraryType string, libraryRoots ...string) (fileRootAssignment, bool) {
	kind := librarykind.Of(libraryType)
	if kind.Movie || kind.TV || kind.Mixed {
		if _, theme := themesongs.OwnerDirectory(filePath); theme {
			return fileRootAssignment{}, false
		}
	}
	result := inferRootAssignments([]string{filePath}, libraryType, 0, nil, libraryRoots...)
	assignment, ok := result.Assignments[filepath.Clean(filePath)]
	return assignment, ok
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
	libraryRoots ...string,
) rootInferenceResult {
	snapshots, assignments := naming.InferRootAssignments(filePaths, libraryType, folderID, overrides, libraryRoots...)
	fileTaggedRoots := make(map[string]bool)
	for _, assignment := range assignments {
		if assignment.HasFileIDs {
			fileTaggedRoots[assignment.RootPath] = true
		}
	}
	observations := make([]RootObservation, 0, len(snapshots))
	for _, snapshot := range snapshots {
		observations = append(observations, observationFromSnapshot(snapshot, fileTaggedRoots[snapshot.RootPath]))
	}
	return rootInferenceResult{
		Observations: observations,
		Snapshots:    snapshots,
		Assignments:  assignments,
	}
}

// observationFromAssignment counts a file-name tag only for a movie file: a
// movie's tag identifies its root, but an episode's cannot vouch for the series.
func observationFromAssignment(assignment fileRootAssignment) RootObservation {
	return newRootObservation(
		assignment.RootPath,
		assignment.FilePath,
		1,
		assignment.HasFolderIDs || (assignment.HasFileIDs && assignment.InferredType == movieRootType),
	)
}

// observationFromSnapshot checks the root's final type, after any override, so
// a root forced to movie counts its files' tags.
func observationFromSnapshot(snapshot models.ScannedMediaRoot, fileTagged bool) RootObservation {
	hasProviderIDs := naming.ParseFolderIDs(filepath.Base(snapshot.RootPath)) != nil ||
		(fileTagged && snapshot.InferredType == movieRootType)
	return newRootObservation(snapshot.RootPath, snapshot.SampleFilePath, snapshot.ObservedFileCount, hasProviderIDs)
}

func newRootObservation(rootPath, sampleFilePath string, fileCount int, hasProviderIDs bool) RootObservation {
	observation := RootObservation{
		RootPath:       rootPath,
		SampleFilePath: sampleFilePath,
		FileCount:      fileCount,
		HasProviderIDs: hasProviderIDs,
		Reason:         RootObservationReasonMissingFolderIDs,
	}
	if hasProviderIDs {
		observation.Reason = RootObservationReasonMatchable
	}
	return observation
}

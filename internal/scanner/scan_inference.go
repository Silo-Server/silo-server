package scanner

import (
	"encoding/json"
	"path/filepath"
	"sort"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

const (
	inferenceStateResolved  = "resolved"
	inferenceStateAmbiguous = "ambiguous"
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

// scanGroupInference is scanRootInference's counterpart for content groups. A
// group or an observed location can hold files from any directory of the
// scope, so their snapshots are tallied across every batch; only the per-file
// identities are handed back batch by batch. result equals what a single
// inferGroupAssignments call over all the files returns.
//
// A tally keeps the whole identity of its first file, which supplies the
// snapshot's sample and identity fields, and for the rest of its files only
// what the snapshot counts.
type scanGroupInference struct {
	libraryType       string
	folderID          int
	identityOverrides *identityOverrideSet
	groups            map[string]*groupTally
	locations         map[string]*locationTally
}

type groupTally struct {
	first fileGroupAssignment
	// observedRoots holds the observed root of each file, in walk order. The
	// strings are the location tallies' own, so a file costs one string header.
	observedRoots []string
	// locations holds the group's distinct observed roots, first seen first.
	locations     []models.MediaGroupLocation
	seenLocations map[string]struct{}
	ambiguous     bool
	overridden    bool
}

type locationTally struct {
	first     fileGroupAssignment
	fileCount int
	// groups maps each group with a file here to whether any of those files
	// was placed without an identity override.
	groups    map[string]bool
	ambiguous bool
}

// groupEvidence and locationEvidence are stored with the snapshots. Their
// fields are in the alphabetical order of their JSON names.
type groupEvidence struct {
	FileCount     int      `json:"file_count"`
	ObservedRoots []string `json:"observed_roots"`
}

type locationEvidence struct {
	GroupCount       int    `json:"group_count"`
	ObservedRootPath string `json:"observed_root_path"`
}

func newScanGroupInference(libraryType string, folderID int, identityOverrides *identityOverrideSet) *scanGroupInference {
	return &scanGroupInference{
		libraryType:       libraryType,
		folderID:          folderID,
		identityOverrides: identityOverrides,
		groups:            make(map[string]*groupTally),
		locations:         make(map[string]*locationTally),
	}
}

// add infers the group identity of each file and returns the identities by
// clean path. rootAssignments are the same files' root assignments.
func (a *scanGroupInference) add(filePaths []string, rootAssignments map[string]fileRootAssignment) map[string]fileGroupAssignment {
	assignments := inferGroupAssignments(filePaths, a.libraryType, a.folderID, rootAssignments, a.identityOverrides).Assignments
	for _, rawPath := range filePaths {
		a.observe(assignments[filepath.Clean(rawPath)])
	}
	return assignments
}

func (a *scanGroupInference) observe(identity fileGroupAssignment) {
	groupKey := groupBucketKey(identity)
	ambiguous := identity.State == inferenceStateAmbiguous

	location := a.locations[identity.ObservedRootPath]
	if location == nil {
		location = &locationTally{first: identity, groups: make(map[string]bool)}
		a.locations[identity.ObservedRootPath] = location
	}
	location.fileCount++
	location.groups[groupKey] = location.groups[groupKey] || !identity.Overridden
	location.ambiguous = location.ambiguous || ambiguous

	group := a.groups[groupKey]
	if group == nil {
		group = &groupTally{first: identity, seenLocations: make(map[string]struct{})}
		a.groups[groupKey] = group
	}
	observedRoot := location.first.ObservedRootPath
	if _, seen := group.seenLocations[observedRoot]; !seen {
		group.seenLocations[observedRoot] = struct{}{}
		group.locations = append(group.locations, models.MediaGroupLocation{
			MediaFolderID:    a.folderID,
			GroupKeyVersion:  identity.GroupKeyVersion,
			ContentGroupKey:  identity.ContentGroupKey,
			ObservedRootPath: observedRoot,
			IsPrimary:        len(group.observedRoots) == 0,
		})
	}
	group.observedRoots = append(group.observedRoots, observedRoot)
	group.ambiguous = group.ambiguous || ambiguous
	group.overridden = group.overridden || identity.Overridden
}

// result returns the group, location and group-location snapshots of the files
// seen so far. Its Assignments are nil: add already handed each batch's out.
func (a *scanGroupInference) result() groupInferenceResult {
	groupKeys := make([]string, 0, len(a.groups))
	for key := range a.groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)

	scannedGroups := make([]models.ScannedMediaGroup, 0, len(groupKeys))
	groupLocations := make([]models.MediaGroupLocation, 0, len(groupKeys))
	for _, key := range groupKeys {
		group := a.groups[key]
		scannedGroups = append(scannedGroups, group.snapshot(a.folderID))
		groupLocations = append(groupLocations, group.locations...)
	}

	locationKeys := make([]string, 0, len(a.locations))
	for key := range a.locations {
		locationKeys = append(locationKeys, key)
	}
	sort.Strings(locationKeys)

	locations := make([]models.ObservedMediaLocation, 0, len(locationKeys))
	for _, observedRoot := range locationKeys {
		locations = append(locations, a.locations[observedRoot].snapshot(a.folderID, observedRoot))
	}

	return groupInferenceResult{
		ScannedGroups:  scannedGroups,
		Locations:      locations,
		GroupLocations: groupLocations,
	}
}

func (g *groupTally) snapshot(folderID int) models.ScannedMediaGroup {
	state := inferenceStateResolved
	if g.ambiguous {
		state = inferenceStateAmbiguous
	}
	overrideSource := overrideSourceNone
	if g.overridden {
		overrideSource = overrideSourceManual
	}
	evidence, _ := json.Marshal(groupEvidence{
		FileCount:     len(g.observedRoots),
		ObservedRoots: g.observedRoots,
	})
	first := g.first
	return models.ScannedMediaGroup{
		MediaFolderID:          folderID,
		GroupKeyVersion:        first.GroupKeyVersion,
		ContentGroupKey:        first.ContentGroupKey,
		State:                  state,
		InferredType:           first.BaseType,
		TypeConfidence:         first.Confidence,
		BaseTitle:              first.BaseTitle,
		BaseYear:               first.BaseYear,
		TmdbID:                 first.TmdbID,
		ImdbID:                 first.ImdbID,
		TvdbID:                 first.TvdbID,
		ObservedFileCount:      len(g.observedRoots),
		SampleFilePath:         first.RepresentativePath,
		SampleObservedRootPath: first.ObservedRootPath,
		EvidenceJSON:           evidence,
		OverrideSource:         overrideSource,
	}
}

func (l *locationTally) snapshot(folderID int, observedRoot string) models.ObservedMediaLocation {
	groupCount := len(l.groups)
	nonOverriddenGroups := 0
	for _, hasNonOverriddenFile := range l.groups {
		if hasNonOverriddenFile {
			nonOverriddenGroups++
		}
	}
	// A location split across groups is ambiguous only when the split was not
	// operator-directed: identity overrides deliberately place files from one
	// folder into different groups.
	state := inferenceStateResolved
	if l.ambiguous || (groupCount > 1 && nonOverriddenGroups > 1) {
		state = inferenceStateAmbiguous
	}
	primary := l.first
	primaryGroupVersion := 0
	primaryGroupKey := ""
	if groupCount == 1 {
		primaryGroupVersion = primary.GroupKeyVersion
		primaryGroupKey = primary.ContentGroupKey
	}
	evidence, _ := json.Marshal(locationEvidence{
		GroupCount:       groupCount,
		ObservedRootPath: observedRoot,
	})
	return models.ObservedMediaLocation{
		MediaFolderID:          folderID,
		ObservedRootPath:       observedRoot,
		LocationType:           primary.BaseType,
		SampleFilePath:         primary.RepresentativePath,
		ObservedFileCount:      l.fileCount,
		ContentGroupCount:      groupCount,
		PrimaryGroupKeyVersion: primaryGroupVersion,
		PrimaryContentGroupKey: primaryGroupKey,
		State:                  state,
		EvidenceJSON:           evidence,
	}
}

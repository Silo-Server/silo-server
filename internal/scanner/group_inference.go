package scanner

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

type fileGroupAssignment = naming.GroupIdentity

// identityOverrideSet indexes path-scoped identity overrides for per-file
// lookup during inference. File scope wins over root scope; among root
// overrides the deepest matching root wins.
type identityOverrideSet struct {
	byFile map[string]*models.MediaIdentityOverride
	byRoot map[string]*models.MediaIdentityOverride
}

func newIdentityOverrideSet(overrides []models.MediaIdentityOverride) *identityOverrideSet {
	if len(overrides) == 0 {
		return nil
	}
	set := &identityOverrideSet{
		byFile: map[string]*models.MediaIdentityOverride{},
		byRoot: map[string]*models.MediaIdentityOverride{},
	}
	for i := range overrides {
		override := &overrides[i]
		switch override.Scope {
		case models.IdentityOverrideScopeFile:
			if path := filepath.Clean(override.FilePath); path != "" && path != "." {
				set.byFile[path] = override
			}
		case models.IdentityOverrideScopeRoot:
			if path := filepath.Clean(override.RootPath); path != "" && path != "." {
				set.byRoot[path] = override
			}
		}
	}
	return set
}

func (s *identityOverrideSet) lookup(filePath string) *models.MediaIdentityOverride {
	if s == nil {
		return nil
	}
	if override, ok := s.byFile[filePath]; ok {
		return override
	}
	// Walk ancestor directories so the deepest matching root override wins.
	for dir := filepath.Dir(filePath); ; dir = filepath.Dir(dir) {
		if override, ok := s.byRoot[dir]; ok {
			return override
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
	}
}

type groupInferenceResult struct {
	Assignments    map[string]fileGroupAssignment
	ScannedGroups  []models.ScannedMediaGroup
	Locations      []models.ObservedMediaLocation
	GroupLocations []models.MediaGroupLocation
}

const (
	overrideSourceNone   = "none"
	overrideSourceManual = "manual"

	groupStateAmbiguous = "ambiguous"
	groupStateResolved  = "resolved"
)

// inferGroupAssignments infers the group identity of each file and the
// content groups, observed locations and group locations the files make up.
func inferGroupAssignments(
	filePaths []string,
	libraryType string,
	folderID int,
	rootAssignments map[string]fileRootAssignment,
	identityOverrides *identityOverrideSet,
) groupInferenceResult {
	inference := newScanGroupInference(libraryType, folderID, identityOverrides)
	assignments := inference.add(filePaths, rootAssignments)
	result := inference.result()
	result.Assignments = assignments
	return result
}

// inferGroupIdentities returns the group identity of each file by clean path,
// with any identity override applied. A file's identity depends only on its
// own path, its root assignment and the overrides.
func inferGroupIdentities(
	filePaths []string,
	libraryType string,
	rootAssignments map[string]fileRootAssignment,
	identityOverrides *identityOverrideSet,
) map[string]fileGroupAssignment {
	assignments := make(map[string]fileGroupAssignment, len(filePaths))
	for _, rawPath := range filePaths {
		cleanPath := filepath.Clean(rawPath)
		rootAssignment, ok := rootAssignments[cleanPath]
		if !ok {
			rootAssignment = fileRootAssignment{
				FilePath:     cleanPath,
				RootPath:     filepath.Dir(cleanPath),
				InferredType: "movie",
			}
		}
		identity := naming.InferGroupIdentity(cleanPath, libraryType, rootAssignment)
		if override := identityOverrides.lookup(cleanPath); override != nil {
			naming.ApplyIdentityOverride(&identity, naming.IdentityOverride{
				ForcedType:   override.ForcedType,
				ForcedTitle:  override.ForcedTitle,
				ForcedYear:   override.ForcedYear,
				ForcedTmdbID: override.ForcedTmdbID,
				ForcedImdbID: override.ForcedImdbID,
				ForcedTvdbID: override.ForcedTvdbID,
			})
		}
		assignments[cleanPath] = identity
	}
	return assignments
}

// scanGroupInference tallies the content groups and observed locations of the
// files added to it. add hands back the files' identities; result returns the
// group, location and group-location snapshots of every file added so far, the
// same however the files were split across add calls.
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
	assignments := inferGroupIdentities(filePaths, a.libraryType, rootAssignments, a.identityOverrides)
	for _, rawPath := range filePaths {
		a.observe(assignments[filepath.Clean(rawPath)])
	}
	return assignments
}

func (a *scanGroupInference) observe(identity fileGroupAssignment) {
	groupKey := groupBucketKey(identity)
	ambiguous := identity.State == groupStateAmbiguous

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
	state := groupStateResolved
	if g.ambiguous {
		state = groupStateAmbiguous
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
	// folder into different groups, and flagging that forever would make every
	// resolved split look broken.
	state := groupStateResolved
	if l.ambiguous || (groupCount > 1 && nonOverriddenGroups > 1) {
		state = groupStateAmbiguous
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

func groupBucketKey(identity naming.GroupIdentity) string {
	return identity.ContentGroupKey + "|" + identity.BaseType
}

func applyGroupOverrides(result *groupInferenceResult, overridesByKey map[string]models.MediaGroupOverride) {
	if result == nil || len(overridesByKey) == 0 {
		return
	}

	for i, group := range result.ScannedGroups {
		override, ok := overridesByKey[groupOverrideKey(group.GroupKeyVersion, group.ContentGroupKey)]
		if !ok {
			continue
		}
		group = applyGroupOverrideToSnapshot(group, override)
		result.ScannedGroups[i] = group
	}

	for i, location := range result.Locations {
		override, ok := overridesByKey[groupOverrideKey(location.PrimaryGroupKeyVersion, location.PrimaryContentGroupKey)]
		if !ok {
			continue
		}
		if forcedType := strings.TrimSpace(override.ForcedType); forcedType != "" {
			location.LocationType = forcedType
		}
		if location.ContentGroupCount <= 1 {
			location.State = groupStateResolved
		}
		result.Locations[i] = location
	}
}

func groupOverrideKey(version int, key string) string {
	return fmtGroupOverrideKey(version, key)
}

func fmtGroupOverrideKey(version int, key string) string {
	return strings.Join([]string{strconv.Itoa(version), key}, "|")
}

func applyGroupOverrideToSnapshot(group models.ScannedMediaGroup, override models.MediaGroupOverride) models.ScannedMediaGroup {
	if forcedType := strings.TrimSpace(override.ForcedType); forcedType != "" {
		group.InferredType = forcedType
	}
	if forcedTitle := strings.TrimSpace(override.ForcedTitle); forcedTitle != "" {
		group.BaseTitle = forcedTitle
	}
	if override.ForcedYear > 0 {
		group.BaseYear = override.ForcedYear
	}
	if forcedTmdbID := strings.TrimSpace(override.ForcedTmdbID); forcedTmdbID != "" {
		group.TmdbID = forcedTmdbID
	}
	if forcedImdbID := strings.TrimSpace(override.ForcedImdbID); forcedImdbID != "" {
		group.ImdbID = forcedImdbID
	}
	if forcedTvdbID := strings.TrimSpace(override.ForcedTvdbID); forcedTvdbID != "" {
		group.TvdbID = forcedTvdbID
	}
	group.TypeConfidence = "high"
	group.State = groupStateResolved
	group.OverrideSource = overrideSourceManual
	return group
}

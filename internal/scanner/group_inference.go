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

func inferGroupAssignments(
	filePaths []string,
	libraryType string,
	folderID int,
	rootAssignments map[string]fileRootAssignment,
	identityOverrides *identityOverrideSet,
) groupInferenceResult {
	assignments := make(map[string]fileGroupAssignment, len(filePaths))
	groupBuckets := make(map[string][]fileGroupAssignment)
	locationBuckets := make(map[string][]fileGroupAssignment)

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
		groupBuckets[groupBucketKey(identity)] = append(groupBuckets[groupBucketKey(identity)], identity)
		locationBuckets[identity.ObservedRootPath] = append(locationBuckets[identity.ObservedRootPath], identity)
	}

	groupKeys := make([]string, 0, len(groupBuckets))
	for key := range groupBuckets {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)

	scannedGroups := make([]models.ScannedMediaGroup, 0, len(groupKeys))
	groupLocations := make([]models.MediaGroupLocation, 0, len(groupKeys))
	for _, key := range groupKeys {
		entries := groupBuckets[key]
		if len(entries) == 0 {
			continue
		}
		first := entries[0]
		scannedGroups = append(scannedGroups, models.ScannedMediaGroup{
			MediaFolderID:          folderID,
			GroupKeyVersion:        first.GroupKeyVersion,
			ContentGroupKey:        first.ContentGroupKey,
			State:                  aggregateGroupState(entries),
			InferredType:           first.BaseType,
			TypeConfidence:         first.Confidence,
			BaseTitle:              first.BaseTitle,
			BaseYear:               first.BaseYear,
			TmdbID:                 first.TmdbID,
			ImdbID:                 first.ImdbID,
			TvdbID:                 first.TvdbID,
			ObservedFileCount:      len(entries),
			SampleFilePath:         first.RepresentativePath,
			SampleObservedRootPath: first.ObservedRootPath,
			EvidenceJSON:           aggregateGroupEvidence(entries),
			OverrideSource:         aggregateGroupOverrideSource(entries),
		})

		seenLocations := map[string]bool{}
		for i, entry := range entries {
			if seenLocations[entry.ObservedRootPath] {
				continue
			}
			seenLocations[entry.ObservedRootPath] = true
			groupLocations = append(groupLocations, models.MediaGroupLocation{
				MediaFolderID:    folderID,
				GroupKeyVersion:  entry.GroupKeyVersion,
				ContentGroupKey:  entry.ContentGroupKey,
				ObservedRootPath: entry.ObservedRootPath,
				IsPrimary:        i == 0,
			})
		}
	}

	locationKeys := make([]string, 0, len(locationBuckets))
	for key := range locationBuckets {
		locationKeys = append(locationKeys, key)
	}
	sort.Strings(locationKeys)

	locations := make([]models.ObservedMediaLocation, 0, len(locationKeys))
	for _, observedRoot := range locationKeys {
		entries := locationBuckets[observedRoot]
		if len(entries) == 0 {
			continue
		}
		groupCounts := make(map[string]fileGroupAssignment)
		for _, entry := range entries {
			groupCounts[groupBucketKey(entry)] = entry
		}
		primaryGroupVersion := 0
		primaryGroupKey := ""
		primary := entries[0]
		if len(groupCounts) == 1 {
			primaryGroupVersion = primary.GroupKeyVersion
			primaryGroupKey = primary.ContentGroupKey
		}
		locationEvidence, _ := json.Marshal(map[string]any{
			"observed_root_path": observedRoot,
			"group_count":        len(groupCounts),
		})
		locations = append(locations, models.ObservedMediaLocation{
			MediaFolderID:          folderID,
			ObservedRootPath:       observedRoot,
			LocationType:           primary.BaseType,
			SampleFilePath:         primary.RepresentativePath,
			ObservedFileCount:      len(entries),
			ContentGroupCount:      len(groupCounts),
			PrimaryGroupKeyVersion: primaryGroupVersion,
			PrimaryContentGroupKey: primaryGroupKey,
			State:                  aggregateLocationState(entries),
			EvidenceJSON:           locationEvidence,
		})
	}

	return groupInferenceResult{
		Assignments:    assignments,
		ScannedGroups:  scannedGroups,
		Locations:      locations,
		GroupLocations: groupLocations,
	}
}

func groupBucketKey(identity naming.GroupIdentity) string {
	return identity.ContentGroupKey + "|" + identity.BaseType
}

func aggregateGroupState(entries []fileGroupAssignment) string {
	for _, entry := range entries {
		if entry.State == "ambiguous" {
			return "ambiguous"
		}
	}
	return "resolved"
}

func aggregateLocationState(entries []fileGroupAssignment) string {
	state := "resolved"
	seenGroups := map[string]struct{}{}
	nonOverriddenGroups := map[string]struct{}{}
	for _, entry := range entries {
		seenGroups[groupBucketKey(entry)] = struct{}{}
		if !entry.Overridden {
			nonOverriddenGroups[groupBucketKey(entry)] = struct{}{}
		}
		if entry.State == "ambiguous" {
			state = "ambiguous"
		}
	}
	// A location split across groups is ambiguous only when the split was not
	// operator-directed: identity overrides deliberately place files from one
	// folder into different groups, and flagging that forever would make every
	// resolved split look broken.
	if len(seenGroups) > 1 && len(nonOverriddenGroups) > 1 {
		state = "ambiguous"
	}
	return state
}

const (
	overrideSourceNone   = "none"
	overrideSourceManual = "manual"
)

func aggregateGroupOverrideSource(entries []fileGroupAssignment) string {
	for _, entry := range entries {
		if entry.Overridden {
			return overrideSourceManual
		}
	}
	return overrideSourceNone
}

func aggregateGroupEvidence(entries []fileGroupAssignment) []byte {
	observedRoots := make([]string, 0, len(entries))
	for _, entry := range entries {
		observedRoots = append(observedRoots, entry.ObservedRootPath)
	}
	evidence, _ := json.Marshal(map[string]any{
		"observed_roots": observedRoots,
		"file_count":     len(entries),
	})
	return evidence
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
			location.State = "resolved"
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
	group.State = "resolved"
	group.OverrideSource = overrideSourceManual
	return group
}

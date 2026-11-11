package scanner

import (
	"path/filepath"
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
// A full scan builds the same result a batch at a time with
// scanGroupInference; this is that inference over one list.
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

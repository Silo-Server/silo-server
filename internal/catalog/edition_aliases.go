package catalog

import (
	"cmp"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// EditionAliases maps the original keys of an item's proven alias groups to
// their catalog keys. It is derived from stored metadata, never persisted.
type EditionAliases map[string]string

func (a EditionAliases) Key(key string) string {
	return cmp.Or(a[key], key)
}

// ResolveEditionAliases compares the live files of one movie or episode that
// the viewer may access. A catalog-only library scope narrows the projection
// after grouping. An unreadable or incompatible file keeps the family on its
// stored keys, since it still appears in the client version list.
func ResolveEditionAliases(files []*models.MediaFile, filter AccessFilter) EditionAliases {
	var families map[string][]*models.MediaFile
	for _, file := range files {
		if !FileAllowedByAccess(file, filter) || file.MissingSince != nil || file.EditionKey == "" {
			continue
		}
		if family := naming.EditionAliasFamily(file.EditionKey); family != "" {
			if families == nil {
				families = make(map[string][]*models.MediaFile)
			}
			families[family] = append(families[family], file)
		}
	}
	var aliases EditionAliases
	for family, members := range families {
		if len(members) < 2 || !compatibleEditionAliases(members) {
			continue
		}
		if aliases == nil {
			aliases = make(EditionAliases)
		}
		aliases[family] = family
		for _, file := range members {
			aliases[file.EditionKey] = family
		}
	}
	return aliases
}

func compatibleEditionAliases(files []*models.MediaFile) bool {
	first := files[0]
	shortest, longest := first.Duration, first.Duration
	differentKeys := false
	for _, file := range files {
		if !standaloneEditionFile(file) || file.EpisodeID != first.EpisodeID || file.ContentID != first.ContentID ||
			file.ProbeUpdatedAt == nil || file.Duration <= 0 ||
			models.VideoDurationImplausible(float64(file.Duration), file.FileSize, true) {
			return false
		}
		shortest, longest = min(shortest, file.Duration), max(longest, file.Duration)
		differentKeys = differentKeys || file.EditionKey != first.EditionKey
	}
	return differentKeys && longest-shortest <= 5
}

func standaloneEditionFile(file *models.MediaFile) bool {
	return file.ExtraID == "" && !file.IsAudioOnly() && (file.CodecVideo != "" || len(file.VideoTracks) > 0) &&
		file.PresentationKind == "" && file.PresentationGroupKey == "" &&
		file.PresentationPartIndex == 0 && file.PresentationPartTotal == 0 &&
		file.MultiEpisodeStart == 0 && file.MultiEpisodeEnd == 0
}

// AllowsSubstitution only extends matching for files in a proven group. The
// caller can still apply its existing rules to equal stored keys.
func (a EditionAliases) AllowsSubstitution(source, candidate *models.MediaFile) bool {
	key := a[source.EditionKey]
	return key != "" && key == a[candidate.EditionKey] &&
		!source.ProbeRejected() && !candidate.ProbeRejected() &&
		standaloneEditionFile(source) && standaloneEditionFile(candidate)
}

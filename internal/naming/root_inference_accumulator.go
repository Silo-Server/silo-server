package naming

import (
	"encoding/json"
	"path/filepath"
	"sort"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	rootTypeMovie        = "movie"
	rootStateResolved    = "resolved"
	rootStateAmbiguous   = "ambiguous"
	rootConfidenceHigh   = "high"
	rootConfidenceMedium = mediumIdentityConfidence
	rootConfidenceLow    = "low"
	rootOverrideNone     = "none"
	rootOverrideManual   = "manual"
)

// RootInference infers the content root of media files one file at a time and
// summarizes each root it has seen. A file's assignment depends only on its
// own path, the overrides and the library roots, so a caller can add the files
// of a library in batches, keep each batch's assignments only while it needs
// them, and still get the same root snapshots as one pass over every file.
// Files must be added in walk order: a root's sample file, title and year come
// from the first file that supplies them.
type RootInference struct {
	libraryType  string
	folderID     int
	overrides    map[string]models.MediaRootOverride
	libraryRoots []string
	byRoot       map[string]*rootAggregate
}

type rootAggregate struct {
	root                 models.ScannedMediaRoot
	hasFolderIDs         bool
	wrapperCollapses     int
	ancestorPromotions   int
	legacyDisagreements  int
	seasonEvidenceCount  int
	episodeEvidenceCount int
	movieEvidenceCount   int
	releaseDensityCount  int
	movieTypeVotes       int
	seriesTypeVotes      int
	contradictionCount   int
}

// rootEvidence is the evidence stored with a root snapshot. Its fields are in
// the alphabetical order of their JSON names.
type rootEvidence struct {
	AncestorPromotions   int  `json:"ancestor_promotions"`
	ContradictionFiles   int  `json:"contradiction_files"`
	EpisodePatternFiles  int  `json:"episode_pattern_files"`
	HasFolderIDs         bool `json:"has_folder_ids"`
	LegacyDisagreements  int  `json:"legacy_disagreements"`
	MovieEvidenceFiles   int  `json:"movie_evidence_files"`
	MovieTypeVotes       int  `json:"movie_type_votes"`
	ObservedFileCount    int  `json:"observed_file_count"`
	OverrideApplied      bool `json:"override_applied"`
	ReleaseDensityFiles  int  `json:"release_density_files"`
	SeasonStructureFiles int  `json:"season_structure_files"`
	SeriesTypeVotes      int  `json:"series_type_votes"`
	WrapperCollapses     int  `json:"wrapper_collapses"`
}

// NewRootInference starts an inference for one library folder.
func NewRootInference(
	libraryType string,
	folderID int,
	overrides map[string]models.MediaRootOverride,
	libraryRoots ...string,
) *RootInference {
	return &RootInference{
		libraryType:  libraryType,
		folderID:     folderID,
		overrides:    overrides,
		libraryRoots: libraryRoots,
		byRoot:       make(map[string]*rootAggregate),
	}
}

// Add infers the root of one file, counts the file toward that root and
// returns the file's assignment.
func (r *RootInference) Add(rawPath string) RootAssignment {
	cleanFilePath := filepath.Clean(rawPath)
	assignment := inferFileRootAssignment(cleanFilePath, r.libraryType, r.overrides, r.libraryRoots...)

	agg, found := r.byRoot[assignment.RootPath]
	if !found {
		agg = newRootAggregate(r.folderID, cleanFilePath, assignment)
		r.byRoot[assignment.RootPath] = agg
	}
	agg.observe(assignment)
	return assignment
}

func newRootAggregate(folderID int, cleanFilePath string, assignment RootAssignment) *rootAggregate {
	agg := &rootAggregate{
		root: models.ScannedMediaRoot{
			MediaFolderID:     folderID,
			RootPath:          assignment.RootPath,
			State:             rootStateResolved,
			InferredType:      assignment.InferredType,
			TypeConfidence:    rootConfidenceLow,
			ObservedFileCount: 0,
			SampleFilePath:    cleanFilePath,
			OverrideSource:    rootOverrideNone,
			Title:             assignment.Title,
			Year:              assignment.Year,
		},
	}
	if ids := ParseFolderIDs(filepath.Base(assignment.RootPath)); ids != nil && assignment.RootPath != assignment.LibraryRootPath {
		agg.hasFolderIDs = true
		agg.root.TmdbID = ids.TmdbID
		agg.root.ImdbID = ids.ImdbID
		agg.root.TvdbID = ids.TvdbID
	}
	return agg
}

func (agg *rootAggregate) observe(assignment RootAssignment) {
	agg.root.ObservedFileCount++
	agg.hasFolderIDs = agg.hasFolderIDs || assignment.HasFolderIDs
	agg.wrapperCollapses += countIf(assignment.WrapperCollapsed)
	agg.ancestorPromotions += countIf(assignment.PromotedAncestor)
	agg.legacyDisagreements += countIf(assignment.LegacyRootPath != "" && assignment.LegacyRootPath != assignment.RootPath)
	agg.seasonEvidenceCount += countIf(assignment.HasSeasonStructure)
	agg.episodeEvidenceCount += countIf(assignment.HasEpisodePattern)
	agg.movieEvidenceCount += countIf(assignment.HasMovieEvidence)
	agg.contradictionCount += countIf(assignment.HasStrongContradiction)
	agg.releaseDensityCount += countIf(inferReleaseTokenRe.MatchString(filepath.Base(assignment.RootPath)))
	if assignment.InferredType == seriesContentType {
		agg.seriesTypeVotes++
	} else {
		agg.movieTypeVotes++
	}
	if agg.root.Title == "" && assignment.Title != "" {
		agg.root.Title = assignment.Title
	}
	if agg.root.Year == 0 && assignment.Year != 0 {
		agg.root.Year = assignment.Year
	}
}

func countIf(condition bool) int {
	if condition {
		return 1
	}
	return 0
}

// Snapshots returns one snapshot per root seen so far, ordered by root path.
func (r *RootInference) Snapshots() []models.ScannedMediaRoot {
	roots := make([]string, 0, len(r.byRoot))
	for rootPath := range r.byRoot {
		roots = append(roots, rootPath)
	}
	sort.Strings(roots)

	snapshots := make([]models.ScannedMediaRoot, 0, len(roots))
	for _, rootPath := range roots {
		override, overridden := r.overrides[rootPath]
		snapshots = append(snapshots, r.byRoot[rootPath].snapshot(override, overridden))
	}
	return snapshots
}

func (agg *rootAggregate) snapshot(override models.MediaRootOverride, overridden bool) models.ScannedMediaRoot {
	root := agg.root
	root.InferredType = agg.inferredType()
	root.TypeConfidence = agg.typeConfidence()
	if agg.ambiguous(root.InferredType, root.TypeConfidence) {
		root.State = rootStateAmbiguous
	}
	if overridden {
		applyRootOverride(&root, override)
	}
	evidence, _ := json.Marshal(rootEvidence{
		AncestorPromotions:   agg.ancestorPromotions,
		ContradictionFiles:   agg.contradictionCount,
		EpisodePatternFiles:  agg.episodeEvidenceCount,
		HasFolderIDs:         agg.hasFolderIDs,
		LegacyDisagreements:  agg.legacyDisagreements,
		MovieEvidenceFiles:   agg.movieEvidenceCount,
		MovieTypeVotes:       agg.movieTypeVotes,
		ObservedFileCount:    root.ObservedFileCount,
		OverrideApplied:      root.OverrideSource == rootOverrideManual,
		ReleaseDensityFiles:  agg.releaseDensityCount,
		SeasonStructureFiles: agg.seasonEvidenceCount,
		SeriesTypeVotes:      agg.seriesTypeVotes,
		WrapperCollapses:     agg.wrapperCollapses,
	})
	root.EvidenceJSON = evidence
	return root
}

func (agg *rootAggregate) inferredType() string {
	switch {
	case agg.seriesTypeVotes > 0 && agg.movieTypeVotes == 0:
		return seriesContentType
	case agg.movieTypeVotes > 0 && agg.seriesTypeVotes == 0:
		return rootTypeMovie
	case agg.seasonEvidenceCount > 0 || agg.episodeEvidenceCount > agg.movieEvidenceCount:
		return seriesContentType
	default:
		return rootTypeMovie
	}
}

func (agg *rootAggregate) typeConfidence() string {
	switch {
	case agg.hasFolderIDs || agg.seasonEvidenceCount > 0 || agg.movieEvidenceCount > 0:
		return rootConfidenceHigh
	case agg.root.Title != "" || agg.root.Year != 0 || agg.episodeEvidenceCount > 0 || agg.root.ObservedFileCount > 1:
		return rootConfidenceMedium
	default:
		return rootConfidenceLow
	}
}

func (agg *rootAggregate) ambiguous(inferredType, typeConfidence string) bool {
	conflictingTypeVotes := agg.seriesTypeVotes > 0 && agg.movieTypeVotes > 0 &&
		(agg.seasonEvidenceCount > 0 || agg.episodeEvidenceCount > 0) &&
		agg.movieEvidenceCount > 0
	return conflictingTypeVotes ||
		(!agg.hasFolderIDs && typeConfidence == rootConfidenceLow && agg.root.Title == "") ||
		(!agg.hasFolderIDs && inferredType == seriesContentType && agg.root.Title == "") ||
		(agg.contradictionCount > 0 && agg.root.ObservedFileCount == 1)
}

func applyRootOverride(root *models.ScannedMediaRoot, override models.MediaRootOverride) {
	root.OverrideSource = rootOverrideManual
	root.State = rootStateResolved
	if override.ForcedType != "" {
		root.InferredType = override.ForcedType
	}
	if override.ForcedTitle != "" {
		root.Title = override.ForcedTitle
	}
	if override.ForcedYear != 0 {
		root.Year = override.ForcedYear
	}
	if override.ForcedTmdbID != "" {
		root.TmdbID = override.ForcedTmdbID
	}
	if override.ForcedImdbID != "" {
		root.ImdbID = override.ForcedImdbID
	}
	if override.ForcedTvdbID != "" {
		root.TvdbID = override.ForcedTvdbID
	}
}

package naming

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

var (
	inferTitleYearRe       = regexp.MustCompile(`^(.+?)\s*\((\d{4})\)`)
	inferWhitespaceTokenRe = regexp.MustCompile(`\s+`)
	inferReleaseTokenRe    = regexp.MustCompile(`(?i)\b(?:remux|bluray|bdrip|brrip|web[ ._-]?dl|webrip|hdr|dv|2160p|1080p|720p|x264|x265|h\.?264|h\.?265|hevc|av1|aac|dts|truehd|atmos)\b`)
	// Matches a well-formed tag ([tvdb-81189]), an unsubstituted Sonarr token
	// ([tvdb-{TvdbId}]), or an empty token ({imdb-}). The id part is either a
	// {...} placeholder or zero-or-more word chars.
	inferProviderTagRe = regexp.MustCompile(`(?i)\s*[{\[](?:tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)[-=](?:\{[^}]*\}|[\w]*)[}\]]`)
)

type RootAssignment struct {
	FilePath               string
	RootPath               string
	LibraryRootPath        string
	InferredType           string
	Title                  string
	Year                   int
	LegacyRootPath         string
	LegacyType             string
	HasFolderIDs           bool
	HasSeasonStructure     bool
	HasMovieEvidence       bool
	HasEpisodePattern      bool
	WrapperCollapsed       bool
	PromotedAncestor       bool
	HasStrongContradiction bool
}

func InferRootAssignments(
	filePaths []string,
	libraryType string,
	folderID int,
	overrides map[string]models.MediaRootOverride,
	libraryRoots ...string,
) ([]models.ScannedMediaRoot, map[string]RootAssignment) {
	assignments := make(map[string]RootAssignment, len(filePaths))
	if len(filePaths) == 0 {
		return []models.ScannedMediaRoot{}, assignments
	}

	type aggregate struct {
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

	byRoot := make(map[string]*aggregate, len(filePaths))
	for _, rawPath := range filePaths {
		cleanFilePath := filepath.Clean(rawPath)
		assignment := inferFileRootAssignment(cleanFilePath, libraryType, overrides, libraryRoots...)
		assignments[cleanFilePath] = assignment

		agg, found := byRoot[assignment.RootPath]
		if !found {
			agg = &aggregate{
				root: models.ScannedMediaRoot{
					MediaFolderID:     folderID,
					RootPath:          assignment.RootPath,
					State:             "resolved",
					InferredType:      assignment.InferredType,
					TypeConfidence:    "low",
					ObservedFileCount: 0,
					SampleFilePath:    cleanFilePath,
					OverrideSource:    "none",
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
			byRoot[assignment.RootPath] = agg
		}

		agg.root.ObservedFileCount++
		agg.hasFolderIDs = agg.hasFolderIDs || assignment.HasFolderIDs
		if assignment.WrapperCollapsed {
			agg.wrapperCollapses++
		}
		if assignment.PromotedAncestor {
			agg.ancestorPromotions++
		}
		if assignment.LegacyRootPath != "" && assignment.LegacyRootPath != assignment.RootPath {
			agg.legacyDisagreements++
		}
		if assignment.HasSeasonStructure {
			agg.seasonEvidenceCount++
		}
		if assignment.HasEpisodePattern {
			agg.episodeEvidenceCount++
		}
		if assignment.HasMovieEvidence {
			agg.movieEvidenceCount++
		}
		if assignment.HasStrongContradiction {
			agg.contradictionCount++
		}
		if inferReleaseTokenRe.MatchString(filepath.Base(assignment.RootPath)) {
			agg.releaseDensityCount++
		}
		switch assignment.InferredType {
		case "series":
			agg.seriesTypeVotes++
		default:
			agg.movieTypeVotes++
		}
		if agg.root.Title == "" && assignment.Title != "" {
			agg.root.Title = assignment.Title
		}
		if agg.root.Year == 0 && assignment.Year != 0 {
			agg.root.Year = assignment.Year
		}
	}

	roots := make([]string, 0, len(byRoot))
	for rootPath := range byRoot {
		roots = append(roots, rootPath)
	}
	sort.Strings(roots)

	snapshots := make([]models.ScannedMediaRoot, 0, len(roots))
	for _, rootPath := range roots {
		agg := byRoot[rootPath]

		switch {
		case agg.seriesTypeVotes > 0 && agg.movieTypeVotes == 0:
			agg.root.InferredType = "series"
		case agg.movieTypeVotes > 0 && agg.seriesTypeVotes == 0:
			agg.root.InferredType = "movie"
		case agg.seasonEvidenceCount > 0 || agg.episodeEvidenceCount > agg.movieEvidenceCount:
			agg.root.InferredType = "series"
		default:
			agg.root.InferredType = "movie"
		}

		switch {
		case agg.hasFolderIDs || agg.seasonEvidenceCount > 0 || agg.movieEvidenceCount > 0:
			agg.root.TypeConfidence = "high"
		case agg.root.Title != "" || agg.root.Year != 0 || agg.episodeEvidenceCount > 0 || agg.root.ObservedFileCount > 1:
			agg.root.TypeConfidence = "medium"
		default:
			agg.root.TypeConfidence = "low"
		}

		conflictingTypeVotes := agg.seriesTypeVotes > 0 && agg.movieTypeVotes > 0 &&
			(agg.seasonEvidenceCount > 0 || agg.episodeEvidenceCount > 0) &&
			agg.movieEvidenceCount > 0
		if conflictingTypeVotes ||
			(!agg.hasFolderIDs && agg.root.TypeConfidence == "low" && agg.root.Title == "") ||
			(!agg.hasFolderIDs && agg.root.InferredType == seriesContentType && agg.root.Title == "") ||
			(agg.contradictionCount > 0 && agg.root.ObservedFileCount == 1) {
			agg.root.State = "ambiguous"
		}

		if override, ok := overrides[rootPath]; ok {
			agg.root.OverrideSource = "manual"
			agg.root.State = "resolved"
			if override.ForcedType != "" {
				agg.root.InferredType = override.ForcedType
			}
			if override.ForcedTitle != "" {
				agg.root.Title = override.ForcedTitle
			}
			if override.ForcedYear != 0 {
				agg.root.Year = override.ForcedYear
			}
			if override.ForcedTmdbID != "" {
				agg.root.TmdbID = override.ForcedTmdbID
			}
			if override.ForcedImdbID != "" {
				agg.root.ImdbID = override.ForcedImdbID
			}
			if override.ForcedTvdbID != "" {
				agg.root.TvdbID = override.ForcedTvdbID
			}
		}

		evidence, _ := json.Marshal(map[string]any{
			"has_folder_ids":         agg.hasFolderIDs,
			"season_structure_files": agg.seasonEvidenceCount,
			"episode_pattern_files":  agg.episodeEvidenceCount,
			"movie_evidence_files":   agg.movieEvidenceCount,
			"release_density_files":  agg.releaseDensityCount,
			"wrapper_collapses":      agg.wrapperCollapses,
			"ancestor_promotions":    agg.ancestorPromotions,
			"legacy_disagreements":   agg.legacyDisagreements,
			"movie_type_votes":       agg.movieTypeVotes,
			"series_type_votes":      agg.seriesTypeVotes,
			"override_applied":       agg.root.OverrideSource == "manual",
			"observed_file_count":    agg.root.ObservedFileCount,
			"contradiction_files":    agg.contradictionCount,
		})
		agg.root.EvidenceJSON = evidence
		snapshots = append(snapshots, agg.root)
	}

	return snapshots, assignments
}

func inferFileRootAssignment(
	filePath string,
	libraryType string,
	overrides map[string]models.MediaRootOverride,
	libraryRoots ...string,
) RootAssignment {
	assignment := extractPathEvidence(filePath, libraryType, libraryRoots...)

	if overrideRoot, ok := deepestOverrideAncestor(filePath, overrides); ok {
		assignment.RootPath = overrideRoot
		assignment.PromotedAncestor = overrideRoot != assignment.LegacyRootPath
	}

	promotedRoot, wrapperCollapsed, promotedAncestor := promoteCandidateRoot(
		filePath,
		assignment.RootPath,
		assignment.Title,
		assignment.Year,
		assignment.LibraryRootPath,
	)
	assignment.RootPath = promotedRoot
	assignment.WrapperCollapsed = wrapperCollapsed
	assignment.PromotedAncestor = assignment.PromotedAncestor || promotedAncestor

	if (assignment.Title == "" || assignment.Year == 0) && assignment.RootPath != filepath.Clean(filePath) && assignment.RootPath != assignment.LibraryRootPath {
		rootName := filepath.Base(assignment.RootPath)
		rootTitle, rootYear := parseTitleYearCandidate(rootName)
		if assignment.InferredType == "movie" {
			if stem := parseInferMovieStem(rootName, assignment.Title, assignment.Year); stem.Title != "" {
				rootTitle, rootYear = stem.Title, stem.Year
			}
		}
		if assignment.Title == "" {
			assignment.Title = rootTitle
		}
		if assignment.Year == 0 {
			assignment.Year = rootYear
		}
	}

	if ids := ParseFolderIDs(filepath.Base(assignment.RootPath)); ids != nil && assignment.RootPath != assignment.LibraryRootPath {
		assignment.HasFolderIDs = true
	}

	return assignment
}

func extractPathEvidence(filePath string, libraryType string, libraryRoots ...string) RootAssignment {
	cleanFilePath := filepath.Clean(filePath)
	baseName := filepath.Base(cleanFilePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parentDir := filepath.Dir(cleanFilePath)
	parentBase := filepath.Base(parentDir)
	libraryRoot := deepestContainingLibraryRoot(cleanFilePath, libraryRoots)
	dirParts := directorySegmentsWithinRoot(cleanFilePath, libraryRoot)

	seriesLibrary := normalizeInferLibraryType(libraryType) == seriesContentType
	allowNumericSeason := seriesLibrary && (libraryRoot == "" || len(dirParts) > 1)
	_, hasEpisodePattern := parseEpisodeToken(nameNoExt, dirParts, allowNumericSeason, seriesLibrary)
	hasSeasonStructure, _ := detectSeasonStructure(dirParts, hasEpisodePattern || seriesLibrary, libraryRoot != "")
	parentTitle, parentYear, parentTrusted := parseInferFolderTitleYear(parentBase)
	if parentDir == libraryRoot {
		parentTitle, parentYear, parentTrusted = "", 0, false
	}
	fileStem := parseInferMovieStem(nameNoExt, parentTitle, parentYear)
	hasMovieEvidence := parentDir != libraryRoot && detectInferMovieFolderEvidence(parentBase, nameNoExt, hasSeasonStructure)
	strongMovieContradiction := false
	// A title inferred from an undated release is weaker evidence than a
	// trusted movie folder. Only a dated filename can contradict its title.
	if !hasSeasonStructure && parentTrusted && fileStem.Year != 0 && fileStem.Title != "" && !inferTitlesCoherent(parentTitle, fileStem.Title) {
		strongMovieContradiction = true
	}
	if !hasSeasonStructure && parentTrusted && hasEpisodePattern {
		strongMovieContradiction = true
	}

	inferredType := normalizeInferLibraryType(libraryType)
	switch inferredType {
	case "series":
		inferredType = "series"
	case "movie":
		inferredType = "movie"
	default:
		switch {
		case hasSeasonStructure:
			inferredType = "series"
		case hasMovieEvidence:
			inferredType = "movie"
		case hasEpisodePattern:
			inferredType = "series"
		default:
			inferredType = "movie"
		}
	}

	rootPath := parentDir
	var seriesContext *PathContext
	if inferredType == "series" {
		seriesContext = ResolvePathContext(cleanFilePath, libraryType, libraryRoot)
		rootPath = seriesContext.RootPath
	} else {
		rootPath = deriveInferMovieRootPath(cleanFilePath, hasMovieEvidence || parentTrusted)
	}

	title, year := parseInferTitleYear(filepath.Base(rootPath))
	if inferredType == seriesContentType {
		title, year = parseTitleYearCandidate(filepath.Base(rootPath))
	}
	if inferredType == "movie" && parentTrusted {
		title = parentTitle
		year = parentYear
	} else if inferredType == "movie" && fileStem.Title != "" {
		title = fileStem.Title
		year = fileStem.Year
	}
	if title == "" || year == 0 {
		fileTitle, fileYear := parseInferTitleYear(nameNoExt)
		if inferredType == seriesContentType {
			fileTitle, fileYear = parseTitleYearCandidate(nameNoExt)
		} else if fileStem.Title != "" {
			fileTitle, fileYear = fileStem.Title, fileStem.Year
		}
		if title == "" {
			title = fileTitle
		}
		if year == 0 {
			year = fileYear
		}
	}
	if seriesContext != nil {
		title, year = seriesContext.Title, seriesContext.Year
	}

	return RootAssignment{
		FilePath:               cleanFilePath,
		LibraryRootPath:        libraryRoot,
		RootPath:               filepath.Clean(rootPath),
		InferredType:           inferredType,
		Title:                  title,
		Year:                   year,
		LegacyRootPath:         filepath.Clean(rootPath),
		LegacyType:             inferredType,
		HasSeasonStructure:     hasSeasonStructure,
		HasMovieEvidence:       hasMovieEvidence,
		HasEpisodePattern:      hasEpisodePattern,
		HasStrongContradiction: strongMovieContradiction,
	}
}

func normalizeInferLibraryType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie", "movies":
		return "movie"
	case "series", "tv", "show", "tvshows":
		return "series"
	default:
		return ""
	}
}

func detectInferSeasonStructure(parts []string, allowNumeric bool) bool {
	found, _ := detectSeasonStructure(parts, allowNumeric)
	return found
}

// IsMisplacedSeriesFile reports whether filePath is a TV episode that lives
// inside an explicit "Season NN/" (or "Specials"/"Extras") directory. Such a
// file is a series structure dropped into a movie-type library (e.g. a fan
// "supercuts" pack): a movie library would otherwise turn every episode into a
// bogus per-episode "Season NN" movie that never matches a movie provider.
//
// The check requires both a recognized episode token in the
// file name AND an explicit season/specials ancestor directory — so legitimate
// movies that merely carry an episode-like substring in their release name
// (e.g. "...S01E43..." inside a "Title (Year)/" folder) are never flagged.
// Directories at or above a configured library root (such as an /mnt/s3 mount)
// are not season directories.
func IsMisplacedSeriesFile(filePath string, libraryRoots ...string) bool {
	clean := filepath.Clean(filePath)
	baseName := filepath.Base(clean)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parts := directorySegmentsWithinRoot(clean, deepestContainingLibraryRoot(clean, libraryRoots))
	if _, ok := parseEpisodeToken(nameNoExt, parts, false); !ok {
		return false
	}
	for i, part := range parts {
		parent := ""
		if i > 0 {
			parent = parts[i-1]
		}
		if _, ok := seasonDirectoryNumber(part, parent, false); ok {
			return true
		}
	}
	return false
}

func detectInferMovieFolderEvidence(parentBase string, nameNoExt string, hasSeasonStructure bool) bool {
	if hasSeasonStructure {
		return false
	}
	if ParseFolderIDs(parentBase) != nil {
		return true
	}
	parentTitle, parentYear, trusted := parseInferFolderTitleYear(parentBase)
	if parentTitle == "" || (!trusted && parentYear == 0) {
		return false
	}
	fileStem := parseInferMovieStem(nameNoExt, parentTitle, parentYear)
	if fileStem.Title == "" {
		return false
	}
	if !inferTitlesCoherent(parentTitle, fileStem.Title) {
		return false
	}
	if parentYear != 0 && fileStem.Year != 0 && parentYear != fileStem.Year {
		return true
	}
	return true
}

func deriveInferMovieRootPath(filePath string, hasMovieEvidence bool) string {
	parentDir := filepath.Dir(filePath)
	if hasMovieEvidence {
		return parentDir
	}
	baseName := filepath.Base(filePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	return filepath.Join(parentDir, nameNoExt)
}

func deepestOverrideAncestor(
	filePath string,
	overrides map[string]models.MediaRootOverride,
) (string, bool) {
	if len(overrides) == 0 {
		return "", false
	}

	cleanFilePath := filepath.Clean(filePath)
	longest := ""
	for rootPath := range overrides {
		cleanRoot := filepath.Clean(rootPath)
		rel, err := filepath.Rel(cleanRoot, cleanFilePath)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(cleanRoot) > len(longest) {
			longest = cleanRoot
		}
	}
	if longest == "" {
		return "", false
	}
	return longest, true
}

func promoteCandidateRoot(filePath, currentRoot, title string, year int, libraryRoot string) (string, bool, bool) {
	cleanRoot := filepath.Clean(currentRoot)
	wrapperCollapsed := false
	promotedAncestor := false

	for {
		parent := filepath.Dir(cleanRoot)
		if parent == "." || parent == "/" || parent == cleanRoot || parent == "" || parent == libraryRoot {
			break
		}

		parentBase := filepath.Base(parent)
		currentBase := filepath.Base(cleanRoot)
		parentIDs := ParseFolderIDs(parentBase) != nil
		childTitle, childYear := parseInferTitleYear(currentBase)
		parentTitle, parentYear := parseInferTitleYear(parentBase)

		sameNamedWrapper := sameInferRootIdentity(parentTitle, parentYear, childTitle, childYear)
		matchesParsedTitle := sameInferRootIdentity(parentTitle, parentYear, title, year)
		legacySyntheticChild := normalizeInferComparable(currentBase) == normalizeInferComparable(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))

		switch {
		case sameNamedWrapper:
			cleanRoot = parent
			wrapperCollapsed = true
			promotedAncestor = true
			continue
		case parentIDs && (matchesParsedTitle || legacySyntheticChild):
			cleanRoot = parent
			promotedAncestor = true
			continue
		default:
			return cleanRoot, wrapperCollapsed, promotedAncestor
		}
	}

	return cleanRoot, wrapperCollapsed, promotedAncestor
}

func sameInferRootIdentity(aTitle string, aYear int, bTitle string, bYear int) bool {
	if normalizeInferComparable(aTitle) == "" || normalizeInferComparable(bTitle) == "" {
		return false
	}
	if !inferTitlesCoherent(aTitle, bTitle) {
		return false
	}
	if aYear != 0 && bYear != 0 && aYear != bYear {
		return false
	}
	return true
}

func parseInferTitleYear(name string) (string, int) {
	surface := stripInferProviderTags(name)
	surface = inferWhitespaceTokenRe.ReplaceAllString(surface, " ")
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return "", 0
	}
	if title, year, trusted := parseInferFolderTitleYear(surface); trusted {
		return title, year
	}
	if match := inferTitleYearRe.FindStringSubmatch(surface); match != nil {
		year := 0
		for _, r := range match[2] {
			year = year*10 + int(r-'0')
		}
		return strings.TrimSpace(match[1]), year
	}
	if stem := parseInferMovieStem(surface, "", 0); stem.Title != "" {
		return stem.Title, stem.Year
	}
	return normalizeNameSeparators(surface), 0
}

func stripInferProviderTags(name string) string {
	return strings.TrimSpace(inferProviderTagRe.ReplaceAllString(name, " "))
}

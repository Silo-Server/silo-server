package naming

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
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
	HasFileIDs             bool
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
	inference := NewRootInference(libraryType, folderID, overrides, libraryRoots...)
	for _, rawPath := range filePaths {
		assignments[filepath.Clean(rawPath)] = inference.Add(rawPath)
	}
	return inference.Snapshots(), assignments
}

func inferFileRootAssignment(
	filePath string,
	libraryType string,
	overrides map[string]models.MediaRootOverride,
	libraryRoots ...string,
) RootAssignment {
	assignment := extractDisplayPathEvidence(filePath, libraryType, libraryRoots...)

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
	// Recorded for every file: an override can still force the root's final
	// type, so callers apply the movie-only rule once that type is known.
	assignment.HasFileIDs = FileNameHasProviderTag(filePath)
	assignment.Title = fspath.Display(assignment.Title)

	return assignment
}

// extractDisplayPathEvidence parses names as display text (see
// fspath.Display), so the inferred type and title do not depend on whether a
// name was stored in UTF-8; the paths keep the input's encoding.
func extractDisplayPathEvidence(filePath string, libraryType string, libraryRoots ...string) RootAssignment {
	paths := newDisplayPaths(filePath, libraryRoots)
	if paths == nil {
		return extractPathEvidence(filePath, libraryType, libraryRoots...)
	}
	evidence := extractPathEvidence(paths.file, libraryType, paths.roots...)
	evidence.FilePath = paths.toEncoded(evidence.FilePath)
	evidence.LibraryRootPath = paths.toEncoded(evidence.LibraryRootPath)
	evidence.RootPath = paths.toEncoded(evidence.RootPath)
	evidence.LegacyRootPath = paths.toEncoded(evidence.LegacyRootPath)
	return evidence
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
	episode, hasEpisodePattern := parseEpisodeToken(nameNoExt, dirParts, libraryRoot != "", allowNumericSeason, seriesLibrary)
	hasSeasonStructure, _ := detectSeasonStructure(dirParts, nameNoExt, episode.seriesTitle, hasEpisodePattern || seriesLibrary, libraryRoot != "")
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

	var rootPath string
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
	libraryRoot := deepestContainingLibraryRoot(clean, libraryRoots)
	parts := directorySegmentsWithinRoot(clean, libraryRoot)
	if _, ok := parseEpisodeToken(nameNoExt, parts, libraryRoot != "", false); !ok {
		return false
	}
	for i, part := range parts {
		parent := ""
		if i > 0 {
			parent = parts[i-1]
		}
		topLevel := libraryRoot != "" && i == 0 && mayBeShowFolder(part, nameNoExt)
		if _, ok := seasonDirectoryNumber(part, parent, false, topLevel); ok {
			return true
		}
	}
	return false
}

func detectInferMovieFolderEvidence(parentBase string, nameNoExt string, hasSeasonStructure bool) bool {
	if hasSeasonStructure {
		return false
	}
	// A TMDB or IMDb tag is movie evidence by itself. A TVDB-only tag isn't:
	// Sonarr adds the show's TVDB ID to series folders, and TVDB lists series
	// and movies alike (#1642). In such a folder an episode token in the file
	// name decides, even in a release name that repeats the show's title and
	// year ("Show.2024.S01E03.1080p"), unless the folder's own title is shaped
	// like an episode code ("s01e03 (2020)"), which only a movie would carry.
	// Otherwise the folder falls through to the title check below.
	if hints := ParseFolderIDs(parentBase); hints != nil {
		if hints.TmdbID != "" || hints.ImdbID != "" {
			return true
		}
		if hasExplicitEpisodeToken(nameNoExt) && !hasExplicitEpisodeToken(stripInferProviderTags(parentBase)) {
			return false
		}
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

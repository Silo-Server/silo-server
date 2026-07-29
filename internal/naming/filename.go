package naming

import (
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// folderTagRe matches strict provider-ID tags in square, curly, or round
	// brackets, including Plex-style bare IMDb tags such as (tt0473100).
	folderTagRe = regexp.MustCompile(`(?i)\s*(?:\((?:(?:tmdb|tmdbid|tvdb|tvdbid)-\d+|(?:imdb|imdbid)-tt\d{7,10}|tt\d{7,10})\)|\[(?:(?:tmdb|tmdbid|tvdb|tvdbid)-\d+|(?:imdb|imdbid)-tt\d{7,10}|tt\d{7,10})\]|\{(?:(?:tmdb|tmdbid|tvdb|tvdbid)-\d+|(?:imdb|imdbid)-tt\d{7,10}|tt\d{7,10})\})`)

	// titleYearRe matches "Title (Year)" with optional trailing content.
	titleYearRe = regexp.MustCompile(`^(.+?)\s*\((\d{4})\)`)

	// seasonEpisodeRe matches S01E01 or s01e05 patterns in filenames.
	seasonEpisodeRe = regexp.MustCompile(`(?i)[Ss](\d{1,4})[Ee](\d{1,3})`)

	// airDateRe matches daily/by-date episode names using Jellyfin-style
	// separators: yyyy-MM-dd, yyyy.MM.dd, yyyy_MM_dd, or yyyy MM dd.
	airDateRe = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})[-._ ]([01]\d)[-._ ]([0-3]\d)(?:[^0-9]|$)`)

	// seasonDirRe matches "Season XX" directory names, optionally followed by
	// trailing text (e.g. "Season 01 - Arc 01 - Romance Dawn"). The season
	// number is captured in group 1.
	seasonDirRe = regexp.MustCompile(`(?i)^Season\s+(\d{1,4})(?:\s.*)?$`)

	// numericSeasonDirRe matches numeric-only season directories like "01".
	numericSeasonDirRe = regexp.MustCompile(`^\d{1,4}$`)

	// specialsDirRe matches common specials/extras folders.
	specialsDirRe = regexp.MustCompile(`(?i)^(?:specials?|extras?)$`)

	// seasonReleaseDirRe recognizes release-pack directories such as
	// "Show.Name.S01.2160p.WEB-DL-GROUP". These are season containers, not
	// show roots; treating them as roots fragments one show into one metadata
	// item per release directory and searches providers with the release name.
	seasonReleaseDirRe  = regexp.MustCompile(`(?i)(?:^|[ ._-])s\d{1,4}(?:[ ._-]|$)`)
	seasonReleaseTechRe = regexp.MustCompile(`(?i)(?:^|[ ._-])(?:2160p|1080p|720p|576p|480p|web(?:[ ._-]?dl|rip)?|blu[ ._-]?ray|bluray|hdtv|remux|x26[45]|h26[45]|hevc)(?:[ ._-]|$)`)
)

// ResolvePathContext classifies a media path using both naming heuristics and
// the declared library type. libraryType accepts values like "movies",
// "movie", "series", "tv", "show", or "mixed".
func ResolvePathContext(filePath string, libraryType string) *PathContext {
	normalized := filepath.ToSlash(filePath)
	if normalized == "" {
		return &PathContext{}
	}

	baseName := path.Base(normalized)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parts := strings.Split(normalized, "/")
	parentDir := path.Dir(normalized)
	parentBase := path.Base(parentDir)
	normalizedLibraryType := normalizeLibraryType(libraryType)

	ctx := &PathContext{}

	parsedSeason := 0
	parsedEpisode := 0
	parsedAirDate := ""
	if m := seasonEpisodeRe.FindStringSubmatch(nameNoExt); m != nil {
		parsedSeason, _ = strconv.Atoi(m[1])
		parsedEpisode, _ = strconv.Atoi(m[2])
		ctx.HasEpisodePattern = true
	}
	if airDate, ok := parseAirDate(nameNoExt); ok {
		parsedAirDate = airDate
	}

	allowNumericSeasonDirs := ctx.HasEpisodePattern || normalizedLibraryType == "series"
	ctx.HasSeasonStructure, _ = detectSeasonStructure(parts[:max(len(parts)-1, 0)], allowNumericSeasonDirs)
	ctx.HasMovieFolderEvidence = detectMovieFolderEvidence(parentBase, nameNoExt, ctx.HasSeasonStructure)

	switch normalizedLibraryType {
	case "movie":
		ctx.Type = "movie"
	case "series":
		ctx.Type = "series"
	default:
		switch {
		case ctx.HasSeasonStructure:
			ctx.Type = "series"
		case ctx.HasMovieFolderEvidence:
			ctx.Type = "movie"
		case ctx.HasEpisodePattern:
			ctx.Type = "series"
		default:
			ctx.Type = "movie"
		}
	}

	if ctx.Type == "series" {
		if parsedAirDate != "" {
			ctx.AirDate = parsedAirDate
			ctx.HasAirDatePattern = true
		}

		if root, ok := deriveSeriesRoot(normalized, ctx.HasEpisodePattern, normalizedLibraryType == "series"); ok {
			ctx.RootPath = root.RootPath
			ctx.Title, ctx.Year = parseTitleYearCandidate(root.FolderName)
		} else if parentDir != "." && parentDir != "/" && parentDir != "" {
			ctx.RootPath = parentDir
			ctx.Title, ctx.Year = parseTitleYearCandidate(parentBase)
		}

		if ctx.HasEpisodePattern {
			ctx.SeasonNum = parsedSeason
			ctx.EpisodeNum = parsedEpisode
		} else if seasonNum, ok := firstSeasonNumber(parts[:max(len(parts)-1, 0)], allowNumericSeasonDirs); ok {
			ctx.SeasonNum = seasonNum
		}

		return ctx
	}

	if root, ok := deriveMovieRoot(normalized); ok {
		ctx.RootPath = root.RootPath
	}
	ctx.Title, ctx.Year = extractMovieTitleYear(normalized)

	return ctx
}

// ParseFilename extracts metadata hints from a media file path.
// folderType accepts either item-style values ("movie"/"series") or
// library-style values ("movies"/"mixed"/"tv").
func ParseFilename(filePath string, folderType string) *FilenameHints {
	ctx := ResolvePathContext(filePath, folderType)
	if ctx == nil {
		return &FilenameHints{}
	}

	return &FilenameHints{
		Title:      ctx.Title,
		Year:       ctx.Year,
		Type:       ctx.Type,
		SeasonNum:  ctx.SeasonNum,
		EpisodeNum: ctx.EpisodeNum,
		AirDate:    ctx.AirDate,
	}
}

// SeriesRoot describes a recognized show root folder for episodic content.
type SeriesRoot struct {
	RootPath   string
	FolderName string
}

// DetectSeriesRoot derives the show root from a file path when the layout
// clearly looks like episodic TV content in the given library context.
func DetectSeriesRoot(filePath string, libraryType string) (*SeriesRoot, bool) {
	ctx := ResolvePathContext(filePath, libraryType)
	if ctx == nil || ctx.Type != "series" || ctx.RootPath == "" {
		return nil, false
	}

	return &SeriesRoot{
		RootPath:   ctx.RootPath,
		FolderName: path.Base(ctx.RootPath),
	}, true
}

// stripFolderTags removes bracketed provider-ID tags (e.g. [tmdbid-27205],
// {tvdb-81189}) from a folder or file name so they don't leak into titles.
func stripFolderTags(name string) string {
	return strings.TrimSpace(folderTagRe.ReplaceAllString(name, ""))
}

func isSeasonDirSegment(segment string, allowNumeric bool) bool {
	if seasonDirRe.MatchString(segment) {
		return true
	}
	return allowNumeric && numericSeasonDirRe.MatchString(segment)
}

// DetectCanonicalRoot returns the canonical content root for a media file
// path using the given library context.
func DetectCanonicalRoot(filePath string, libraryType string) (*CanonicalRoot, bool) {
	ctx := ResolvePathContext(filePath, libraryType)
	if ctx == nil || ctx.RootPath == "" || ctx.Type == "" {
		return nil, false
	}

	return &CanonicalRoot{
		RootPath: ctx.RootPath,
		Type:     ctx.Type,
	}, true
}

func normalizeLibraryType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movie", "movies":
		return "movie"
	case "series", "tv", "show", "tvshows":
		return "series"
	default:
		return ""
	}
}

func parseAirDate(name string) (string, bool) {
	m := airDateRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	candidate := m[1] + "-" + m[2] + "-" + m[3]
	if _, err := time.Parse("2006-01-02", candidate); err != nil {
		return "", false
	}
	return candidate, true
}

func detectSeasonStructure(parts []string, allowNumeric bool) (bool, int) {
	for _, part := range parts {
		if m := seasonDirRe.FindStringSubmatch(part); m != nil {
			seasonNum, _ := strconv.Atoi(m[1])
			return true, seasonNum
		}
		if numericSeasonDirRe.MatchString(part) && allowNumeric {
			seasonNum, _ := strconv.Atoi(part)
			return true, seasonNum
		}
		if specialsDirRe.MatchString(part) {
			return true, 0
		}
	}
	return false, 0
}

func firstSeasonNumber(parts []string, allowNumeric bool) (int, bool) {
	_, seasonNum := detectSeasonStructure(parts, allowNumeric)
	if seasonNum == 0 {
		for _, part := range parts {
			if specialsDirRe.MatchString(part) {
				return 0, true
			}
		}
	}
	for _, part := range parts {
		if m := seasonDirRe.FindStringSubmatch(part); m != nil {
			seasonNum, _ := strconv.Atoi(m[1])
			return seasonNum, true
		}
		if numericSeasonDirRe.MatchString(part) && allowNumeric {
			seasonNum, _ := strconv.Atoi(part)
			return seasonNum, true
		}
	}
	return 0, false
}

func detectMovieFolderEvidence(parentBase string, nameNoExt string, hasSeasonStructure bool) bool {
	if hasSeasonStructure {
		return false
	}

	if hasExplicitFolderIDs(parentBase) {
		return true
	}

	parentComparable := normalizeComparableTitle(parentBase)
	fileComparable := normalizeComparableTitle(nameNoExt)
	if parentComparable == "" || fileComparable == "" {
		return false
	}

	parentTitle, parentYear := parseTitleYearCandidate(parentBase)
	if parentTitle == "" || parentYear == 0 {
		return false
	}

	return comparableTitlesOverlap(fileComparable, parentComparable)
}

func hasExplicitFolderIDs(name string) bool {
	return ParseStructuredFolderIDs(name) != nil
}

func parseTitleYearCandidate(name string) (string, int) {
	candidate := strings.TrimSpace(stripFolderTags(name))
	candidate = collapseWhitespace(candidate)
	if candidate == "" {
		return "", 0
	}

	if m := titleYearRe.FindStringSubmatch(candidate); m != nil {
		year, _ := strconv.Atoi(m[2])
		return strings.TrimSpace(m[1]), year
	}

	return candidate, 0
}

func normalizeComparableTitle(name string) string {
	candidate := stripFolderTags(name)
	candidate = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(candidate)
	candidate = collapseWhitespace(strings.TrimSpace(candidate))
	title, _ := parseTitleYearCandidate(candidate)
	if title == "" {
		title = candidate
	}
	return strings.ToLower(collapseWhitespace(strings.TrimSpace(title)))
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func deriveSeriesRoot(filePath string, hasEpisodePattern bool, forceParent bool) (*SeriesRoot, bool) {
	parentDir := path.Dir(filePath)
	if isSeasonReleaseDirectoryForParent(path.Base(parentDir), path.Base(path.Dir(parentDir))) {
		rootPath := path.Dir(parentDir)
		if rootPath != "." && rootPath != "/" && rootPath != "" {
			return &SeriesRoot{RootPath: rootPath, FolderName: path.Base(rootPath)}, true
		}
	}
	for current := parentDir; current != "." && current != "/" && current != ""; current = path.Dir(current) {
		segment := path.Base(current)
		if isSeasonDirSegment(segment, hasEpisodePattern || forceParent) || specialsDirRe.MatchString(segment) {
			rootPath := path.Dir(current)
			if rootPath == "." || rootPath == "/" || rootPath == "" {
				return nil, false
			}
			return &SeriesRoot{
				RootPath:   rootPath,
				FolderName: path.Base(rootPath),
			}, true
		}
	}

	if hasEpisodePattern || forceParent {
		if parentDir == "." || parentDir == "/" || parentDir == "" {
			return nil, false
		}
		return &SeriesRoot{
			RootPath:   parentDir,
			FolderName: path.Base(parentDir),
		}, true
	}

	return nil, false
}

func isSeasonReleaseDirectory(segment string) bool {
	return seasonReleaseDirRe.MatchString(segment) && seasonReleaseTechRe.MatchString(segment)
}

func isSeasonReleaseDirectoryForParent(segment, parentSegment string) bool {
	if !isSeasonReleaseDirectory(segment) {
		return false
	}
	location := seasonReleaseDirRe.FindStringIndex(segment)
	if location == nil || location[0] == 0 {
		return false
	}
	releaseTitle := strings.Trim(segment[:location[0]], " ._-")
	parentTitle, _ := parseTitleYearCandidate(parentSegment)
	if parentTitle == "" {
		parentTitle = parentSegment
	}
	return inferTitlesCoherent(releaseTitle, parentTitle)
}

func deriveMovieRoot(filePath string) (*CanonicalRoot, bool) {
	baseName := path.Base(filePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parentDir := path.Dir(filePath)
	if parentDir == "." || parentDir == "/" || parentDir == "" {
		return nil, false
	}

	parentBase := path.Base(parentDir)
	parentComparable := normalizeComparableTitle(parentBase)
	fileComparable := normalizeComparableTitle(nameNoExt)
	_, parentYear := parseTitleYearCandidate(parentBase)

	if hasExplicitFolderIDs(parentBase) || parentYear > 0 || comparableTitlesOverlap(fileComparable, parentComparable) {
		return &CanonicalRoot{
			RootPath: parentDir,
			Type:     "movie",
		}, true
	}

	return &CanonicalRoot{
		RootPath: path.Join(parentDir, nameNoExt),
		Type:     "movie",
	}, true
}

func extractMovieTitleYear(filePath string) (string, int) {
	baseName := path.Base(filePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parentBase := path.Base(path.Dir(filePath))

	source := nameNoExt
	if title, year := parseTitleYearCandidate(parentBase); title != "" && (year > 0 || hasExplicitFolderIDs(parentBase)) {
		return title, year
	}

	return parseTitleYearCandidate(source)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func comparableTitlesOverlap(left string, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return strings.HasPrefix(left, right) || strings.HasPrefix(right, left)
}

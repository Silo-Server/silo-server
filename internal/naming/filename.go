package naming

import (
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxEpisodeNumberDigits bounds the episode numbers this package reports.
// Absolute-numbered shows reach four digits (One Piece S23E1162), so five
// leaves room to spare; a longer run is not an episode number and must not be
// handed to the catalog, whose episode columns are 32-bit.
const maxEpisodeNumberDigits = 5

const seriesContentType = "series"

var (
	// folderTagRe matches strict provider-ID tags in square, curly, or round
	// brackets, including Plex-style bare IMDb tags such as (tt0473100).
	folderTagRe = regexp.MustCompile(`(?i)\s*(?:\((?:(?:tmdb|tmdbid|tvdb|tvdbid)[-=]\d+|(?:imdb|imdbid)[-=]tt\d{7,10}|tt\d{7,10})\)|\[(?:(?:tmdb|tmdbid|tvdb|tvdbid)[-=]\d+|(?:imdb|imdbid)[-=]tt\d{7,10}|tt\d{7,10})\]|\{(?:(?:tmdb|tmdbid|tvdb|tvdbid)[-=]\d+|(?:imdb|imdbid)[-=]tt\d{7,10}|tt\d{7,10})\})`)

	// airDateRe matches daily/by-date episode names using Jellyfin-style
	// separators: yyyy-MM-dd, yyyy.MM.dd, yyyy_MM_dd, or yyyy MM dd.
	airDateRe = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})[-._ ]([01]\d)[-._ ]([0-3]\d)(?:[^0-9]|$)`)

	// numericSeasonDirRe matches numeric-only season directories like "01".
	numericSeasonDirRe = regexp.MustCompile(`^\d{1,4}$`)

	// specialsDirRe matches common specials/extras folders.
	specialsDirRe = regexp.MustCompile(`(?i)^(?:specials?|extras?)$`)

	// seasonReleaseDirRe recognizes release-pack directories such as
	// "Show.Name.S01.2160p.WEB-DL-GROUP". These are season containers, not
	// show roots; treating them as roots fragments one show into one metadata
	// item per release directory and searches providers with the release name.
	seasonReleaseDirRe = regexp.MustCompile(`(?i)(?:^|[ ._-])s\d{1,4}(?:[ ._-]|$)`)
)

// ResolvePathContext classifies a media path using both naming heuristics and
// the declared library type. libraryType accepts values like "movies",
// "movie", "series", "tv", "show", or "mixed".
func ResolvePathContext(filePath string, libraryType string, libraryRoots ...string) *PathContext {
	normalized := filepath.ToSlash(filePath)
	if normalized == "" {
		return &PathContext{}
	}

	baseName := path.Base(normalized)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	libraryRoot := deepestContainingLibraryRoot(normalized, libraryRoots)
	directories := directorySegmentsWithinRoot(normalized, libraryRoot)
	parentDir := path.Dir(normalized)
	parentBase := path.Base(parentDir)
	normalizedLibraryType := normalizeLibraryType(libraryType)

	ctx := &PathContext{LibraryRootPath: libraryRoot}

	parsedAirDate := ""
	allowNumericSeason := normalizedLibraryType == seriesContentType && (libraryRoot == "" || len(directories) > 1)
	episode, hasEpisode := parseEpisodeToken(nameNoExt, directories, allowNumericSeason, normalizedLibraryType == seriesContentType)
	ctx.HasEpisodePattern = hasEpisode
	if airDate, ok := parseAirDate(nameNoExt); ok {
		parsedAirDate = airDate
	}

	allowNumericSeasonDirs := ctx.HasEpisodePattern || normalizedLibraryType == "series"
	ctx.HasSeasonStructure, _ = detectSeasonStructure(directories, allowNumericSeasonDirs, libraryRoot != "")
	ctx.HasMovieFolderEvidence = filepath.Clean(parentDir) != libraryRoot && detectInferMovieFolderEvidence(parentBase, nameNoExt, ctx.HasSeasonStructure)

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
		rootSeason, rootSeasonKnown := 0, false
		if parsedAirDate != "" {
			ctx.AirDate = parsedAirDate
			ctx.HasAirDatePattern = true
		}

		if root, ok := deriveSeriesRoot(normalized, ctx.HasEpisodePattern, normalizedLibraryType == "series", libraryRoot); ok {
			ctx.RootPath = root.RootPath
			if filepath.Clean(ctx.RootPath) != libraryRoot {
				ctx.Title, ctx.Year, rootSeason, rootSeasonKnown = parseSeriesFolderIdentity(root.FolderName)
			}
		} else if parentDir != "." && parentDir != "/" && parentDir != "" {
			ctx.RootPath = parentDir
			ctx.Title, ctx.Year = parseTitleYearCandidate(parentBase)
		}

		if ctx.HasEpisodePattern {
			ctx.SeasonNum = episode.season
			ctx.SeasonKnown = episode.seasonKnown
			ctx.EpisodeNum = episode.episode
			if !ctx.SeasonKnown && rootSeasonKnown && path.Dir(normalized) == ctx.RootPath {
				ctx.SeasonNum, ctx.SeasonKnown = rootSeason, true
			}
			applyFilenameSeriesIdentity(normalized, episode.seriesTitle, libraryRoot, ctx)
		} else if seasonNum, ok := firstSeasonNumber(directories, allowNumericSeasonDirs, libraryRoot != ""); ok {
			ctx.SeasonNum = seasonNum
			ctx.SeasonKnown = true
		}
		if ctx.HasAirDatePattern && !ctx.HasEpisodePattern {
			if location := airDateRe.FindStringIndex(nameNoExt); location != nil {
				applyFilenameSeriesIdentity(normalized, cleanEpisodeSeriesTitle(nameNoExt[:location[0]]), libraryRoot, ctx)
			}
		}

		if ctx.RootPath == libraryRoot && ParseFolderIDs(nameNoExt) != nil {
			ctx.RootPath = normalized
		}
		return ctx
	}

	if root, ok := deriveMovieRoot(normalized, libraryRoot); ok {
		ctx.RootPath = root.RootPath
	}
	ctx.Title, ctx.Year = extractMovieTitleYear(normalized, libraryRoot)

	return ctx
}

// ParseFilename extracts metadata hints from a media file path.
// folderType accepts either item-style values ("movie"/"series") or
// library-style values ("movies"/"mixed"/"tv").
func ParseFilename(filePath string, folderType string, libraryRoots ...string) *FilenameHints {
	ctx := ResolvePathContext(filePath, folderType, libraryRoots...)
	if ctx == nil {
		return &FilenameHints{}
	}

	return &FilenameHints{
		Title:       ctx.Title,
		Year:        ctx.Year,
		Type:        ctx.Type,
		SeasonNum:   ctx.SeasonNum,
		SeasonKnown: ctx.SeasonKnown,
		EpisodeNum:  ctx.EpisodeNum,
		AirDate:     ctx.AirDate,
	}
}

// applyFilenameSeriesIdentity lets named episodes identify loose series files.
// A real season structure or an explicitly identified show folder remains
// authoritative; a shared downloads directory cannot identify every show in it.
func applyFilenameSeriesIdentity(filePath, filenameTitle, libraryRoot string, ctx *PathContext) {
	title, year := parseTitleYearCandidate(filenameTitle)
	atLibraryRoot := libraryRoot != "" && filepath.Clean(ctx.RootPath) == libraryRoot
	filenameOnly := path.Dir(filePath) == "." || path.Dir(filePath) == "/"
	// A release filename can add a bare year to an otherwise matching folder
	// title. Require that corroboration before treating a title's number as a year.
	if stem := parseInferMovieStem(filenameTitle, ctx.Title, ctx.Year); !atLibraryRoot && stem.Year != 0 && inferTitlesCoherent(stem.Title, ctx.Title) {
		title, year = stem.Title, stem.Year
	}
	// A coherent dated filename can fill a missing folder year without
	// changing the folder's identity or its explicit provider authority.
	if !atLibraryRoot && ctx.Year == 0 && year != 0 && seriesTitlesShareYear(ctx.Title, title) {
		ctx.Year = year
	}
	if title == "" || (!atLibraryRoot && (ctx.HasSeasonStructure || ParseFolderIDs(path.Base(ctx.RootPath)) != nil || ctx.Year != 0)) {
		return
	}
	if !atLibraryRoot && inferTitlesCoherent(ctx.Title, title) {
		if ctx.Year == 0 {
			ctx.Year = year
		}
		return
	}
	folderName := path.Base(ctx.RootPath)
	// Season packs and per-episode release directories still own their files.
	// Their release details should not become provider search titles.
	folderEpisode, hasFolderEpisode := parseEpisodeToken(folderName, nil, false)
	location := seasonReleaseDirRe.FindStringIndex(folderName)
	if !atLibraryRoot && ((hasFolderEpisode && inferTitlesCoherent(folderEpisode.seriesTitle, title)) ||
		(location != nil && inferTitlesCoherent(strings.Trim(folderName[:location[0]], " ._-"), title))) {
		ctx.Title, ctx.Year = title, year
		return
	}
	if !atLibraryRoot && !filenameOnly {
		return
	}
	ctx.Title, ctx.Year = title, year
	// Root-based queue jobs relink all files sharing an observed root. A flat
	// folder needs a file root so matching one series cannot claim its neighbors.
	ctx.RootPath = filePath
}

// SeriesRoot describes a recognized show root folder for episodic content.
type SeriesRoot struct {
	RootPath   string
	FolderName string
}

// DetectSeriesRoot derives the show root from a file path when the layout
// clearly looks like episodic TV content in the given library context.
func DetectSeriesRoot(filePath string, libraryType string, libraryRoots ...string) (*SeriesRoot, bool) {
	ctx := ResolvePathContext(filePath, libraryType, libraryRoots...)
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

// DetectCanonicalRoot returns the canonical content root for a media file
// path using the given library context.
func DetectCanonicalRoot(filePath string, libraryType string, libraryRoots ...string) (*CanonicalRoot, bool) {
	ctx := ResolvePathContext(filePath, libraryType, libraryRoots...)
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

func detectSeasonStructure(parts []string, allowNumeric bool, configuredRoot ...bool) (bool, int) {
	for i := len(parts) - 1; i >= 0; i-- {
		parent := ""
		if i > 0 {
			parent = parts[i-1]
		}
		numericSeason := allowNumeric && (i != 0 || len(configuredRoot) == 0 || !configuredRoot[0])
		if number, ok := seasonDirectoryNumber(parts[i], parent, numericSeason); ok {
			return true, number
		}
	}
	return false, 0
}

func firstSeasonNumber(parts []string, allowNumeric bool, configuredRoot ...bool) (int, bool) {
	found, seasonNum := detectSeasonStructure(parts, allowNumeric, configuredRoot...)
	return seasonNum, found
}

func hasExplicitFolderIDs(name string) bool {
	return ParseStructuredFolderIDs(name) != nil
}

func parseTitleYearCandidate(name string) (string, int) {
	candidate := normalizeNameSeparators(strings.TrimSpace(stripFolderTags(name)))
	if match := inferBracketTitleYearRe.FindStringSubmatch(candidate); match != nil {
		year, _ := strconv.Atoi(match[2])
		return strings.TrimSpace(match[1]), year
	}
	// Series names can end in a number (Space 1999). A bare number in a
	// folder title is not enough evidence to remove it as a release year.
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

func deriveSeriesRoot(filePath string, hasEpisodePattern bool, forceParent bool, libraryRoot string) (*SeriesRoot, bool) {
	parentDir := path.Dir(filePath)
	for current := parentDir; current != "." && current != "/" && current != ""; current = path.Dir(current) {
		if libraryRoot != "" && filepath.Clean(current) == libraryRoot {
			break
		}
		segment := path.Base(current)
		allowNumeric := (hasEpisodePattern || forceParent) && (libraryRoot == "" || filepath.Clean(path.Dir(current)) != libraryRoot)
		if _, ok := seasonDirectoryNumber(segment, path.Base(path.Dir(current)), allowNumeric); ok {
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

func deriveMovieRoot(filePath, libraryRoot string) (*CanonicalRoot, bool) {
	baseName := path.Base(filePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parentDir := path.Dir(filePath)
	if parentDir == "." || parentDir == "/" || parentDir == "" {
		return nil, false
	}

	if filepath.Clean(parentDir) == libraryRoot {
		return &CanonicalRoot{RootPath: path.Join(parentDir, nameNoExt), Type: "movie"}, true
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

func extractMovieTitleYear(filePath, libraryRoot string) (string, int) {
	baseName := path.Base(filePath)
	nameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	parentBase := path.Base(path.Dir(filePath))

	parentTitle, parentYear, trusted := parseInferFolderTitleYear(stripFolderTags(parentBase))
	if filepath.Clean(path.Dir(filePath)) == libraryRoot {
		parentTitle, parentYear, trusted = "", 0, false
	}
	if parentTitle != "" && (trusted || hasExplicitFolderIDs(parentBase)) {
		return parentTitle, parentYear
	}
	if stem := parseInferMovieStem(nameNoExt, parentTitle, parentYear); stem.Title != "" {
		return stem.Title, stem.Year
	}
	return parseTitleYearCandidate(nameNoExt)
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

// deepestContainingLibraryRoot uses configured paths as the boundary between
// library organization and media identity. Names alone cannot distinguish a
// show folder from a flat library containing several unrelated shows.
func deepestContainingLibraryRoot(filePath string, libraryRoots []string) string {
	deepest := ""
	for _, root := range libraryRoots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		relative, err := filepath.Rel(root, filepath.Clean(filePath))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if deepest == "" || len(root) > len(deepest) {
			deepest = root
		}
	}
	return deepest
}

func directorySegmentsWithinRoot(filePath, libraryRoot string) []string {
	parent := filepath.Dir(filePath)
	if libraryRoot != "" {
		if relative, err := filepath.Rel(libraryRoot, parent); err == nil {
			if relative == "." {
				return nil
			}
			return strings.Split(filepath.ToSlash(relative), "/")
		}
	}
	return strings.Split(filepath.ToSlash(parent), "/")
}

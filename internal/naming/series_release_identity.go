package naming

import (
	"path/filepath"
	"strings"
)

// ParseSeriesReleaseYear supplies an alternate identity for loose episodes
// whose title ends in a bare release year. The complete title stays primary:
// a number in names such as Space 1999 must first be searched as part of the title.
func ParseSeriesReleaseYear(filePath string, libraryRoots ...string) (string, int, bool) {
	ctx := ResolvePathContext(filePath, seriesContentType, libraryRoots...)
	if ctx.Year != 0 || !ctx.HasEpisodePattern || !ctx.SeasonKnown ||
		filepath.Clean(ctx.RootPath) != filepath.Clean(filePath) {
		return "", 0, false
	}
	separator := strings.LastIndexByte(ctx.Title, ' ')
	if separator < 0 {
		return "", 0, false
	}
	year, ok := parseInferYearToken(ctx.Title[separator+1:])
	title := strings.TrimRight(ctx.Title[:separator], " -_")
	if !ok || title == "" {
		return "", 0, false
	}
	return title, year, true
}

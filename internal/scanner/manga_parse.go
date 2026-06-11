package scanner

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// mangaSeriesWhitespace collapses any run of whitespace to a single space so a
// series name keys identically regardless of incidental spacing.
var mangaSeriesWhitespace = regexp.MustCompile(`\s+`)

// mangaTrailingParen matches a single trailing parenthetical group, allowing
// optional whitespace before it.  Applied repeatedly to strip all trailing
// groups (year, year-range, "Digital", release-group names, etc.).
var mangaTrailingParen = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// cleanMangaSeriesName removes all trailing parenthetical groups (scene-release
// metadata such as years, "Digital", and release-group tags) from a manga
// folder name, then trims any dangling whitespace or trailing " -".
//
// Parentheticals in the middle of the name are left untouched so titles like
// "JoJo's Bizarre Adventure - Part 8 - JoJolion (something) extra" are
// preserved.  The function is pure and idempotent.  If stripping would produce
// an empty string the original trimmed input is returned unchanged so a series
// name is never empty.
func cleanMangaSeriesName(name string) string {
	s := strings.TrimSpace(name)
	for {
		stripped := mangaTrailingParen.ReplaceAllString(s, "")
		if stripped == s {
			break
		}
		s = stripped
	}
	// Trim any trailing dash (with optional surrounding spaces) left after
	// stripping, e.g. "Series Name - (Digital)" → "Series Name -" → "Series Name".
	s = strings.TrimRight(s, " -")
	s = strings.TrimSpace(s)
	if s == "" {
		return strings.TrimSpace(name)
	}
	return s
}

// mangaSeriesGroupKey is the stable, library-scoped content-group key that all
// chapters of one series resolve their series item by. It lowercases, trims,
// and collapses internal whitespace so cosmetic variations of the same folder
// name yield the same key. Returns "" for an empty name (caller must skip).
func mangaSeriesGroupKey(folderID int, name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.TrimSpace(mangaSeriesWhitespace.ReplaceAllString(normalized, " "))
	if normalized == "" {
		return ""
	}
	return fmt.Sprintf("manga:series:%d:%s", folderID, normalized)
}

// mangaVolumeFolder matches directory names that are volume markers, not series.
var mangaVolumeFolder = regexp.MustCompile(`(?i)^v(?:ol(?:ume)?\.?)?\s*\d+$`)

var (
	mangaVolYearIssue = regexp.MustCompile(`(?i)\b(Vol\.?\s*\d{4})\b.*?#\s*(\d+(?:\.\d+)?)`)
	mangaVolYearLabel = regexp.MustCompile(`(?i)\bvol\.?\s*\d{4}\b`) // strip a year-style "Vol.YYYY" so it never reads as an index
	// mangaVolume / mangaChapterC only match the abbreviated forms (v13, vol.4, c128, ch.5).
	// Full English words ("volume 3", "chapter 5") intentionally fall through to the bare-number path.
	mangaVolume     = regexp.MustCompile(`(?i)\bv(?:ol\.?)?\s*(\d+(?:\.\d+)?)\b`)
	mangaChapterC   = regexp.MustCompile(`(?i)\bc(?:h\.?)?\s*(\d+(?:\.\d+)?)\b`)
	mangaBareNumber = regexp.MustCompile(`\b(\d+(?:\.\d+)?)\b`)
	mangaParenNoise = regexp.MustCompile(`\([^)]*\)`) // (year) (Digital) (group) (Month, Year)
)

// mangaSeriesFromPath returns the series name: the nearest ancestor directory of
// the file whose name is not a volume marker.
func mangaSeriesFromPath(filePath string) string {
	dir := filepath.Dir(filePath)
	for dir != "" && dir != "." && dir != string(filepath.Separator) {
		base := filepath.Base(dir)
		if !mangaVolumeFolder.MatchString(strings.TrimSpace(base)) {
			return cleanMangaSeriesName(base)
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// mangaIndexForFile parses the volume/chapter index from a manga file's base
// name (extension already stripped), first removing the series-name prefix so
// numbers inside the series title (e.g. "404 Demons", "365 Days") are not
// mistaken for the chapter/volume number. Falls back to the full base name when
// the file does not start with the series name.
func mangaIndexForFile(base, seriesName string) (volume string, index float64, has bool) {
	trimmedBase := strings.TrimSpace(base)
	trimmedSeries := strings.TrimSpace(seriesName)
	if trimmedSeries != "" && strings.HasPrefix(strings.ToLower(trimmedBase), strings.ToLower(trimmedSeries)) {
		remainder := trimmedBase[len(trimmedSeries):]
		return parseMangaIndex(remainder)
	}
	return parseMangaIndex(trimmedBase)
}

// parseMangaIndex extracts the ordering index (volume or chapter number) and the
// raw volume token from a manga release filename (extension already stripped).
// Returns has=false when no number is present (e.g. a one-shot).
//
// The returned volume is a display token (e.g. "v13" or "Vol.2003") and is not
// normalized across forms; callers should treat it as label text, not a key.
func parseMangaIndex(name string) (volume string, index float64, has bool) {
	if m := mangaVolYearIssue.FindStringSubmatch(name); m != nil {
		if n, err := strconv.ParseFloat(m[2], 64); err == nil {
			return "v" + strings.TrimSpace(m[2]), n, true
		}
	}
	clean := strings.TrimSpace(mangaParenNoise.ReplaceAllString(name, " "))
	// A bare "Vol.YYYY" (no "#issue") is a year, not an index — strip it so it
	// never leaks into the volume/chapter/bare-number scans below.
	clean = mangaVolYearLabel.ReplaceAllString(clean, " ")
	if m := mangaVolume.FindStringSubmatch(clean); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			return "v" + m[1], n, true
		}
	}
	if m := mangaChapterC.FindStringSubmatch(clean); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			return "", n, true
		}
	}
	if m := mangaBareNumber.FindStringSubmatch(clean); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			return "", n, true
		}
	}
	return "", 0, false
}

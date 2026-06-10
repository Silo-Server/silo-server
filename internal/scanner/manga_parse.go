package scanner

import (
	"regexp"
	"strconv"
	"strings"
)

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

// parseMangaIndex extracts the ordering index (volume or chapter number) and the
// raw volume token from a manga release filename (extension already stripped).
// Returns has=false when no number is present (e.g. a one-shot).
//
// The returned volume is a display token (e.g. "v13" or "Vol.2003") and is not
// normalized across forms; callers should treat it as label text, not a key.
func parseMangaIndex(name string) (volume string, index float64, has bool) {
	if m := mangaVolYearIssue.FindStringSubmatch(name); m != nil {
		if n, err := strconv.ParseFloat(m[2], 64); err == nil {
			return strings.TrimSpace(m[1]), n, true
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

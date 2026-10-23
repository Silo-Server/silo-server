package titleutil

import (
	"strconv"
	"strings"
	"unicode"
)

// IsProvisionalEpisodeTitle reports whether title is a provider placeholder
// ("TBA"/"TBD") that stands in until the real episode title is announced.
func IsProvisionalEpisodeTitle(title string) bool {
	title = strings.TrimSpace(title)
	return strings.EqualFold(title, "tba") || strings.EqualFold(title, "tbd")
}

// IsNumberedEpisodeTitle reports whether title is only a word and the
// episode's number, such as "Episode 3", "Folge 3", "Avsnitt 3", or "3.
// Bölüm": the stand-in TMDB returns in the requested language when it has no
// title for the episode in that language.
func IsNumberedEpisodeTitle(title string, number int) bool {
	fields := strings.Fields(title)
	if len(fields) != 2 {
		return false
	}
	// The number comes after the word in most languages and before it,
	// with a period, in some ("3. Bölüm").
	word := fields[0]
	if n, err := strconv.Atoi(strings.TrimSuffix(fields[0], ".")); err == nil && n == number {
		word = fields[1]
	} else if n, err := strconv.Atoi(strings.TrimPrefix(fields[1], "#")); err != nil || n != number {
		return false
	}
	for _, r := range strings.TrimSuffix(word, ".") {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return word != ""
}

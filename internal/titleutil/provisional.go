package titleutil

import (
	"strconv"
	"strings"
)

// IsProvisionalEpisodeTitle reports whether title is a provider placeholder
// ("TBA"/"TBD") that stands in until the real episode title is announced.
func IsProvisionalEpisodeTitle(title string) bool {
	title = strings.TrimSpace(title)
	return strings.EqualFold(title, "tba") || strings.EqualFold(title, "tbd")
}

// numberedEpisodeWords are the words TMDB writes before the episode number
// when it has no title for an episode in a language: "Episode 3" in English,
// "Folge 3" in German, "Avsnitt 3" in Swedish, and so on.
var numberedEpisodeWords = map[string]bool{
	"episode": true, "folge": true, "avsnitt": true, "épisode": true, "episodio": true,
	"episódio": true, "aflevering": true, "odcinek": true, "эпизод": true, "серия": true,
	"серія": true, "jakso": true, "afsnit": true, "episodul": true, "επεισόδιο": true,
	"פרק": true, "الحلقة": true, "епизод": true, "epizoda": true, "epizóda": true, "قسمت": true,
}

// numberedEpisodeSuffixes are the words TMDB writes after "<number>." in the
// languages that put the number first ("3. Bölüm", "3. epizoda").
var numberedEpisodeSuffixes = map[string]bool{"bölüm": true, "epizoda": true, "epizód": true}

// IsNumberedEpisodeTitle reports whether title is TMDB's stand-in for an
// episode with no title in the requested language, such as "Episode 3",
// "Avsnitt 3", "3. Bölüm", or "第 3 集", or Silo's own "Episode 3". A real
// title that happens to look similar, such as "Day 3", is not one.
func IsNumberedEpisodeTitle(title string, number int) bool {
	title = strings.ToLower(strings.TrimSpace(strings.Map(dropBidiMark, title)))
	n := strconv.Itoa(number)
	switch title {
	case "第 " + n + " 集", "第" + n + "集", "第" + n + "話", n + "화":
		return true
	}
	fields := strings.Fields(title)
	if len(fields) != 2 {
		return false
	}
	if numberedEpisodeWords[fields[0]] && sameNumber(fields[1], number) {
		return true
	}
	return numberedEpisodeSuffixes[fields[1]] && strings.HasSuffix(fields[0], ".") && sameNumber(strings.TrimSuffix(fields[0], "."), number)
}

func sameNumber(text string, number int) bool {
	n, err := strconv.Atoi(text)
	return err == nil && n == number
}

// dropBidiMark removes the direction marks TMDB wraps some right-to-left
// titles in.
func dropBidiMark(r rune) rune {
	if r == '\u200e' || r == '\u200f' || (r >= '\u202a' && r <= '\u202e') {
		return -1
	}
	return r
}

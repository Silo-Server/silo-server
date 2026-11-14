package scanner

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxAudiobookCreditsPerRole and maxAudiobookCreditNameLen bound what one
// author or narrator tag can write: each credit is a person lookup or insert,
// and a tag is file content anyone with write access to a library controls.
const (
	maxAudiobookCreditsPerRole = 20
	maxAudiobookCreditNameLen  = 200
)

// splitAudiobookCredits splits an author or narrator tag that names several
// people into one name per person. Taggers join names with ";", "&", " and ",
// or ",". Commas and conjunctions also occur inside one name ("King,
// Stephen", "Martin Luther King, Jr.", "Simon & Schuster"), so those split
// only when every piece has at least two words, as a full personal name
// does. Semicolons always split. Names are deduplicated case-insensitively,
// keeping the first spelling, and capped at maxAudiobookCreditsPerRole names
// of at most maxAudiobookCreditNameLen bytes.
func splitAudiobookCredits(raw string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, group := range strings.Split(raw, ";") {
		for _, joined := range splitCreditPieces(group, audiobookCreditConjunctionRE) {
			for _, name := range splitCreditPieces(joined, audiobookCreditCommaRE) {
				name = collapseSpacesRE.ReplaceAllString(strings.TrimSpace(name), " ")
				key := strings.ToLower(name)
				if name == "" || len(name) > maxAudiobookCreditNameLen || seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, name)
				if len(out) == maxAudiobookCreditsPerRole {
					return out
				}
			}
		}
	}
	return out
}

var (
	audiobookCreditConjunctionRE = regexp.MustCompile(`(?i)\s+(?:&|and)\s+`)
	audiobookCreditCommaRE       = regexp.MustCompile(`\s*,\s*`)
)

// splitCreditPieces splits s on sep only when every piece is a multi-word name.
func splitCreditPieces(s string, sep *regexp.Regexp) []string {
	pieces := sep.Split(strings.TrimSpace(s), -1)
	if len(pieces) < 2 {
		return []string{s}
	}
	for _, p := range pieces {
		if len(strings.Fields(p)) < 2 {
			return []string{s}
		}
	}
	return pieces
}

// audiobookSeriesPositionPrefixRE matches the words taggers put before a
// series position: "Book 3", "#3", "Vol. 3", "Volume 3", "No. 3", "Part 3".
var audiobookSeriesPositionPrefixRE = regexp.MustCompile(`(?i)^(?:#|(?:book|bk|volume|vol|number|no|part|pt)\b\.?)\s*#?\s*`)

// audiobookGroupingSeriesRE reads a series and position from a grouping tag
// such as "Series Name #2", "Series Name, Book 2", or "Series Name Vol. 2".
var audiobookGroupingSeriesRE = regexp.MustCompile(`(?i)^(.*?\S)(?:\s*,\s*|\s+)((?:#|(?:book|bk|volume|vol|number|no|part|pt)\b\.?\s*#?)\s*(?:\d+(?:\.\d+)?|[ivxlc]+))\s*$`)

// parseGroupingSeries reads a series from the grouping tag. Grouping holds
// other things too (works, collections, store categories), so it counts as a
// series only when it ends in a recognizable position. When several series
// are listed ("A #1; B #3"), the first one is used.
func parseGroupingSeries(grouping string) (name, position string) {
	first, _, _ := strings.Cut(grouping, ";")
	m := audiobookGroupingSeriesRE.FindStringSubmatch(strings.TrimSpace(first))
	if m == nil {
		return "", ""
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
}

// parseRomanNumeral parses a standalone Roman numeral up to 399, as used for
// series positions ("III"). It returns 0 when s is not a well-formed numeral.
func parseRomanNumeral(s string) int {
	s = strings.ToUpper(s)
	if s == "" || strings.Trim(s, "IVXLC") != "" {
		return 0
	}
	values := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100}
	total := 0
	for i := 0; i < len(s); i++ {
		v := values[s[i]]
		if i+1 < len(s) && v < values[s[i+1]] {
			total -= v
		} else {
			total += v
		}
	}
	if total <= 0 || total >= 400 || formatRomanNumeral(total) != s {
		return 0
	}
	return total
}

func formatRomanNumeral(n int) string {
	steps := []struct {
		value  int
		symbol string
	}{{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}
	var b strings.Builder
	for _, step := range steps {
		for n >= step.value {
			b.WriteString(step.symbol)
			n -= step.value
		}
	}
	return b.String()
}

// parseGenresFromTags pulls genres from the `genre` tag plus tmp_Genre1..5
// sub-tags written by some audiobook taggers. Audible-style values join a
// category path with "/" and its names contain commas ("Mystery, Thriller &
// Suspense/Suspense"), so when a value holds "/" or ";" only those split it,
// as Audiobookshelf does. A value with neither splits on ",". Returns a
// deduplicated list in input order.
func parseGenresFromTags(tags map[string]string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(g string) {
		g = strings.TrimSpace(g)
		if g == "" {
			return
		}
		key := strings.ToLower(g)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, g)
	}
	if raw := tags["genre"]; raw != "" {
		separators := "/;"
		if !strings.ContainsAny(raw, separators) {
			separators = ","
		}
		for _, g := range strings.FieldsFunc(raw, func(r rune) bool { return strings.ContainsRune(separators, r) }) {
			add(g)
		}
	}
	for i := 1; i <= 5; i++ {
		if v := tags["tmp_genre"+strconv.Itoa(i)]; v != "" {
			add(v)
		}
	}
	return out
}

// audiobookTrackTitleRE matches a per-part title that names the part rather
// than the book: "Chapter 3", "Track 03", "Part 2", "Disc 1", "07".
var audiobookTrackTitleRE = regexp.MustCompile(`(?i)^\s*(?:(?:chapter|chap|track|part|pt|disc|cd)\.?\s*\d+\b|\d+\s*$)`)

// partTitlesNameParts reports whether the parts' title tags name the parts
// rather than the book: the present ones differ from part to part, or the
// first one reads like a track title. A part without a title tag says
// nothing either way.
func partTitlesNameParts(titles []string) bool {
	var present []string
	for _, t := range titles {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			present = append(present, t)
		}
	}
	if len(present) == 0 {
		return false
	}
	for _, t := range present[1:] {
		if t != present[0] {
			return true
		}
	}
	return audiobookTrackTitleRE.MatchString(present[0])
}

// parseTagPosition reads the leading number of a track or disc tag ("3",
// "3/12"). It returns 0 when the tag is absent or not a positive number.
func parseTagPosition(raw string) int {
	return parseTrackNumber(strings.TrimSpace(raw))
}

// audiobookPartOrder orders parts by disc then track tag when the tags can
// be trusted: every part has a track number, either every part or no part
// has a disc number, and no two parts share a disc and track. Otherwise it
// returns false and the caller keeps the natural filename order.
func audiobookPartOrder(tags []map[string]string) ([]int, bool) {
	if len(tags) < 2 {
		return nil, false
	}
	type key struct{ disc, track int }
	keys := make([]key, len(tags))
	seen := make(map[key]bool, len(tags))
	withDisc := 0
	for i, t := range tags {
		k := key{disc: parseTagPosition(t["disc"]), track: parseTagPosition(t["track"])}
		if k.track == 0 || seen[k] {
			return nil, false
		}
		if k.disc > 0 {
			withDisc++
		}
		seen[k] = true
		keys[i] = k
	}
	if withDisc != 0 && withDisc != len(tags) {
		return nil, false
	}
	order := make([]int, len(tags))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ka, kb := keys[order[a]], keys[order[b]]
		if ka.disc != kb.disc {
			return ka.disc < kb.disc
		}
		return ka.track < kb.track
	})
	return order, true
}

// rejectedAudiobookParts remembers audio files ffprobe rejected as damaged,
// by path, size, and modification time, for the life of the process. A book
// is indexed without such a part; remembering it keeps every later scan from
// probing the same bytes again and from treating the book as changed because
// the part has no media_files row. Replacing the file changes its size or
// time, so it is probed again. A restart forgets the list, which costs one
// more probe of each rejected part.
var rejectedAudiobookParts = struct {
	sync.Mutex
	byPath map[string]rejectedAudiobookPart
}{byPath: make(map[string]rejectedAudiobookPart)}

type rejectedAudiobookPart struct {
	size    int64
	modTime time.Time
}

func rememberRejectedAudiobookPart(path string, size int64, modTime time.Time) {
	rejectedAudiobookParts.Lock()
	defer rejectedAudiobookParts.Unlock()
	rejectedAudiobookParts.byPath[path] = rejectedAudiobookPart{size: size, modTime: modTime}
}

func audiobookPartRejected(path string, size int64, modTime time.Time) bool {
	rejectedAudiobookParts.Lock()
	part, ok := rejectedAudiobookParts.byPath[path]
	rejectedAudiobookParts.Unlock()
	return ok && part.size == size && sameFileModifiedAt(&part.modTime, modTime)
}

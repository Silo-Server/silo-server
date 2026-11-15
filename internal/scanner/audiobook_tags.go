package scanner

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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

// Reading an audiobook's tags
//
// Taggers that export from Audible (inAudible, OpenAudible, Libation) write
// tags the plain "first non-empty tag" rule reads wrongly. Three rules cover
// them; docs/architecture/audiobook-metadata.md describes them for readers.
//
//   - Title (audiobookTagTitle): the title tag, except that the album tag wins
//     when the title is the album behind a series prefix ("Saga 2 - Book" with
//     album "Book") or the album is the title plus a subtitle ("Book" with
//     album "Book: A Subtitle"). The prefix is the book's series tag with an
//     optional position or, for a book without one, text ending in a number.
//     A "subtitle" naming the series or ending in a position is not one.
//     Unrelated tags keep the title, since a single file's album may name its
//     series or a collection.
//   - Narrator (audiobookTagNarrators): an explicit narrator tag (narrator,
//     ©nrt, narratedby) when present. Otherwise the first of performer and
//     composer that names someone other than the tagged authors: some files
//     put the author in performer and the narrator in composer. When both
//     name only authors, the author read the book and performer is used.
//   - Genres (parseGenresFromTags): the tmp_genreN tags, one genre each,
//     when any is present; otherwise the genre tag.

// audiobookTitleKey is the form two title tags are compared in: narrator
// suffixes and "(Unabridged)" removed, lower case, single spaces.
func audiobookTitleKey(title string) string {
	return strings.ToLower(stripNarratorSuffix(title))
}

var (
	// audiobookSubtitleSeparatorRE matches the start of a subtitle after a
	// title: ": Subtitle" or " - Subtitle".
	audiobookSubtitleSeparatorRE = regexp.MustCompile(`^(?::|\s[-–—])\s*\S`)
	// audiobookPrefixSeparatorRE matches the separator ending a series
	// prefix before the book title: "Saga 2 - " or "Saga 2: ".
	audiobookPrefixSeparatorRE = regexp.MustCompile(`\s*(?::|\s[-–—])\s*$`)
	// audiobookNumberedPrefixRE matches a prefix of text ending in a series
	// number: "Saga Name 2", "Saga Name, Book 2", "Saga Name #2.5".
	audiobookNumberedPrefixRE = regexp.MustCompile(`(?i)^\S.*?[\s,]+(?:(?:book|bk|volume|vol|number|no|part|pt)\b\.?\s*)?#?\s*\d+(?:\.\d+)?$`)
	// audiobookSeriesPositionRE matches what may follow the series name in a
	// prefix: nothing, or a position in digits or Roman numerals.
	audiobookSeriesPositionRE = regexp.MustCompile(`(?i)^(?:[\s,]+(?:(?:book|bk|volume|vol|number|no|part|pt)\b\.?\s*)?#?\s*(\d+(?:\.\d+)?|[ivxlc]+))?$`)
	// audiobookTrailingPositionRE matches a series position ending a
	// subtitle: "..., Book 1", "... #3", "Vol. 2", "Book III", "(Book 1)".
	// A Roman numeral counts only after a marker word; endsInSeriesPosition
	// checks that it is a well-formed one.
	audiobookTrailingPositionRE = regexp.MustCompile(`(?i)(?:#\s*\d+(?:\.\d+)?|\b(?:book|bk|volume|vol|number|no|part|pt)\b\.?\s*#?\s*(?:\d+(?:\.\d+)?|([ivxlc]+)))\s*[)\]]?\s*$`)
)

// endsInSeriesPosition reports whether text ends in a series position, as
// audiobookTrailingPositionRE describes.
func endsInSeriesPosition(text string) bool {
	m := audiobookTrailingPositionRE.FindStringSubmatch(text)
	return m != nil && (m[1] == "" || parseRomanNumeral(m[1]) != 0)
}

// containsTitleWords reports whether phrase appears in text as whole words,
// ignoring case and punctuation. Words are runs of letters and digits in any
// script. An empty phrase is never contained.
func containsTitleWords(text, phrase string) bool {
	words := func(v string) string {
		return " " + strings.Join(strings.FieldsFunc(strings.ToLower(v), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}), " ") + " "
	}
	p := words(phrase)
	return strings.TrimSpace(p) != "" && strings.Contains(words(text), p)
}

// audiobookTagTitle picks a book's title from its title and album tags as
// the rules above describe. series is the book's series tag.
func audiobookTagTitle(title, album, series string) string {
	title, album = strings.TrimSpace(title), strings.TrimSpace(album)
	if title == "" || album == "" {
		return firstNonEmpty(title, album)
	}
	t, a := audiobookTitleKey(title), audiobookTitleKey(album)
	if t == "" || a == "" || t == a {
		return title
	}
	s := audiobookTitleKey(series)
	if rest, ok := strings.CutPrefix(a, t); ok && audiobookSubtitleSeparatorRE.MatchString(rest) {
		// An album that adds the series ("Book: Saga, Book 1") is not a
		// subtitle.
		if !containsTitleWords(rest, s) && !endsInSeriesPosition(rest) {
			return album
		}
		return title
	}
	prefix, ok := strings.CutSuffix(t, a)
	if !ok {
		return title
	}
	loc := audiobookPrefixSeparatorRE.FindStringIndex(prefix)
	if loc == nil {
		return title
	}
	prefix = strings.TrimSpace(prefix[:loc[0]])
	// With a series tag only that series counts as a prefix, so a number in
	// the work's own name ("Apollo 13 - ...") is kept.
	if s == "" {
		if audiobookNumberedPrefixRE.MatchString(prefix) {
			return album
		}
		return title
	}
	rest, ok := strings.CutPrefix(prefix, s)
	if !ok {
		return title
	}
	m := audiobookSeriesPositionRE.FindStringSubmatch(rest)
	if m == nil {
		return title
	}
	// The position regexp takes any run of Roman numeral letters; only a
	// well-formed numeral is a position ("Saga Civil" is not).
	if position := m[1]; position != "" && !strings.ContainsAny(position, "0123456789") && parseRomanNumeral(position) == 0 {
		return title
	}
	return album
}

// audiobookTagNarrators picks the narrators from the narrator sources as the
// rules above describe. authors are the book's tagged authors.
func audiobookTagNarrators(tags map[string]string, authors []string) []string {
	isAuthor := make(map[string]bool, len(authors))
	for _, a := range authors {
		isAuthor[strings.ToLower(a)] = true
	}
	//nolint:goconst // Tag keys are data, matching the names ffprobe reports.
	for _, key := range []string{"narrator", "narratedby"} {
		if names := splitAudiobookCredits(tags[key]); len(names) > 0 {
			return names
		}
	}
	var first []string
	//nolint:goconst // Tag keys are data, matching the names ffprobe reports.
	for _, key := range []string{"performer", "composer"} {
		names := splitAudiobookCredits(tags[key])
		if len(names) == 0 {
			continue
		}
		if first == nil {
			first = names
		}
		for _, n := range names {
			if !isAuthor[strings.ToLower(n)] {
				return names
			}
		}
	}
	return first
}

// maxAudiobookTmpGenres bounds the tmp_genreN tags read from one file.
const maxAudiobookTmpGenres = 20

// audibleCommaGenres are Audible category names that contain a comma. The
// genre tag of files exported from Audible joins categories with ", " too
// ("Mystery, Thriller & Suspense, Cozy"), so these names are taken out
// whole before the rest splits on commas.
//
//nolint:goconst // Genre names are data.
var audibleCommaGenres = []string{
	"Mystery, Thriller & Suspense",
	"Relationships, Parenting & Personal Development",
}

// parseGenresFromTags reads a book's genres. Taggers that export from Audible
// write one genre per tmp_genreN tag (tmp_genre1, tmp_genre2, ...), sometimes
// with a trailing ";"; when any is present they are the genres. Otherwise the
// genre tag is read. Audible-style values join a category path with "/" and
// its names contain commas ("Mystery, Thriller & Suspense/Suspense"), so when
// a value holds "/" or ";" only those split it, as Audiobookshelf does. A
// value with neither splits on ",", keeping the Audible names in
// audibleCommaGenres whole. Returns a deduplicated list in input order.
func parseGenresFromTags(tags map[string]string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(g string) {
		g = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(g), ";,"))
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
	for i := 1; i <= maxAudiobookTmpGenres; i++ {
		add(tags["tmp_genre"+strconv.Itoa(i)])
	}
	if len(out) > 0 {
		return out
	}
	raw := tags["genre"]
	if strings.ContainsAny(raw, "/;") {
		for _, g := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == ';' }) {
			add(g)
		}
		return out
	}
	for _, g := range splitCommaGenres(raw) {
		add(g)
	}
	return out
}

// joinGenrePieces rejoins comma pieces as ", "-separated trimmed names.
func joinGenrePieces(pieces []string) string {
	trimmed := make([]string, len(pieces))
	for i, p := range pieces {
		trimmed[i] = collapseSpacesRE.ReplaceAllString(strings.TrimSpace(p), " ")
	}
	return strings.Join(trimmed, ", ")
}

// splitCommaGenres splits a comma-joined genre tag, keeping each name in
// audibleCommaGenres whole where it appears between commas.
func splitCommaGenres(raw string) []string {
	pieces := strings.Split(raw, ",")
	var out []string
	for i := 0; i < len(pieces); i++ {
		joined := false
		for _, name := range audibleCommaGenres {
			n := strings.Count(name, ",") + 1
			if i+n <= len(pieces) && strings.EqualFold(joinGenrePieces(pieces[i:i+n]), name) {
				out = append(out, name)
				i += n - 1
				joined = true
				break
			}
		}
		if !joined {
			out = append(out, pieces[i])
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
// has a disc number, and no two parts share a disc and track. A part without
// a disc tag takes its disc folder's number from folderDiscs (0 outside a
// disc folder; nil when no part is in one), so complete tags win and a disc
// folder stands in only for a missing disc tag. Otherwise it returns false
// and the caller keeps the disc-folder then filename order.
func audiobookPartOrder(tags []map[string]string, folderDiscs []int) ([]int, bool) {
	if len(tags) < 2 {
		return nil, false
	}
	type key struct{ disc, track int }
	keys := make([]key, len(tags))
	seen := make(map[key]bool, len(tags))
	withDisc := 0
	for i, t := range tags {
		k := key{disc: parseTagPosition(t["disc"]), track: parseTagPosition(t["track"])}
		if k.disc == 0 && folderDiscs != nil {
			k.disc = folderDiscs[i]
		}
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
// more probe of each rejected part. The list holds at most
// maxRejectedAudiobookParts files, so files written into a library cannot
// grow it without bound; a full list drops an arbitrary entry, which only
// costs one more probe of that file.
const maxRejectedAudiobookParts = 4096

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
	byPath := rejectedAudiobookParts.byPath
	if _, known := byPath[path]; !known && len(byPath) >= maxRejectedAudiobookParts {
		for old := range byPath {
			delete(byPath, old)
			break
		}
	}
	byPath[path] = rejectedAudiobookPart{size: size, modTime: modTime}
}

func audiobookPartRejected(path string, size int64, modTime time.Time) bool {
	rejectedAudiobookParts.Lock()
	part, ok := rejectedAudiobookParts.byPath[path]
	rejectedAudiobookParts.Unlock()
	return ok && part.size == size && sameFileModifiedAt(&part.modTime, modTime)
}

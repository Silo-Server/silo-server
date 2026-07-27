package metadata

import (
	"regexp"
	"strconv"
	"strings"
)

// Search providers rank by their own relevance, not ours, and they answer
// almost every query with something. Enrichment used to take results[0]
// unconditionally, so a provider's best guess became the item's identity even
// when it was a different book. Probing 20 unidentified production audiobooks
// against iTunes, 19 came back with a result and roughly a quarter of those top
// hits were wrong -- a different volume of the right series, or an unrelated
// title that happened to share a common word. Enrichment stamps last_refreshed
// on success and never revisits the item, so each wrong acceptance is a
// permanent mislabel: someone else's cover, overview and narrator.
//
// BestMatch is the gate. It scores candidates against the title we actually
// have on disk and returns nothing when none is credible, which callers treat
// as "no match found" -- the same terminal-but-honest outcome as an empty
// result set.

const (
	// minTitleScore is the similarity a candidate must reach to be accepted.
	// Calibrated on the production sample in match_confidence_test.go: correct
	// matches there score 0.67 and above (differing only by decorations like
	// "(Unabridged)", a series parenthetical, or an author prefix), while the
	// wrong ones land at 0.29 and below. 0.5 sits in that gap with room on
	// both sides.
	minTitleScore = 0.5

	// minContainmentLen is the shortest normalised title allowed to match on
	// containment alone. "Bitcoin" is a substring of a great many audiobook
	// titles; requiring some length stops very short titles from matching
	// anything that happens to include them.
	//
	// Measured in bytes, not runes, and that is deliberate. For ASCII it is the
	// character count this was calibrated against. For multi-byte scripts it is
	// more permissive -- a four-character CJK title clears it -- which is the
	// behaviour we want, because a short CJK title is specific in a way that a
	// short English word like "Bitcoin" is not.
	minContainmentLen = 12
)

var (
	// Decorations providers append that say nothing about identity.
	editionNoiseRE = regexp.MustCompile(
		`(?i)\b(unabridged|abridged|audiobook|audio\s*book|dramatised|dramatized|` +
			`narrated\s+by|complete\s+edition|special\s+edition|anniversary\s+edition|` +
			`box\s*set|boxed\s*set|omnibus|light\s*novel)\b`)

	// A volume marker in any of the shapes providers and rippers use:
	// "Book 4", "Vol. 2", "#3", "Part 7", "Series 2", or a bare trailing number.
	volumeMarkerRE = regexp.MustCompile(
		`(?i)\b(?:book|bk|vol|volume|part|series|episode|ep)\b\.?\s*#?\s*(\d{1,4})\b`)
	hashVolumeRE = regexp.MustCompile(`#\s*(\d{1,4})\b`)

	// Punctuation and separators only. Deliberately NOT [^a-z0-9]: that is
	// ASCII-only, and this library is not. Stripping every non-ASCII rune
	// reduced "進撃の巨人" to the empty string, so an identical Japanese title
	// scored 0 against itself and was rejected outright, and accented Latin
	// titles were shredded ("Blåbærsyltetøy" -> "bl b rsyltet y"). That would
	// have been a hard regression for non-English content, which previously
	// matched by accident because nothing was checked at all.
	nonAlnumRE = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// normaliseTitle lowercases, strips edition decorations and punctuation, and
// collapses whitespace so two spellings of the same title compare equal.
//
// Note for scripts that do not space their words (CJK): the whole title
// normalises to a single token, so Dice gives 1 for an exact match and 0
// otherwise, and containment carries the near-misses. That is coarse but
// correct, and strictly better than the ASCII-only behaviour it replaces.
func normaliseTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = editionNoiseRE.ReplaceAllString(s, " ")
	s = nonAlnumRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// titleVolume extracts a volume number, preferring an explicit marker
// ("Book 4", "#3") over a bare trailing number. Returns ok=false when the
// title carries no volume at all, which is common and must not be treated as
// a disagreement.
func titleVolume(s string) (int, bool) {
	lower := strings.ToLower(s)

	if m := volumeMarkerRE.FindStringSubmatch(lower); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}
	if m := hashVolumeRE.FindStringSubmatch(lower); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}

	// A bare number standing as its own word, e.g. "Op-Center 4 - Acts of War"
	// or "Dungeon In My Closet 2". Years are excluded: they date an edition
	// rather than number a volume.
	fields := strings.Fields(nonAlnumRE.ReplaceAllString(lower, " "))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 || n > 999 {
			continue
		}
		if n >= 1000 || (n >= 1900 && n <= 2100) {
			continue
		}
		return n, true
	}
	return 0, false
}

// titleStopwords carry no identifying signal but are common enough to inflate
// overlap badly. Two unrelated boxed sets scored 0.56 -- over the threshold --
// on "the" (three times), "complete", "trilogy" and the volume numbers of a
// "Books 1-3" range. Dropping these takes that pair to 0.42, where it belongs.
var titleStopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "of": {}, "and": {}, "or": {}, "to": {},
	"in": {}, "on": {}, "at": {}, "for": {}, "with": {}, "from": {},
}

// contentWords drops stopwords, unless doing so would leave too little to
// compare -- "A Man in Full" is mostly stopwords, and an empty token set
// scores 0 against everything.
func contentWords(fields []string) []string {
	kept := make([]string, 0, len(fields))
	for _, w := range fields {
		if _, stop := titleStopwords[w]; !stop {
			kept = append(kept, w)
		}
	}
	if len(kept) < 2 {
		return fields
	}
	return kept
}

// diceCoefficient scores word-set overlap between two normalised titles.
// Chosen over edit distance because the differences that matter here are whole
// words added or dropped -- an author prefix, a series parenthetical, a
// subtitle -- not characters transposed.
func diceCoefficient(a, b string) float64 {
	aw, bw := contentWords(strings.Fields(a)), contentWords(strings.Fields(b))
	if len(aw) == 0 || len(bw) == 0 {
		return 0
	}

	counts := make(map[string]int, len(aw))
	for _, w := range aw {
		counts[w]++
	}
	overlap := 0
	for _, w := range bw {
		if counts[w] > 0 {
			counts[w]--
			overlap++
		}
	}
	return 2 * float64(overlap) / float64(len(aw)+len(bw))
}

// TitleScore rates how plausibly candidate names the same work as want, on a
// 0..1 scale. A volume disagreement returns 0 outright: "The OP MC 8" and
// "The OP MC, Book 1" share nearly every word but are different books, so word
// overlap alone cannot separate them.
func TitleScore(want, candidate string) float64 {
	w, c := normaliseTitle(want), normaliseTitle(candidate)
	if w == "" || c == "" {
		return 0
	}
	if w == c {
		return 1
	}

	if wv, wok := titleVolume(want); wok {
		if cv, cok := titleVolume(candidate); cok && wv != cv {
			return 0
		}
	}

	score := diceCoefficient(w, c)

	// One title fully containing the other is strong evidence: providers
	// routinely return "Title (Series Book 2)" for "Title", and our scanner
	// routinely has "Series 2 - Title" for "Title".
	// The floor applies to the *contained* title, not the containing one: a
	// long candidate does not make a short query specific.
	if shorter := min(len(w), len(c)); shorter >= minContainmentLen {
		if strings.Contains(w, c) || strings.Contains(c, w) {
			if containment := 0.9; containment > score {
				score = containment
			}
		}
	}
	return score
}

// BestMatch returns the highest-scoring credible candidate. ok is false when
// nothing clears the bar, which callers must treat as "no match" rather than
// falling back to results[0].
//
// want should be the title as it exists on disk, not a cleaned or truncated
// search query: the point is to check the answer against what we actually
// have.
func BestMatch(want string, results []SearchResult) (SearchResult, bool) {
	best, bestScore := SearchResult{}, 0.0
	found := false

	for _, r := range results {
		name := r.Name
		if strings.TrimSpace(name) == "" {
			name = r.OriginalTitle
		}
		score := TitleScore(want, name)

		// Aliases are provider-confirmed titles for the same work, so a
		// translated or regional spelling should not be penalised.
		for _, alias := range r.TitleAliases {
			if s := TitleScore(want, alias.Title); s > score {
				score = s
			}
		}

		if score > bestScore {
			best, bestScore, found = r, score, true
		}
	}

	if !found || bestScore < minTitleScore {
		return SearchResult{}, false
	}
	return best, true
}

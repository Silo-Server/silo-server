package metadata

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Title scoring alone cannot separate two different books that happen to share
// a title, and for books that is not a rare edge: generic titles recur
// constantly across authors. The obvious fix -- score the author at search time
// -- is not available: the plugin contract's SearchResult carries title, year,
// overview, image and provider IDs, and no author at all, so checking it there
// would mean changing the SDK proto and every plugin that implements it.
//
// It is available one step later. Enrichment calls GetMetadata on the accepted
// match, and MetadataResult.People carries the credits. So the author is
// verified after the fetch instead: if the item names an author and the fetched
// metadata names a different one, the match is rejected before anything is
// written.

// AuthorsAgree reports whether a fetched result's credited authors are
// consistent with the author already on the item.
//
// Absence is not disagreement. An item with no author, or a provider that
// returns no credits, yields true -- most of this library has one or the other
// missing, and treating that as a conflict would reject far more good matches
// than bad ones. Only a positive contradiction rejects.
func AuthorsAgree(itemAuthor string, people []models.ItemPerson) bool {
	want := normalisePersonName(itemAuthor)
	if want == "" {
		return true
	}

	var candidates []string
	for _, p := range people {
		if p.Kind != models.PersonKindAuthor {
			continue
		}
		if n := normalisePersonName(p.Name); n != "" {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 {
		return true
	}

	for _, got := range candidates {
		if personNamesMatch(want, got) {
			return true
		}
	}
	return false
}

// personNamesMatch compares two normalised names allowing for the forms the
// same person is credited under: "J.R.R. Tolkien" against "J. R. R. Tolkien"
// (punctuation already gone), "King, Stephen" against "Stephen King", and a
// middle name present on one side only.
func personNamesMatch(a, b string) bool {
	if a == b {
		return true
	}

	aw, bw := strings.Fields(a), strings.Fields(b)
	if len(aw) == 0 || len(bw) == 0 {
		return false
	}

	// Surname plus first initial is the strongest cheap signal: it survives
	// reordering, middle names, and initials-vs-full-first-name.
	aLast, bLast := aw[len(aw)-1], bw[len(bw)-1]
	if aLast == bLast && sharesInitial(aw[:len(aw)-1], bw[:len(bw)-1]) {
		return true
	}

	// "King, Stephen" normalises to "king stephen", so also try the reversal.
	if aw[0] == bLast && aLast == bw[0] {
		return true
	}

	// One name fully contained in the other, e.g. "Iain Banks" within
	// "Iain M Banks".
	return isSubsequence(aw, bw) || isSubsequence(bw, aw)
}

// sharesInitial reports whether the leading given-name tokens agree on their
// first letter. Empty on either side counts as agreement: a bare surname is
// consistent with any given name rather than in conflict with it.
func sharesInitial(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	return a[0][:1] == b[0][:1]
}

// isSubsequence reports whether every token of sub appears in order within all.
func isSubsequence(sub, all []string) bool {
	if len(sub) == 0 {
		return false
	}
	i := 0
	for _, w := range all {
		if i < len(sub) && sub[i] == w {
			i++
		}
	}
	return i == len(sub)
}

// normalisePersonName lowercases, folds diacritics and strips punctuation so
// that initials, commas and accents do not create spurious differences.
//
// Diacritics are folded here but deliberately NOT in normaliseTitle. Personal
// names are transliterated inconsistently by every provider -- "Émile Zola" and
// "Emile Zola" are the same person, and refusing to fold left them disagreeing
// on their first initial. Titles are different: "Blåbær" and "Blabaer" are not
// reliably the same work, and folding there would erase a real distinction.
func normalisePersonName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = foldDiacritics(s)
	s = nonAlnumRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// foldDiacritics decomposes and drops combining marks, so "é" becomes "e".
func foldDiacritics(s string) string {
	folded, _, err := transform.String(
		transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC),
		s,
	)
	if err != nil {
		return s
	}
	return folded
}

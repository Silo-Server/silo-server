package metadata

import (
	"strings"
	"unicode"
	"unicode/utf8"

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
// (punctuation already gone), "King, Stephen" against "Stephen King", a
// middle name present on one side only, and spacing ("Mary Van Dyke" against
// "Mary VanDyke").
func personNamesMatch(a, b string) bool {
	if a == b || sameLetters(a, b) {
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
	aInitial, _ := utf8.DecodeRuneInString(a[0])
	bInitial, _ := utf8.DecodeRuneInString(b[0])
	return aInitial == bInitial
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

// CreditsAgree is the audiobook form of AuthorsAgree: it checks the item's
// credited names against the fetched people of one kind, authors or
// narrators. As there, absence on either side is not a contradiction.
//
// It is stricter about short names. AuthorsAgree lets a bare surname agree
// with anyone of that surname, which is how a "King" tag passes for any King;
// here a one-word name agrees only with the same one word. An item name may
// hold several people in one tag ("Michael Kramer, Kate Reading"), so a
// fetched name found inside it also agrees.
func CreditsAgree(itemNames []string, people []models.ItemPerson, kind models.PersonKind) bool {
	var want []string
	for _, n := range itemNames {
		if n = normalisePersonName(n); n != "" {
			want = append(want, n)
		}
	}
	if len(want) == 0 {
		return true
	}
	var got []string
	for _, p := range people {
		if p.Kind != kind {
			continue
		}
		if n := normalisePersonName(p.Name); n != "" {
			got = append(got, n)
		}
	}
	if len(got) == 0 {
		return true
	}
	for _, w := range want {
		for _, g := range got {
			if strictNamesMatch(w, g) {
				return true
			}
		}
	}
	return false
}

// strictNamesMatch is personNamesMatch without the bare-surname allowance,
// and with surname-first order ("Bray R C") accepted as the same words.
func strictNamesMatch(a, b string) bool {
	if a == b || sameLetters(a, b) {
		return true
	}
	aw, bw := strings.Fields(a), strings.Fields(b)
	if len(aw) < 2 || len(bw) < 2 {
		return false
	}
	if sameWords(aw, bw) {
		return true
	}
	return personNamesMatch(a, b)
}

// sameLetters reports whether two normalised names differ only in where
// their spaces fall: "jo anne smith" and "joanne smith". Every letter must
// match in order, so it never pairs two different names.
func sameLetters(a, b string) bool {
	return strings.ReplaceAll(a, " ", "") == strings.ReplaceAll(b, " ", "")
}

func sameWords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, w := range a {
		counts[w]++
	}
	for _, w := range b {
		if counts[w] == 0 {
			return false
		}
		counts[w]--
	}
	return true
}

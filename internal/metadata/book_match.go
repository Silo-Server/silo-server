package metadata

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/bookid"
)

// BookMatchRules turns on the stricter candidate selection audiobooks use.
// The plain title score treats an edition as equal to the plain title and lets
// a series name stand in for one of its volumes; for audiobooks that picked
// book 1 for "Dungeon Crawler Carl 2" and a box set for "The Hunger Games".
// Ebook and manga enrichment pass no rules and keep the plain behavior.
type BookMatchRules struct {
	// SeriesPosition is the item's whole-number position in its series, or 0
	// when unknown. It stands in for a volume the title does not state.
	SeriesPosition int
	// BareTitle, when set, is the item's title without a leading series
	// name and number ("Book Title" for "Series Name 3 - Book Title").
	// Candidates are scored against it and against the full title, and keep
	// the better score, so a provider title is not marked down for lacking
	// the series or for keeping it.
	BareTitle string
	// VolumeTitle, when set, is the title the volume rules read in place of
	// the full title (SeriesVolumeTitle): the volume a series prefix states,
	// which a number in the book's own title ("Series Name 3 - Room 101")
	// must not replace.
	VolumeTitle string
}

// SeriesVolumeTitle states a series volume as an explicit marker after the
// bare title: "Room 101, Book 3".
func SeriesVolumeTitle(bare, volume string) string {
	return bare + ", Book " + volume
}

// editionMarkerRE finds the edition words that name a different product than
// the plain title: several books in one, a dramatisation, or a cut text.
// "Unabridged" does not match: \b cannot fall inside the word.
//
//nolint:misspell // "dramatised" is a provider decoration we must recognize.
var editionMarkerRE = regexp.MustCompile(
	`(?i)\b(box(?:ed)?\s*set|omnibus|dramati[sz](?:ed|ation)|full[\s-]*cast|abridged)\b`)

// editionMarkers returns the canonical edition markers title carries.
func editionMarkers(title string) map[string]bool {
	markers := map[string]bool{}
	for _, m := range editionMarkerRE.FindAllString(strings.ToLower(title), -1) {
		key := strings.Join(strings.Fields(strings.ReplaceAll(m, "-", " ")), "")
		switch {
		case strings.HasPrefix(key, "box"):
			key = "boxset"
		case strings.HasPrefix(key, "dramati"):
			key = "dramatized"
		}
		markers[key] = true
	}
	return markers
}

// editionDiffers reports whether want and candidate name different editions:
// either one carries an edition marker the other lacks. An item that asks for
// the abridged recording must not take the full one, and the reverse.
func editionDiffers(want, candidate string) bool {
	return !maps.Equal(editionMarkers(want), editionMarkers(candidate))
}

// bookCandidate is one credible result with the keys selection ranks it by.
type bookCandidate struct {
	selection  bestMatchSelection
	sharedID   bool
	sameVolume bool
	yearGap    int
	key        string
}

// selectBookMatch picks the candidate an audiobook should be matched to.
//
// A candidate is dropped when its provider IDs contradict the item's, when it
// states another volume than the item's title, or, unless an ID vouches for it, when it names an
// edition the item does not. A candidate that carries an ID the item already
// holds (a tagged ASIN) wins without a title score, unless the item names an
// author and the titles share no word: that pairing says the tag is wrong. An
// item with no author matches only through such an ID.
//
// When the item's title states a volume, a candidate stating none is dropped
// if it holds every other word of the title: that is the series name, so
// another volume.
//
// Among candidates whose scores tie (within scoreTieEpsilon of the best), one
// stating the wanted volume wins, then the nearer year, then the higher score,
// then the provider IDs in sorted order, so the result does not depend on the
// order results arrive in. The volume only breaks ties: a translated edition
// that names "Mistborn 2" in a long title must not beat the exact English
// title of book 2.
func selectBookMatch(
	want string,
	wantYear int,
	rules BookMatchRules,
	existing map[string]string,
	noAuthor bool,
	results []SearchResult,
) (bestMatchSelection, bool) {
	w := bookWant{title: want, year: wantYear, existing: existing, noAuthor: noAuthor, threshold: matchThreshold()}
	// Volumes are compared per candidate (volumeAgainst), so the score itself
	// carries no volume rule here.
	w.prepared = preparedWantedTitle{normalised: normaliseTitle(want)}
	if strings.TrimSpace(rules.BareTitle) != "" {
		w.bare = &preparedWantedTitle{normalised: normaliseTitle(rules.BareTitle)}
	}
	w.volumeTitle = want
	if strings.TrimSpace(rules.VolumeTitle) != "" {
		w.volumeTitle = rules.VolumeTitle
	}
	w.seriesPosition = rules.SeriesPosition

	var credible []bookCandidate
	for _, r := range results {
		if c, ok := w.evaluate(r); ok {
			credible = append(credible, c)
		}
	}
	if len(credible) == 0 {
		return bestMatchSelection{}, false
	}

	// A candidate the item's own ID vouches for outranks every title match.
	if slices.ContainsFunc(credible, func(c bookCandidate) bool { return c.sharedID }) {
		credible = slices.DeleteFunc(credible, func(c bookCandidate) bool { return !c.sharedID })
	}
	topScore := slices.MaxFunc(credible, func(a, b bookCandidate) int {
		return cmp.Compare(a.selection.score, b.selection.score)
	}).selection.score
	pool := slices.DeleteFunc(credible, func(c bookCandidate) bool {
		return c.selection.score < topScore-scoreTieEpsilon
	})
	best := slices.MinFunc(pool, func(a, b bookCandidate) int {
		return cmp.Or(
			-boolCompare(a.sameVolume, b.sameVolume),
			cmp.Compare(a.yearGap, b.yearGap),
			cmp.Compare(b.selection.score, a.selection.score),
			cmp.Compare(a.key, b.key),
		)
	})
	return best.selection, true
}

// bookWant is what the item asks of a candidate.
type bookWant struct {
	title    string
	prepared preparedWantedTitle
	// bare is the title without its series prefix, or nil.
	bare *preparedWantedTitle
	// volumeTitle is the title the volume rules read: title, or the
	// series prefix's volume after the bare title.
	volumeTitle    string
	seriesPosition int // a tiebreak only; the title's own volume rules candidates out
	year           int
	existing       map[string]string
	noAuthor       bool
	threshold      float64
}

// evaluate applies selectBookMatch's rules to one result. ok is false when
// the result is not credible.
func (w bookWant) evaluate(r SearchResult) (bookCandidate, bool) {
	name := primaryTitle(r)
	if contradictsProviderIDs(w.existing, r.ProviderIDs) {
		return bookCandidate{}, false
	}
	sharedID := sharesProviderID(w.existing, r.ProviderIDs) &&
		(w.noAuthor || resultSharesTitleWord(w.title, r))
	if w.noAuthor && !sharedID {
		return bookCandidate{}, false
	}
	// The tagged ID names its edition, so only a title-matched candidate is
	// held to the item's edition words.
	if !sharedID && editionDiffers(w.title, name) {
		return bookCandidate{}, false
	}

	sameVolume, fits := w.volumeFits(r, name)
	if !fits {
		return bookCandidate{}, false
	}

	score := w.score(name)
	matchedTitle := name
	for _, alias := range r.TitleAliases {
		// An alias is held to the same volume rules as the primary title
		// before its score may establish the match.
		if !w.aliasFits(alias.Title) {
			continue
		}
		if s := w.score(alias.Title); s > score {
			score, matchedTitle = s, alias.Title
		}
	}
	if !sharedID && score <= w.threshold {
		return bookCandidate{}, false
	}

	yearGap := 1 << 30
	if w.year > 0 && r.Year > 0 {
		yearGap = abs(r.Year - w.year)
	}
	return bookCandidate{
		selection:  bestMatchSelection{result: r, score: score, matchedTitle: matchedTitle},
		sharedID:   sharedID,
		sameVolume: sameVolume,
		yearGap:    yearGap,
		key:        resultKey(r, name),
	}, true
}

// score is the candidate title's score against the item's title, or against
// its bare title when that scores higher.
func (w bookWant) score(title string) float64 {
	normalised := normaliseTitle(title)
	s := w.prepared.score(title, normalised)
	if w.bare != nil {
		s = max(s, w.bare.score(title, normalised))
	}
	return s
}

// volumeFits applies the volume rules to a result whose primary title is
// name. fits is false when the result names another volume; sameVolume marks
// one stating the item's volume, which wins ties.
func (w bookWant) volumeFits(r SearchResult, name string) (sameVolume, fits bool) {
	wv, titleVol := volumeAgainst(w.volumeTitle, name)
	cv, cok := volumeAgainst(name, w.volumeTitle)
	switch {
	case cok && cv.first != cv.last && (!titleVol || cv != wv):
		// A volume range ("Books 1-3") is a collection, not this book, unless
		// the item's title names the same range.
		return false, false
	case titleVol && cok && cv != wv && !w.otherTitleAgrees(r):
		return false, false
	case titleVol && cok:
		// Same volume, or another series' numbering in the primary title
		// with the item's numbering on another of the record's titles.
		sameVolume = true
	case !titleVol && cok && w.seriesPosition > 0:
		// A tagged series position may count in another series' numbering,
		// so it only breaks ties.
		sameVolume = cv == volumeIdentity{first: w.seriesPosition, last: w.seriesPosition}
	case titleVol && !cok && coversAllButVolume(w.title, wv, name):
		return false, false
	}
	return sameVolume, true
}

// otherTitleAgrees reports whether the result's original title or an alias
// both passes the title match against the item and states the volume the
// item's title states. A book can sit in two numbered
// series ("Guards! Guards!" is Discworld 8 and City Watch 1), so a primary
// title in the other numbering does not rule it out.
func (w bookWant) otherTitleAgrees(r SearchResult) bool {
	titles := []string{r.OriginalTitle}
	for _, alias := range r.TitleAliases {
		titles = append(titles, alias.Title)
	}
	for _, t := range titles {
		if strings.TrimSpace(t) == "" {
			continue
		}
		// It must name the item's work, not just carry its number.
		if w.score(t) <= w.threshold {
			continue
		}
		wv, wok := volumeAgainst(w.volumeTitle, t)
		if tv, tok := volumeAgainst(t, w.volumeTitle); wok && tok && tv == wv {
			return true
		}
	}
	return false
}

// aliasFits reports whether an alias title passes the volume rules a primary
// title must: it states no other volume or a volume range, and it is not the
// series name standing in for the item's volume.
func (w bookWant) aliasFits(alias string) bool {
	wv, titleVol := volumeAgainst(w.volumeTitle, alias)
	av, aok := volumeAgainst(alias, w.volumeTitle)
	switch {
	case aok && av.first != av.last && (!titleVol || av != wv):
		return false
	case titleVol && aok && av != wv:
		return false
	case titleVol && !aok && coversAllButVolume(w.title, wv, alias):
		return false
	}
	return true
}

func boolCompare(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// coversAllButVolume reports whether candidate holds every word of want other
// than its volume number: "Dungeon Crawler Carl" for "Dungeon Crawler Carl 2".
// A candidate holding the number as often as want does is the title itself
// ("Series Name 3 - Gone"), not the series name; one holding it fewer times
// lacks the volume ("Zone 3" for "Zone 3, Book 3").
func coversAllButVolume(want string, volume volumeIdentity, candidate string) bool {
	words := identityWords(normaliseTitle(want))
	candidateWords := identityWords(normaliseTitle(candidate))
	n := volume.word()
	if words[n] > 0 {
		if candidateWords[n] >= words[n] {
			return false
		}
		words[n]--
		if words[n] == 0 {
			delete(words, n)
		}
	}
	if len(words) == 0 {
		return false
	}
	return wordSubset(words, candidateWords)
}

// resultSharesTitleWord reports whether want and the result's title or an
// alias have at least one identifying word in common.
func resultSharesTitleWord(want string, r SearchResult) bool {
	titles := []string{r.Name, r.OriginalTitle}
	for _, alias := range r.TitleAliases {
		titles = append(titles, alias.Title)
	}
	return !TitleContradicts(want, titles...)
}

// VolumesContradict reports whether want's title and a record's primary title
// both state a volume and the volumes differ: "Dungeon Crawler Carl 2" against
// "Dungeon Crawler Carl, Book 1". A title that states none contradicts
// nothing, and a number both titles share is part of the title
// (volumeAgainst).
func VolumesContradict(want, record string) bool {
	if strings.TrimSpace(record) == "" {
		return false
	}
	wv, wok := volumeAgainst(want, record)
	rv, rok := volumeAgainst(record, want)
	return wok && rok && rv != wv
}

// RecordVolumeContradicts reports whether a record's titles (primary and
// aliases) state a volume other than the one want states, and none states
// want's. A book can sit in two numbered series -- "Guards! Guards!" is
// Discworld 8 and City Watch 1 -- so one alias with another number is not
// enough while another title agrees.
func RecordVolumeContradicts(want string, titles []string) bool {
	contradicted := false
	for _, t := range titles {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if VolumesContradict(want, t) {
			contradicted = true
			continue
		}
		wv, wok := volumeAgainst(want, t)
		if tv, tok := volumeAgainst(t, want); wok && tok && tv == wv {
			return false
		}
	}
	return contradicted
}

// volumeAgainst is the volume title states when compared with other. An
// explicit marker ("Book 2", "Vol. 2", "#2", "Part 2", a range) always counts.
// A bare number counts only when other does not have it too: in
// "Slaughterhouse-Five", "Catch-22" or "Fahrenheit 451" the number is part of
// the title, and the record "Slaughterhouse-Five, Book 1" repeats it, while
// "Dungeon Crawler Carl" lacks the 2 of "Dungeon Crawler Carl 2".
//
// The bare number read is the last one, not the first: in "The 39 Clues 2"
// or "Area 51 2" the series name carries a number of its own, and the volume
// is the trailing 2. A record that repeats the item's trailing number and
// also states a different explicit volume ("X 2" against "X 2, Book 1") is
// read as "Catch-22, Book 1" is, and accepted; no real title of that shape is
// known, while famous numeric titles are common.
func volumeAgainst(title, other string) (volumeIdentity, bool) {
	v, ok, bare := titleVolumeKind(title)
	if !ok || !bare {
		return v, ok
	}
	v, found := lastBareVolume(title)
	if !found || slices.Contains(strings.Fields(normaliseTitle(other)), v.word()) {
		return volumeIdentity{}, false
	}
	return v, true
}

// lastBareVolume is the last word of title's normalised form that reads as a
// volume number (parseVolume: 1 to 999 or a decimal; years date an edition,
// not a volume).
func lastBareVolume(title string) (volumeIdentity, bool) {
	fields := strings.Fields(normaliseTitle(title))
	for i := len(fields) - 1; i >= 0; i-- {
		if v, ok := parseVolume(fields[i]); ok {
			return v, true
		}
	}
	return volumeIdentity{}, false
}

// TitleContradicts reports whether want and a record's titles share no
// identifying word. Empty titles are ignored; with nothing to compare on
// either side there is no contradiction.
func TitleContradicts(want string, titles ...string) bool {
	wn := normaliseTitle(want)
	if wn == "" {
		return false
	}
	ww := identityWords(wn)
	compared := false
	for _, title := range titles {
		tn := normaliseTitle(title)
		if tn == "" {
			continue
		}
		compared = true
		for word := range identityWords(tn) {
			if ww[word] > 0 {
				return false
			}
		}
	}
	return compared
}

// contradictsProviderIDs reports whether candidate holds a different ID than
// existing under the same provider key.
func contradictsProviderIDs(existing, candidate map[string]string) bool {
	have := normaliseProviderIDs(existing)
	for provider, id := range normaliseProviderIDs(candidate) {
		if current, ok := have[provider]; ok && current != id {
			return true
		}
	}
	return false
}

func normaliseProviderIDs(ids map[string]string) map[string]string {
	out := make(map[string]string, len(ids))
	for provider, id := range ids {
		provider, id = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(id)
		if provider != "" && id != "" {
			out[provider] = bookid.CanonicalProviderID(provider, id)
		}
	}
	return out
}

// resultKey is a stable identity for a result: its provider IDs in sorted
// order, then its title.
func resultKey(r SearchResult, name string) string {
	ids := normaliseProviderIDs(r.ProviderIDs)
	parts := make([]string, 0, len(ids)+1)
	for provider, id := range ids {
		parts = append(parts, provider+"="+id)
	}
	slices.Sort(parts)
	return strings.Join(append(parts, name), "\x00")
}

// SameSearchResult reports whether a and b are the same provider result. The
// enricher uses it to drop a candidate that failed a check after its fetch.
func SameSearchResult(a, b SearchResult) bool {
	return resultKey(a, primaryTitle(a)) == resultKey(b, primaryTitle(b))
}

// SharesProviderID reports whether candidate carries an ID the item already
// holds under the same provider.
func SharesProviderID(existing, candidate map[string]string) bool {
	return sharesProviderID(existing, candidate)
}

// primaryTitle is the title a result is scored by: its name, or its original
// title when it has no name.
func primaryTitle(r SearchResult) string {
	if strings.TrimSpace(r.Name) == "" {
		return r.OriginalTitle
	}
	return r.Name
}

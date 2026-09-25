package access

import (
	"strconv"
	"strings"
)

// Content ratings reach Silo as free text: providers, Kodi/Radarr NFOs and
// clients all write whatever their source system uses ("PG-13", "FSK 16",
// "DE:16", "Rated R", "12A", "tv-ma"). The comparable axis across every
// country's system is the minimum age it recommends or requires — the US
// ladder is the odd one out only because it hides the age behind letters.
//
// Normalize resolves a raw string to that age; everything else in this file,
// including the historical 0-3 rank shims, is derived from it. This is the one
// place the ladder lives on the Go side.

// Rating systems Normalize recognizes. The values are stable identifiers, safe
// to log or persist alongside a resolved age.
const (
	// systemMPAA is the US film ladder (G, PG, PG-13, R, NC-17).
	systemMPAA = "mpaa"
	// systemUSTV is the US television ladder (TV-Y … TV-MA).
	systemUSTV = "us-tv"
	// systemBBFC is the UK ladder (U, PG, 12, 12A, 15, 18, R18).
	systemBBFC = "bbfc"
	// systemFSK is the German ladder (0, 6, 12, 16, 18).
	systemFSK = "fsk"
	// systemCNC is the French ladder (Tous publics, 12, 16, 18).
	systemCNC = "cnc"
	// systemACB is the Australian ladder (G, PG, M, MA15+, R18+, X18+).
	systemACB = "acb"
	// systemKijkwijzer is the Dutch ladder (AL, 6, 9, 12, 14, 16, 18).
	systemKijkwijzer = "kijkwijzer"
	// systemEirin is the Japanese ladder (G, PG12, R15+, R18+).
	systemEirin = "eirin"
	// systemICAA is the Spanish ladder (APTA, 7, 12, 16, 18, X).
	systemICAA = "icaa"
	// systemDJCTQ is the Brazilian ladder (L, 10, 12, 14, 16, 18).
	systemDJCTQ = "djctq"
	// systemIGAC is the Portuguese ladder (M/3, M/6, M/12, M/14, M/16, M/18).
	systemIGAC = "igac"
	// systemCBFC is the Indian ladder (U, UA 7+, UA 13+, UA 16+). Its adult
	// rung "A" is left out: Mexico's "A" means all ages.
	systemCBFC = "cbfc"
	// systemINCAA is the Argentine ladder (ATP, SAM 13, SAM 16, SAM 18).
	systemINCAA = "incaa"
	// systemRTC is the Mexican ladder's unambiguous rungs (AA, B-15). The
	// single letters A, B and C mean different things in other systems.
	systemRTC = "rtc"
	// systemKMRB is the Korean ladder's all-ages rung (ALL); its other rungs
	// are bare ages.
	systemKMRB = "kmrb"
	// systemAge is a bare minimum age with no system attached ("16", "16+").
	systemAge = "age"
	// systemUnrated is an explicit "no rating assigned" marker (NR, Unrated,
	// Not Rated). It carries no age, which is distinct from age 0.
	systemUnrated = "unrated"
)

// maxRatingAge bounds a bare numeric rating. Anything higher is junk in a
// rating column (a year, a runtime) rather than an age.
const maxRatingAge = 21

// ratingLadder is the whole maturity ladder, by system. Tokens are canonical
// form (see canonicalRatingToken): uppercase, punctuation removed except "+".
//
// Order matters: a token several systems share ("PG", "12", "18") resolves to
// the first system listed here that defines it. The ages agree wherever the
// systems overlap, so the priority only decides the reported system name, not
// the age.
//
//nolint:goconst // The ladder is data: each rung stays readable in its own system's table.
var ratingLadder = []struct {
	system string
	ages   map[string]int
}{
	{systemMPAA, map[string]int{
		"G": 0, "PG": 8, "PG13": 13, "R": 17, "NC17": 18,
	}},
	{systemUSTV, map[string]int{
		"TVY": 0, "TVG": 0, "TVY7": 7, "TVY7FV": 7, "TVPG": 8, "TV14": 14, "TVMA": 17,
	}},
	{systemBBFC, map[string]int{
		"U": 0, "UC": 0, "PG": 8, "12": 12, "12A": 12, "15": 15, "18": 18, "R18": 18,
	}},
	{systemFSK, map[string]int{
		"0": 0, "FSK0": 0, "6": 6, "FSK6": 6, "12": 12, "FSK12": 12,
		"16": 16, "FSK16": 16, "18": 18, "FSK18": 18,
	}},
	{systemCNC, map[string]int{
		"TOUSPUBLICS": 0, "TP": 0, "U": 0, "10": 10, "12": 12, "16": 16, "18": 18,
	}},
	// Australia writes the restricted rung "MA 15+", never a bare "MA", which
	// is instead the common short form of US TV-MA (17). Recognizing "MA" here
	// would admit an adult series to a 15- or 16-year ceiling, so it is left
	// unrecognized, and an unrecognized rating is hidden from every ceilinged
	// viewer (see UnrecognizedRatingAge).
	{systemACB, map[string]int{
		"G": 0, "PG": 8, "M": 15, "MA15": 15, "MA15+": 15,
		"R18": 18, "R18+": 18, "X18": 18, "X18+": 18,
	}},
	{systemKijkwijzer, map[string]int{
		"AL": 0, "6": 6, "9": 9, "12": 12, "14": 14, "16": 16, "18": 18,
	}},
	{systemEirin, map[string]int{
		"G": 0, "PG12": 12, "R15": 15, "R15+": 15, "R18+": 18,
	}},
	{systemICAA, map[string]int{
		"APTA": 0, "TP": 0, "7": 7, "12": 12, "16": 16, "18": 18, "X": 18,
	}},
	{systemDJCTQ, map[string]int{
		"L": 0, "10": 10, "12": 12, "14": 14, "16": 16, "18": 18,
	}},
	{systemIGAC, map[string]int{
		"M3": 3, "M6": 6, "M12": 12, "M14": 14, "M16": 16, "M18": 18,
	}},
	{systemCBFC, map[string]int{
		"U": 0, "UA": 12, "UA7": 7, "UA7+": 7, "UA13": 13, "UA13+": 13, "UA16": 16, "UA16+": 16,
	}},
	{systemINCAA, map[string]int{
		"ATP": 0, "SAM13": 13, "SAM16": 16, "SAM18": 18,
	}},
	{systemRTC, map[string]int{
		"AA": 0, "B15": 15,
	}},
	{systemKMRB, map[string]int{
		"ALL": 0,
	}},
}

// unratedTokens resolve to systemUnrated: a recognized rating string that
// carries no age at all.
var unratedTokens = map[string]struct{}{
	"NR":          {},
	"UR":          {},
	"NA":          {},
	"NONE":        {},
	"UNRATED":     {},
	"UNKNOWN":     {},
	"NOTRATED":    {},
	"NOTYETRATED": {},
}

// countrySystems maps the country prefix Kodi/Radarr write ("DE:16",
// "US:PG-13", "gb:15") to the systems to consult first for the remainder.
var countrySystems = map[string][]string{
	"US":  {systemMPAA, systemUSTV},
	"USA": {systemMPAA, systemUSTV},
	"GB":  {systemBBFC},
	"GBR": {systemBBFC},
	"UK":  {systemBBFC},
	"DE":  {systemFSK},
	"DEU": {systemFSK},
	"GER": {systemFSK},
	"FR":  {systemCNC},
	"FRA": {systemCNC},
	"AU":  {systemACB},
	"AUS": {systemACB},
	"NL":  {systemKijkwijzer},
	"NLD": {systemKijkwijzer},
	"JP":  {systemEirin},
	"JPN": {systemEirin},
	"ES":  {systemICAA},
	"ESP": {systemICAA},
	"BR":  {systemDJCTQ},
	"BRA": {systemDJCTQ},
	"PT":  {systemIGAC},
	"PRT": {systemIGAC},
	"IN":  {systemCBFC},
	"IND": {systemCBFC},
	"AR":  {systemINCAA},
	"ARG": {systemINCAA},
	"MX":  {systemRTC},
	"MEX": {systemRTC},
	"KR":  {systemKMRB},
	"KOR": {systemKMRB},
}

type ratingEntry struct {
	system string
	age    int
}

var (
	// ratingIndex resolves a canonical token without a country hint.
	ratingIndex = map[string]ratingEntry{}
	// systemIndex resolves a canonical token within one system.
	systemIndex = map[string]map[string]int{}
)

// usRatingLadder is the US film and television ladder, grouped by the tier
// (legacy rank) each rating belongs to. Profile ceilings were only ever written
// from this list before ages existed, and clients still label them by tier
// ("PG-13 / TV-14").
//
//nolint:goconst // The US ladder is data, listed tier by tier.
var usRatingLadder = []string{
	"G", "TV-Y", "TV-G",
	"PG", "TV-Y7", "TV-PG",
	"PG-13", "TV-14",
	"R", "NC-17", "TV-MA",
}

// usTierCeilingAge is, per legacy rank, the oldest age any US rating in that
// tier stands for: 0, 8, 14, 18. A US ceiling admits its whole tier (see
// AgeForCeiling), so this is the age it compares against.
var usTierCeilingAge [4]int

type ceilingValue struct {
	value string
	age   int
}

// ceilingValues are the ceiling strings CompatibleCeilings can answer with:
// the US ladder plus every bare age, which is what the profile editor writes
// for a ceiling outside the US ladder ("12", "15").
var ceilingValues []ceilingValue

func init() {
	for _, table := range ratingLadder {
		ages := make(map[string]int, len(table.ages))
		for token, age := range table.ages {
			ages[token] = age
			if _, seen := ratingIndex[token]; !seen {
				ratingIndex[token] = ratingEntry{system: table.system, age: age}
			}
		}
		systemIndex[table.system] = ages
	}

	for _, rating := range usRatingLadder {
		_, age, ok := Normalize(rating)
		if !ok || age == nil {
			panic("access: US rating ladder entry without an age: " + rating)
		}
		if rank := rankForAge(*age); *age > usTierCeilingAge[rank] {
			usTierCeilingAge[rank] = *age
		}
	}

	candidates := append([]string(nil), usRatingLadder...)
	for age := 0; age <= maxRatingAge; age++ {
		candidates = append(candidates, strconv.Itoa(age))
	}
	for _, value := range candidates {
		age, ok := AgeForCeiling(value)
		if !ok {
			panic("access: ceiling value without an age: " + value)
		}
		ceilingValues = append(ceilingValues, ceilingValue{value: value, age: *age})
	}
}

// Normalize resolves a raw content rating to the minimum age it stands for.
//
// It returns the rating system the string was matched in, the minimum age, and
// whether the string was recognized at all. The three results distinguish the
// cases callers care about:
//
//   - ok is false: the string means nothing here (junk, or empty). No age.
//   - ok is true, age is nil: an explicit "not rated" marker (NR, Unrated).
//     Recognized, but it carries no age — which is not the same as age 0.
//   - ok is true, age is non-nil: a real rating, comparable across systems.
//
// Matching is case-insensitive and tolerant of separators and surrounding
// whitespace, and understands the country-prefixed form ("DE:16", "gb:15"), the
// "Rated R" prefix, and bare ages ("15", "16+").
func Normalize(raw string) (system string, age *int, ok bool) {
	rest, systems := splitCountryPrefix(raw)
	for _, token := range ratingTokens(rest) {
		if system, age, ok := resolveRatingToken(token, systems); ok {
			return system, age, ok
		}
	}
	return "", nil, false
}

// ratingTokens lists the lookup keys to try for a rating string, most specific
// first. The MPAA rating is often written with its reason attached ("Rated R
// for strong language"), so when the whole string is not a rating, the first
// word after "Rated" is tried on its own. The fallback can only widen what is
// recognized, never change a string that already resolved.
func ratingTokens(rest string) []string {
	token := canonicalRatingToken(rest)
	if token == "" {
		return nil
	}
	stripped, cut := cutRatedPrefix(strings.ToUpper(strings.TrimSpace(rest)))
	if !cut {
		return []string{token}
	}
	words := strings.Fields(stripped)
	if len(words) < 2 {
		return []string{token}
	}
	first := canonicalRatingToken(words[0])
	if first == "" || first == token {
		return []string{token}
	}
	return []string{token, first}
}

// resolveRatingToken looks one canonical token up, consulting the systems a
// country prefix named before the cross-system index.
func resolveRatingToken(token string, systems []string) (string, *int, bool) {
	if _, unrated := unratedTokens[token]; unrated {
		return systemUnrated, nil, true
	}
	// A country prefix picks the ladder to read first, so "US:M" and "AU:M"
	// cannot land on each other's meaning when the token is shared.
	for _, candidate := range systems {
		if value, found := systemIndex[candidate][token]; found {
			return candidate, ageValue(value), true
		}
	}
	if entry, found := ratingIndex[token]; found {
		return entry.system, ageValue(entry.age), true
	}
	if value, found := bareAge(token); found {
		return systemAge, ageValue(value), true
	}
	return "", nil, false
}

// AgeForCeiling resolves a profile's max_content_rating to the oldest minimum
// age it admits. ok is false when the ceiling sets no usable age limit: it is
// empty, explicitly unrated, or unrecognized. Callers that treat "no ceiling"
// differently from "unusable ceiling" must check for an empty string first.
//
// A US ceiling admits its whole tier, not just ratings at or below its own
// age. The US film and TV ladders are parallel tiers rather than ages, and a
// "PG-13" ceiling has always admitted TV-14 and an "R" ceiling NC-17; both
// clients label the ceilings that way. So a US ceiling resolves to the oldest
// age in its tier (PG-13 and TV-14 to 14, R/NC-17/TV-MA to 18), while an item
// rated PG-13 still stores 13. Every other system is age-based and resolves
// to its own age.
func AgeForCeiling(ceiling string) (*int, bool) {
	system, age, ok := Normalize(ceiling)
	if !ok || age == nil {
		return nil, false
	}
	if system == systemMPAA || system == systemUSTV {
		return ageValue(usTierCeilingAge[rankForAge(*age)]), true
	}
	return age, true
}

// splitCountryPrefix strips a leading "XX:" country code and returns the
// remainder plus the systems that country uses. An unknown or malformed prefix
// yields no systems, leaving the remainder to the global lookup.
func splitCountryPrefix(raw string) (string, []string) {
	trimmed := strings.TrimSpace(raw)
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return trimmed, nil
	}
	code := strings.ToUpper(strings.TrimSpace(trimmed[:idx]))
	if len(code) < 2 || len(code) > 3 || !isAlpha(code) {
		return trimmed, nil
	}
	return strings.TrimSpace(trimmed[idx+1:]), countrySystems[code]
}

// canonicalRatingToken folds a rating string to its lookup key: uppercase, with
// every character that is not a letter, digit or "+" dropped. "PG-13", "pg 13"
// and "PG.13" all canonicalize to "PG13"; "+" is kept because "MA15+" and
// "R18+" are written that way. A leading "Rated " is removed first.
func canonicalRatingToken(raw string) string {
	value := strings.ToUpper(strings.TrimSpace(raw))
	if rest, cut := cutRatedPrefix(value); cut {
		value = rest
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '+':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cutRatedPrefix removes the "Rated " form ("Rated R", "Rated PG-13") that some
// providers write. The prefix must be a whole word so "Unrated" and
// "Not Rated" are left alone.
func cutRatedPrefix(upper string) (string, bool) {
	const prefix = "RATED"
	if !strings.HasPrefix(upper, prefix) {
		return upper, false
	}
	rest := upper[len(prefix):]
	if rest == "" {
		return upper, false
	}
	if next := rest[0]; next >= 'A' && next <= 'Z' {
		return upper, false
	}
	return strings.TrimSpace(rest), true
}

// bareAge reads a canonical token that is just a number, with an optional "+"
// on either side: "15", "16+", "+16".
func bareAge(token string) (int, bool) {
	digits := strings.TrimSuffix(strings.TrimPrefix(token, "+"), "+")
	if digits == "" || len(digits) > 2 {
		return 0, false
	}
	age, err := strconv.Atoi(digits)
	if err != nil || age > maxRatingAge {
		return 0, false
	}
	return age, true
}

func isAlpha(value string) bool {
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return len(value) > 0
}

// ageValue copies the age out of the tables so callers cannot mutate them
// through the returned pointer.
func ageValue(age int) *int {
	value := age
	return &value
}

// rankForAge maps a minimum age onto the legacy 0-3 maturity bucket that
// RatingRank exposes. The boundaries are chosen so every US rating keeps the
// rank it had before ages existed: G/TV-Y/TV-G (0) stay 0, PG/TV-Y7/TV-PG
// (8/7/8) stay 1, PG-13/TV-14 (13/14) stay 2, and R/NC-17/TV-MA (17/18/17)
// stay 3.
//
// Because the buckets are monotone in age, a ceiling's rank is never lower than
// the rank of any rating the ceiling allows — the superset property
// requests.certificationCeilingFor depends on (see its comment).
func rankForAge(age int) int {
	switch {
	case age <= 0:
		return 0
	case age <= 12:
		return 1
	case age <= 16:
		return 2
	default:
		return 3
	}
}

// HasCeiling reports whether a stored max_content_rating value sets a ceiling
// at all. Only the empty string means "no ceiling".
//
// Text that is nothing but whitespace is deliberately NOT "no ceiling": it is a
// ceiling no rating can be compared against, and an unusable parental control
// blocks everything rather than admitting the whole catalog (see
// catalog.ApplyMaturityLimits). max_content_rating is free text on both
// profile APIs, so a stored " " is reachable; trimming before the emptiness
// test would turn a deny-all control into an allow-all one, which is the one
// direction a parental control must never fail in. Callers that clear a
// ceiling write "" or null.
func HasCeiling(ceiling string) bool {
	return ceiling != ""
}

// RatingAllowed reports whether a content rating is visible under the ceiling.
// An empty ceiling allows everything; a ceiling that resolves to no age blocks
// everything, as does a rating that resolves to no age.
func RatingAllowed(rating, ceiling string) bool {
	if !HasCeiling(ceiling) {
		return true
	}
	ceilingAge, ok := AgeForCeiling(ceiling)
	if !ok {
		return false
	}
	_, ratingAge, ok := Normalize(rating)
	if !ok || ratingAge == nil {
		return false
	}
	return *ratingAge <= *ceilingAge
}

// CompatibleCeilings returns the ceiling values no looser than ceiling: the US
// ladder entries and bare ages whose own ceiling age is at or below its age.
//
// It compares one ceiling STRING with another — recommendations matching peer
// profiles by their stored max_content_rating — where no per-item age exists
// to read. Item reads never use it; they compare media_items.content_rating_age.
// A peer ceiling written in some other form ("FSK 16") is not listed, which
// narrows the peer pool but can never widen what a viewer sees.
//
// nil means "no ceiling, no predicate"; a non-nil empty slice means the ceiling
// is unusable and nothing is compatible.
func CompatibleCeilings(ceiling string) []string {
	if !HasCeiling(ceiling) {
		return nil
	}
	ceilingAge, ok := AgeForCeiling(ceiling)
	if !ok {
		return []string{}
	}
	values := make([]string, 0, len(ceilingValues))
	for _, entry := range ceilingValues {
		if entry.age <= *ceilingAge {
			values = append(values, entry.value)
		}
	}
	return values
}

// RatingRank returns the coarse 0-3 maturity rank for a content rating. It is
// a bucketed view of the rating's minimum age; ratings with no age (unrated,
// unrecognized) have no rank. Its one consumer is
// requests.certificationCeilingFor, which needs a bucket coarse enough to map
// onto TMDB's US-only certification filter.
func RatingRank(rating string) (int, bool) {
	_, age, ok := Normalize(rating)
	if !ok || age == nil {
		return 0, false
	}
	return rankForAge(*age), true
}

// UnrecognizedRatingAge is stored for a rating string no ladder recognizes
// ("A", "SPG", "Contains mild peril"). It is older than any age a ceiling can
// resolve to, so such a title is hidden from every ceilinged viewer whatever
// access.unrated_content says: "allow" admits titles that have no rating, never
// titles whose rating Silo cannot read. Sorted by age, they come after every
// real rating and before unrated titles.
const UnrecognizedRatingAge = 99

// StoredRating resolves a raw content rating to the value stored beside it in
// media_items.content_rating_age, which the backfill migration mirrors:
//
//   - a recognized rating stores its minimum age;
//   - no rating at all (empty, or an explicit marker such as NR or Unrated)
//     stores nil, which access.unrated_content governs;
//   - any other text stores UnrecognizedRatingAge.
//
// content_rating keeps the raw string, so the matched system stays
// re-derivable with Normalize and needs no column.
func StoredRating(raw string) *int {
	_, age, ok := Normalize(raw)
	switch {
	case ok:
		return age
	case !hasRatingText(raw):
		return nil
	default:
		return ageValue(UnrecognizedRatingAge)
	}
}

// hasRatingText reports whether raw holds anything a rating could be read
// from, once a country prefix, a "Rated" prefix and punctuation are removed.
func hasRatingText(raw string) bool {
	rest, _ := splitCountryPrefix(raw)
	return canonicalRatingToken(rest) != ""
}

// StricterCeiling combines two maturity ceilings into the one that permits
// less. An empty ceiling means "no limit" (see HasCeiling), so it always loses
// to a real one.
//
// A non-empty ceiling that resolves to no age at all is unusable, and an
// unusable parental control blocks everything (see
// catalog.ApplyMaturityLimits) — so it is the most restrictive value
// there is and wins. That is what makes this safe to apply to a ceiling
// produced somewhere Silo's ladder is not available, such as an
// administrator-authored policy override: combining can only tighten.
func StricterCeiling(a, b string) string {
	switch {
	case !HasCeiling(a):
		return b
	case !HasCeiling(b):
		return a
	}
	ageA, okA := AgeForCeiling(a)
	ageB, okB := AgeForCeiling(b)
	switch {
	case !okA:
		return a
	case !okB:
		return b
	case *ageB < *ageA:
		return b
	default:
		return a
	}
}

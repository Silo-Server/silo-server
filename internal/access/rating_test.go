package access

import (
	"reflect"
	"testing"
)

// normalizeCase is one raw content rating and the system/age it must resolve
// to. A wrong age silently changes what a child can see, so every rung of every
// ladder Normalize claims to know is pinned here.
type normalizeCase struct {
	raw    string
	system string
	age    int
}

func TestNormalizeKnownRatings(t *testing.T) {
	cases := []normalizeCase{
		// US MPAA.
		{"G", systemMPAA, 0},
		{"PG", systemMPAA, 8},
		{"PG-13", systemMPAA, 13},
		{"R", systemMPAA, 17},
		{"NC-17", systemMPAA, 18},

		// US television.
		{"TV-Y", systemUSTV, 0},
		{"TV-Y7", systemUSTV, 7},
		{"TV-Y7-FV", systemUSTV, 7},
		{"TV-G", systemUSTV, 0},
		{"TV-PG", systemUSTV, 8},
		{"TV-14", systemUSTV, 14},
		{"TV-MA", systemUSTV, 17},

		// BBFC / UK.
		{"U", systemBBFC, 0},
		{"Uc", systemBBFC, 0},
		{"12", systemBBFC, 12},
		{"12A", systemBBFC, 12},
		{"15", systemBBFC, 15},
		{"18", systemBBFC, 18},
		{"R18", systemBBFC, 18},
		{"GB:PG", systemBBFC, 8},
		{"GB:U", systemBBFC, 0},

		// FSK / Germany.
		{"FSK 0", systemFSK, 0},
		{"FSK 6", systemFSK, 6},
		{"FSK 12", systemFSK, 12},
		{"FSK 16", systemFSK, 16},
		{"FSK 18", systemFSK, 18},
		{"0", systemFSK, 0},
		{"6", systemFSK, 6},

		// CNC / France.
		{"Tous publics", systemCNC, 0},
		{"FR:U", systemCNC, 0},
		{"FR:TP", systemCNC, 0},
		{"FR:10", systemCNC, 10},
		{"FR:12", systemCNC, 12},
		{"FR:16", systemCNC, 16},
		{"FR:18", systemCNC, 18},

		// ACB / Australia.
		{"M", systemACB, 15},
		{"MA15+", systemACB, 15},
		{"MA 15+", systemACB, 15},
		{"R18+", systemACB, 18},
		{"X18+", systemACB, 18},
		{"AU:G", systemACB, 0},
		{"AU:PG", systemACB, 8},
		{"AU:M", systemACB, 15},

		// Kijkwijzer / Netherlands.
		{"AL", systemKijkwijzer, 0},
		{"9", systemKijkwijzer, 9},
		{"14", systemKijkwijzer, 14},
		{"NL:6", systemKijkwijzer, 6},
		{"NL:12", systemKijkwijzer, 12},
		{"NL:16", systemKijkwijzer, 16},
		{"NL:18", systemKijkwijzer, 18},

		// Eirin / Japan.
		{"PG12", systemEirin, 12},
		{"R15+", systemEirin, 15},
		{"R-15", systemEirin, 15},
		{"JP:G", systemEirin, 0},
		{"JP:PG12", systemEirin, 12},

		// ICAA / Spain.
		{"APTA", systemICAA, 0},
		{"7", systemICAA, 7},
		{"X", systemICAA, 18},
		{"ES:TP", systemICAA, 0},
		{"ES:12", systemICAA, 12},
		{"ES:16", systemICAA, 16},
		{"ES:18", systemICAA, 18},

		// DJCTQ / Brazil.
		{"L", systemDJCTQ, 0},
		// Bare "10" reports cnc: both ladders define it at age 10 and cnc is
		// listed first, so only the system name is decided by the order.
		{"10", systemCNC, 10},
		{"BR:10", systemDJCTQ, 10},
		{"BR:12", systemDJCTQ, 12},
		{"BR:14", systemDJCTQ, 14},
		{"BR:16", systemDJCTQ, 16},
		{"BR:18", systemDJCTQ, 18},

		// IGAC / Portugal, CBFC / India, INCAA / Argentina, RTC / Mexico,
		// KMRB / Korea.
		{"M/12", systemIGAC, 12},
		{"M16", systemIGAC, 16},
		{"PT:M/18", systemIGAC, 18},
		{"UA 13+", systemCBFC, 13},
		{"UA16+", systemCBFC, 16},
		{"UA", systemCBFC, 12},
		{"ATP", systemINCAA, 0},
		{"SAM 16", systemINCAA, 16},
		{"AR:13", systemAge, 13},
		{"B-15", systemRTC, 15},
		{"AA", systemRTC, 0},
		{"ALL", systemKMRB, 0},
		{"-12", systemBBFC, 12},
		{"0+", systemAge, 0},
		{"19", systemAge, 19},

		// Bare ages, with and without a "+".
		{"13", systemAge, 13},
		{"16+", systemAge, 16},
		{"+16", systemAge, 16},
		{"18+", systemAge, 18},
		{"8", systemAge, 8},
		{"21", systemAge, 21},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			system, age, ok := Normalize(tc.raw)
			if !ok {
				t.Fatalf("Normalize(%q) not recognized", tc.raw)
			}
			switch {
			case age == nil:
				t.Fatalf("Normalize(%q) returned no age, want %d", tc.raw, tc.age)
			case *age != tc.age:
				t.Errorf("Normalize(%q) age = %d, want %d", tc.raw, *age, tc.age)
			}
			if system != tc.system {
				t.Errorf("Normalize(%q) system = %q, want %q", tc.raw, system, tc.system)
			}
		})
	}
}

func TestNormalizeCountryPrefixes(t *testing.T) {
	// The prefixed forms Kodi and Radarr write. The country picks which ladder
	// is read first, which matters wherever two systems share a token.
	cases := []normalizeCase{
		{"DE:16", systemFSK, 16},
		{"de:16", systemFSK, 16},
		{"DEU:18", systemFSK, 18},
		{"US:PG-13", systemMPAA, 13},
		{"USA:TV-MA", systemUSTV, 17},
		{"gb:15", systemBBFC, 15},
		{"UK:12A", systemBBFC, 12},
		{"GBR:18", systemBBFC, 18},
		{" DE : 16 ", systemFSK, 16},
		// An unknown prefix falls through to the cross-system lookup.
		{"ZZ:15", systemBBFC, 15},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			system, age, ok := Normalize(tc.raw)
			if !ok || age == nil {
				t.Fatalf("Normalize(%q) = (%q, %v, %v), want an age", tc.raw, system, age, ok)
			}
			if *age != tc.age || system != tc.system {
				t.Errorf("Normalize(%q) = (%q, %d), want (%q, %d)", tc.raw, system, *age, tc.system, tc.age)
			}
		})
	}
}

func TestNormalizeIsCaseAndSeparatorTolerant(t *testing.T) {
	// Every spelling in a group must resolve identically.
	groups := [][]string{
		{"PG-13", "pg-13", "Pg-13", " PG 13 ", "PG.13", "pg13"},
		{"TV-MA", "tv-ma", "  Tv-Ma  ", "TVMA"},
		{"FSK 16", "fsk16", "FSK-16", " fsk 16 "},
		{"MA15+", "ma 15+", "MA-15+"},
		{"Tous publics", "tous publics", "TOUS PUBLICS"},
		{"NC-17", "nc17", " nc-17 "},
	}
	for _, group := range groups {
		want, wantAge, ok := Normalize(group[0])
		if !ok || wantAge == nil {
			t.Fatalf("Normalize(%q) did not resolve", group[0])
		}
		for _, spelling := range group[1:] {
			system, age, ok := Normalize(spelling)
			if !ok || age == nil {
				t.Errorf("Normalize(%q) = (%q, %v, %v), want same as %q", spelling, system, age, ok, group[0])
				continue
			}
			if system != want || *age != *wantAge {
				t.Errorf("Normalize(%q) = (%q, %d), want (%q, %d) like %q",
					spelling, system, *age, want, *wantAge, group[0])
			}
		}
	}
}

func TestNormalizeRatedPrefix(t *testing.T) {
	// "Rated R" and the MPAA's full "rated ... for ..." sentence both carry the
	// rating; "Unrated" and "Not Rated" only look like they do.
	cases := []normalizeCase{
		{"Rated R", systemMPAA, 17},
		{"rated r", systemMPAA, 17},
		{"Rated PG-13", systemMPAA, 13},
		{"Rated NC-17", systemMPAA, 18},
		{"Rated R for strong language", systemMPAA, 17},
		{"Rated PG-13 for some violence", systemMPAA, 13},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			system, age, ok := Normalize(tc.raw)
			if !ok || age == nil {
				t.Fatalf("Normalize(%q) = (%q, %v, %v), want an age", tc.raw, system, age, ok)
			}
			if *age != tc.age || system != tc.system {
				t.Errorf("Normalize(%q) = (%q, %d), want (%q, %d)", tc.raw, system, *age, tc.system, tc.age)
			}
		})
	}
}

func TestNormalizeUnrated(t *testing.T) {
	// Recognized, but carrying no age — which is not the same as age 0.
	for _, raw := range []string{
		"NR", "nr", " NR ", "UR", "N/A", "None", "none",
		"Unrated", "unrated", "UNRATED", "Not Rated", "not rated",
		"NotRated", "Unknown", "Not yet rated",
	} {
		t.Run(raw, func(t *testing.T) {
			system, age, ok := Normalize(raw)
			if !ok {
				t.Fatalf("Normalize(%q) not recognized, want %q", raw, systemUnrated)
			}
			if system != systemUnrated {
				t.Errorf("Normalize(%q) system = %q, want %q", raw, system, systemUnrated)
			}
			if age != nil {
				t.Errorf("Normalize(%q) age = %d, want none", raw, *age)
			}
		})
	}
}

func TestNormalizeUnrecognized(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "\t\n", "???", "banana", "Rated", "-", ":", "::",
		"1990", "42", "100", "2024-05-01", "+", "++",
		// Single letters that mean different ages in different countries
		// (Mexico's "A" is all ages, India's is adults only) stay unresolved.
		"A", "B", "C",
	} {
		t.Run("raw="+raw, func(t *testing.T) {
			system, age, ok := Normalize(raw)
			if ok {
				t.Errorf("Normalize(%q) = (%q, %v, true), want not recognized", raw, system, age)
			}
			if age != nil {
				t.Errorf("Normalize(%q) returned age %d for an unrecognized string", raw, *age)
			}
		})
	}
}

func TestNormalizeZeroAgeIsNotUnknown(t *testing.T) {
	// The distinction the whole design rests on: "G" is age 0 and visible under
	// a 0 ceiling; "NR" has no age and is visible under none.
	_, age, ok := Normalize("G")
	if !ok || age == nil || *age != 0 {
		t.Fatalf(`Normalize("G") = (_, %v, %v), want age 0`, age, ok)
	}
	if _, ok := AgeForCeiling("G"); !ok {
		t.Error(`AgeForCeiling("G") not ok, want a usable age-0 ceiling`)
	}
	if _, ok := AgeForCeiling("NR"); ok {
		t.Error(`AgeForCeiling("NR") ok, want no usable age`)
	}
	if !RatingAllowed("G", "G") {
		t.Error(`RatingAllowed("G", "G") = false, want true`)
	}
	if RatingAllowed("NR", "G") {
		t.Error(`RatingAllowed("NR", "G") = true, want false`)
	}
}

func TestNormalizeReturnsIndependentAgePointers(t *testing.T) {
	// The age must be a copy: a caller mutating it cannot be allowed to
	// rewrite the shared ladder.
	_, first, ok := Normalize("PG-13")
	if !ok || first == nil {
		t.Fatal(`Normalize("PG-13") did not resolve`)
	}
	*first = 99
	_, second, ok := Normalize("PG-13")
	if !ok || second == nil {
		t.Fatal(`Normalize("PG-13") did not resolve on the second call`)
	}
	if *second != 13 {
		t.Errorf("ladder was mutated through the returned pointer: got %d, want 13", *second)
	}
}

func TestAgeForCeilingRoundTripsEveryKnownRating(t *testing.T) {
	// Each age-based rating used as a profile ceiling must resolve to its own
	// age, and every rating must admit itself. The US ladders resolve to their
	// tier instead; TestAgeForCeilingAdmitsTheWholeUSTier pins those.
	for _, table := range ratingLadder {
		usTiered := table.system == systemMPAA || table.system == systemUSTV
		for token := range table.ages {
			_, age, ok := Normalize(token)
			if !ok || age == nil {
				t.Errorf("Normalize(%q) did not resolve, but it is in the %s ladder", token, table.system)
				continue
			}
			ceilingAge, ok := AgeForCeiling(token)
			if !ok || ceilingAge == nil {
				t.Errorf("AgeForCeiling(%q) not ok, but it normalizes to age %d", token, *age)
				continue
			}
			if !usTiered && *ceilingAge != *age {
				t.Errorf("AgeForCeiling(%q) = %d, Normalize age = %d", token, *ceilingAge, *age)
			}
			if !RatingAllowed(token, token) {
				t.Errorf("RatingAllowed(%q, %q) = false; a rating must admit itself", token, token)
			}
		}
	}
}

// TestAgeForCeilingAdmitsTheWholeUSTier pins the ceilings profiles already
// carry. Before ages, a US ceiling admitted every US rating of the same rank,
// and both clients label the ceilings that way ("PG-13 / TV-14"): a PG-13
// ceiling that stopped admitting TV-14 would silently hide most series from
// every existing restricted profile.
func TestAgeForCeilingAdmitsTheWholeUSTier(t *testing.T) {
	want := map[string]int{
		"G": 0, "TV-Y": 0, "TV-G": 0,
		"PG": 8, "TV-Y7": 8, "TV-Y7-FV": 8, "TV-PG": 8,
		"PG-13": 14, "TV-14": 14, "US:PG-13": 14,
		"R": 18, "NC-17": 18, "TV-MA": 18, "Rated R": 18,
	}
	for ceiling, wantAge := range want {
		age, ok := AgeForCeiling(ceiling)
		if !ok || age == nil || *age != wantAge {
			t.Errorf("AgeForCeiling(%q) = (%v, %v), want %d", ceiling, age, ok, wantAge)
		}
	}

	tiers := [][]string{
		{"G", "TV-Y", "TV-G"},
		{"PG", "TV-Y7", "TV-PG"},
		{"PG-13", "TV-14"},
		{"R", "NC-17", "TV-MA"},
	}
	for i, tier := range tiers {
		for _, ceiling := range tier {
			for j, other := range tiers {
				for _, rating := range other {
					if got, want := RatingAllowed(rating, ceiling), j <= i; got != want {
						t.Errorf("RatingAllowed(%q, %q) = %v, want %v", rating, ceiling, got, want)
					}
				}
			}
		}
	}
}

func TestAgeForCeilingUnusable(t *testing.T) {
	// "No usable age limit" covers empty, explicitly unrated and junk alike;
	// callers that treat an empty ceiling as "no ceiling" check that first.
	for _, ceiling := range []string{"", "   ", "NR", "Unrated", "Not Rated", "banana", "1990"} {
		if age, ok := AgeForCeiling(ceiling); ok {
			t.Errorf("AgeForCeiling(%q) = (%v, true), want not ok", ceiling, age)
		}
	}
}

func TestRatingAllowed(t *testing.T) {
	cases := []struct {
		rating  string
		ceiling string
		want    bool
	}{
		// An empty ceiling is no ceiling at all.
		{"NC-17", "", true},
		{"banana", "", true},
		{"", "", true},

		// Within the US ladder.
		{"G", "PG-13", true},
		{"PG", "PG-13", true},
		{"PG-13", "PG-13", true},
		{"R", "PG-13", false},
		{"NC-17", "R", true},
		{"TV-14", "PG-13", true},
		{"TV-MA", "R", true},
		{"TV-Y7", "PG", true},
		{"TV-14", "TV-MA", true},

		// Across systems: the whole point of the age axis.
		{"15", "FSK 16", true},
		{"FSK 18", "15", false},
		{"PG-13", "FSK 16", true},
		{"TV-MA", "FSK 16", false},
		{"12A", "TV-14", true},
		{"MA15+", "R", true},
		// An R ceiling admits its whole tier, NC-17 (18) included, so it
		// admits the other ladders' adult rung too.
		{"R18+", "R", true},
		{"FSK 18", "TV-14", false},
		{"15", "PG-13", false},
		{"AL", "L", true},

		// A rating or ceiling with no age blocks.
		{"NR", "R", false},
		{"banana", "R", false},
		{"R", "NR", false},
		{"R", "banana", false},
		{"", "R", false},

		// A whitespace-only ceiling is a SET ceiling nothing resolves under,
		// not an absent one: max_content_rating is free text on both profile
		// APIs, and a stored " " must keep blocking rather than admit the
		// whole catalog.
		{"G", " ", false},
		{"G", "\t", false},
		{"NR", " ", false},
	}
	for _, tc := range cases {
		if got := RatingAllowed(tc.rating, tc.ceiling); got != tc.want {
			t.Errorf("RatingAllowed(%q, %q) = %v, want %v", tc.rating, tc.ceiling, got, tc.want)
		}
	}
}

func TestRatingRankMatchesLegacyUSLadder(t *testing.T) {
	// These ranks predate ages and are pinned: requests' TMDB ceiling mapping
	// is built from them. Ages may only be chosen so that these still come out.
	want := map[string]int{
		"G": 0, "TV-Y": 0, "TV-G": 0,
		"PG": 1, "TV-Y7": 1, "TV-PG": 1,
		"PG-13": 2, "TV-14": 2,
		"R": 3, "NC-17": 3, "TV-MA": 3,
	}
	for rating, wantRank := range want {
		rank, ok := RatingRank(rating)
		if !ok {
			t.Errorf("RatingRank(%q) not ok", rating)
			continue
		}
		if rank != wantRank {
			t.Errorf("RatingRank(%q) = %d, want %d", rating, rank, wantRank)
		}
	}

	if _, ok := RatingRank("NR"); ok {
		t.Error(`RatingRank("NR") ok, want no rank for an unrated string`)
	}
	if _, ok := RatingRank("banana"); ok {
		t.Error(`RatingRank("banana") ok, want no rank`)
	}
	if _, ok := RatingRank(""); ok {
		t.Error(`RatingRank("") ok, want no rank`)
	}
}

func TestRatingRankAcceptsOtherSystems(t *testing.T) {
	// Non-US ceilings used to have no rank at all, which made requests skip the
	// TMDB pre-filter. They now bucket by age.
	cases := map[string]int{
		"U": 0, "FSK 0": 0, "AL": 0, "L": 0,
		"FSK 6": 1, "12": 1, "12A": 1, "PG12": 1,
		"15": 2, "FSK 16": 2, "MA15+": 2, "16+": 2,
		"18": 3, "R18+": 3, "X18+": 3,
	}
	for rating, want := range cases {
		rank, ok := RatingRank(rating)
		if !ok {
			t.Errorf("RatingRank(%q) not ok", rating)
			continue
		}
		if rank != want {
			t.Errorf("RatingRank(%q) = %d, want %d", rating, rank, want)
		}
	}
}

// TestRatingRankIsASupersetOfRatingAllowed guards the invariant
// requests.certificationCeilingFor documents: its TMDB mapping is keyed on
// RatingRank(ceiling) and must cover everything RatingAllowed permits at that
// ceiling, because the TMDB pre-filter cannot resurrect titles it omitted.
// Since rank buckets are monotone in age, every allowed rating must rank at or
// below its ceiling.
func TestRatingRankIsASupersetOfRatingAllowed(t *testing.T) {
	ratings := []string{
		"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG", "PG-13", "TV-14",
		"R", "NC-17", "TV-MA", "U", "12", "12A", "15", "18", "R18",
		"FSK 0", "FSK 6", "FSK 12", "FSK 16", "FSK 18", "Tous publics",
		"M", "MA15+", "R18+", "X18+", "AL", "9", "14", "PG12", "R15+",
		"APTA", "7", "X", "L", "10", "13", "16+", "DE:16", "US:PG-13", "gb:15",
	}
	for _, ceiling := range ratings {
		ceilingRank, ok := RatingRank(ceiling)
		if !ok {
			continue
		}
		for _, rating := range ratings {
			if !RatingAllowed(rating, ceiling) {
				continue
			}
			rank, ok := RatingRank(rating)
			if !ok {
				t.Errorf("RatingAllowed(%q, %q) is true but %q has no rank", rating, ceiling, rating)
				continue
			}
			if rank > ceilingRank {
				t.Errorf("rank inversion: %q (rank %d) allowed under %q (rank %d)",
					rating, rank, ceiling, ceilingRank)
			}
		}
	}
}

func TestCompatibleCeilings(t *testing.T) {
	cases := []struct {
		ceiling string
		want    []string
	}{
		// No ceiling means no predicate at all.
		{"", nil},
		// A ceiling with no usable age is compatible with nothing. Whitespace
		// is one of those, not an absent ceiling (see HasCeiling).
		{"NR", []string{}},
		{"banana", []string{}},
		{"   ", []string{}},

		{"G", []string{"G", "TV-Y", "TV-G", "0"}},
		{"TV-Y7", []string{"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG", "0", "1", "2", "3", "4", "5", "6", "7", "8"}},
		{"PG-13", []string{
			"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG", "PG-13", "TV-14",
			"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14",
		}},
		{"R", []string{
			"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG", "PG-13", "TV-14", "R", "NC-17", "TV-MA",
			"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14",
			"15", "16", "17", "18",
		}},
		// An age ceiling below a US tier's top excludes that whole tier.
		{"FSK 16", []string{
			"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG", "PG-13", "TV-14",
			"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16",
		}},
		{"13", []string{
			"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG",
			"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
		}},
	}
	for _, tc := range cases {
		got := CompatibleCeilings(tc.ceiling)
		if tc.want == nil {
			if got != nil {
				t.Errorf("CompatibleCeilings(%q) = %v, want nil", tc.ceiling, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("CompatibleCeilings(%q) = %v, want %v", tc.ceiling, got, tc.want)
		}
	}
}

// TestCompatibleCeilingsNeverWiden checks the property peer matching relies
// on: every rating a compatible ceiling admits, the viewer's ceiling admits
// too.
func TestCompatibleCeilingsNeverWiden(t *testing.T) {
	ratings := []string{
		"G", "TV-Y", "TV-G", "PG", "TV-Y7", "TV-PG", "PG-13", "TV-14",
		"R", "NC-17", "TV-MA", "U", "12A", "15", "18", "FSK 16", "MA15+",
		"AL", "L", "16+", "DE:16", "gb:15", "NR", "banana",
	}
	for _, ceiling := range ratings {
		for _, peer := range CompatibleCeilings(ceiling) {
			for _, rating := range ratings {
				if RatingAllowed(rating, peer) && !RatingAllowed(rating, ceiling) {
					t.Errorf("peer ceiling %q admits %q, which viewer ceiling %q does not", peer, rating, ceiling)
				}
			}
		}
	}
}

func TestLadderAgesAreConsistentAcrossSystems(t *testing.T) {
	// A token several ladders share resolves to whichever system is listed
	// first, so the priority may only decide the reported system name. If two
	// systems ever disagree on a shared token's age, the age a title gets would
	// depend on table order.
	ages := map[string]struct {
		system string
		age    int
	}{}
	for _, table := range ratingLadder {
		for token, age := range table.ages {
			prior, seen := ages[token]
			if seen && prior.age != age {
				t.Errorf("token %q is age %d in %s but age %d in %s; the ladder order would decide",
					token, prior.age, prior.system, age, table.system)
				continue
			}
			if !seen {
				ages[token] = struct {
					system string
					age    int
				}{system: table.system, age: age}
			}
		}
	}
}

// TestStoredRating pins the value written into content_rating_age. A rating
// with no age stores NULL, which is what the backfill migration left on those
// rows; content_rating keeps the raw string either way.
func TestStoredRating(t *testing.T) {
	cases := []struct {
		raw string
		age *int
	}{
		{"PG-13", intPtr(13)},
		{"tv-ma", intPtr(17)},
		{"FSK 16", intPtr(16)},
		{"DE:16", intPtr(16)},
		{"12A", intPtr(12)},
		{"Rated R for strong language", intPtr(17)},
		{"16+", intPtr(16)},
		{"NR", nil},
		{"Unrated", nil},
		{"Rated NR", nil},
		{"", nil},
		{"   ", nil},
		{"-", nil},
		{"DE:", nil},
		// Text no ladder reads is stored above every ceiling, so "allow"
		// for unrated titles can never admit it.
		{"Contains mild peril", intPtr(UnrecognizedRatingAge)},
		{"MA", intPtr(UnrecognizedRatingAge)},
		{"A", intPtr(UnrecognizedRatingAge)},
		{"1990", intPtr(UnrecognizedRatingAge)},
	}
	for _, tc := range cases {
		age := StoredRating(tc.raw)
		switch {
		case tc.age == nil && age != nil:
			t.Errorf("StoredRating(%q) age = %d, want nil", tc.raw, *age)
		case tc.age != nil && age == nil:
			t.Errorf("StoredRating(%q) age = nil, want %d", tc.raw, *tc.age)
		case tc.age != nil && *age != *tc.age:
			t.Errorf("StoredRating(%q) age = %d, want %d", tc.raw, *age, *tc.age)
		}
	}
}

// TestStricterCeilingOnlyTightens pins the combination rule the policy scope
// resolver relies on: an override can never widen a ceiling, and a ceiling that
// resolves to no age at all wins because it blocks everything.
func TestStricterCeilingOnlyTightens(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", ""},
		{"PG-13", "", "PG-13"},
		{"", "PG-13", "PG-13"},
		// Only "" is "no ceiling" (see HasCeiling): whitespace is a set
		// ceiling that resolves to nothing, so it blocks and therefore wins.
		{"  ", "PG", "  "},
		{"PG", "  ", "  "},
		{"PG", "PG-13", "PG"},
		{"PG-13", "PG", "PG"},
		// Cross-system: FSK 16 (16) is stricter than BBFC 18.
		{"18", "FSK 16", "FSK 16"},
		{"FSK 16", "18", "FSK 16"},
		// The age ceilings the profile UI offers are exactly the values the old
		// US-only rank table could not rank, and they must still tighten.
		{"PG", "15", "PG"},
		{"15", "PG", "PG"},
		{"TV-14", "12", "12"},
		// An unusable ceiling blocks everything, so it is the strictest value.
		{"PG-13", "banana", "banana"},
		{"banana", "PG-13", "banana"},
		{"PG-13", "NR", "NR"},
		{"banana", "NR", "banana"},
	}
	for _, tc := range cases {
		if got := StricterCeiling(tc.a, tc.b); got != tc.want {
			t.Errorf("StricterCeiling(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestStoredRatingAgreesWithRatingAllowed checks the stored column and the
// in-process check cannot disagree: a ceiling admits exactly the ratings whose
// stored age is at or below the ceiling's age, which is what the SQL predicate
// built from those columns evaluates.
func TestStoredRatingAgreesWithRatingAllowed(t *testing.T) {
	ratings := []string{"G", "TV-Y7", "PG", "12A", "PG-13", "TV-14", "15", "FSK 16", "R", "NC-17", "18", "NR", "junk"}
	for _, ceiling := range []string{"G", "PG", "PG-13", "R", "15", "FSK 16", "DE:16"} {
		ceilingAge, ok := AgeForCeiling(ceiling)
		if !ok {
			t.Fatalf("AgeForCeiling(%q) reported no age", ceiling)
		}
		for _, rating := range ratings {
			age := StoredRating(rating)
			stored := age != nil && *age <= *ceilingAge
			if got := RatingAllowed(rating, ceiling); got != stored {
				t.Errorf("ceiling %q, rating %q: RatingAllowed = %v, stored-age comparison = %v",
					ceiling, rating, got, stored)
			}
		}
	}
}

func intPtr(value int) *int { return &value }

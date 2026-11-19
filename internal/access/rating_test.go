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

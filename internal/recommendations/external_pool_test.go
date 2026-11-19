package recommendations

import (
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

// The adapter must give an outside title the text a library title enriched
// by the TMDB plugin gets, or its vector does not compare with theirs.
func TestExternalDetailEmbeddingText(t *testing.T) {
	movie := &tmdb.MediaDetail{
		MediaType: "movie", Title: "Heat", Year: 1995, Overview: "A thief and a detective.", Tagline: "A Los Angeles crime saga",
		Genres: []string{"Crime", "Drama"}, OriginalLanguage: "EN", OriginCountries: []string{"us"},
		Certifications:      map[string][]string{"US": {"NR", "R", "NC-17"}, "DE": {"16"}},
		USCertification:     "NC-17", // the strictest, for rating guards
		USContentRating:     "R",     // the theatrical one, as the plugin stores it
		ProductionCompanies: []string{"Regency", "Forward Pass"},
		Keywords:            []string{"heist", "Heist", "los angeles", "cop", "robbery", "shootout", "obsession"},
		Cast: []tmdb.MediaCastMember{
			{Name: "Al Pacino", Character: "Vincent Hanna", Order: 0},
			{Name: "Robert De Niro", Character: "Neil McCauley", Order: 1},
			{Name: "Val Kilmer", Character: "Chris Shiherlis", Order: 2},
		},
		Directors: []string{"Michael Mann"},
		Writers:   []string{"Michael Mann"},
	}
	series := &tmdb.MediaDetail{
		MediaType: "series", Title: "Dark", Year: 2017, Overview: "Children vanish.",
		Genres: []string{"Mystery"}, OriginalLanguage: "de", OriginCountries: []string{"DE"},
		Certifications: map[string][]string{"US": {"TV-MA"}}, USCertification: "TV-MA", USContentRating: "TV-MA",
		Networks: []string{"Netflix"}, ProductionCompanies: []string{"Wiedemann & Berg"},
		Cast:      []tmdb.MediaCastMember{{Name: "Louis Hofmann", Character: "Jonas Kahnwald", Order: 0}},
		Directors: []string{"Baran bo Odar"},
		Writers:   []string{"Jantje Friese", "Baran bo Odar"},
	}
	tests := []struct {
		name, mediaType string
		detail          *tmdb.MediaDetail
		want            string
	}{
		{"movie", ExternalMediaMovie, movie, `Crime, Drama movie about A thief and a detective.. Heat (1995). Rated R. "A Los Angeles crime saga". ` +
			`Cast: Al Pacino as Vincent Hanna, Robert De Niro as Neil McCauley, Val Kilmer as Chris Shiherlis. Directed by Michael Mann. Written by Michael Mann. ` +
			`Keywords: heist, los angeles, cop, robbery, shootout. Original language: en. Studios: Regency, Forward Pass. Country: US`},
		// A series' studios are not in the detail; crew sorts by name.
		{"series", ExternalMediaSeries, series, `Mystery TV series about Children vanish.. Dark (2017). Rated TV-MA. ` +
			`Cast: Louis Hofmann as Jonas Kahnwald. Directed by Baran bo Odar. Written by Baran bo Odar, Jantje Friese. ` +
			`Original language: de. Studios: Wiedemann & Berg. Network: Netflix. Country: DE`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddings.BuildEmbeddingText(externalDetailItem(tc.mediaType, tc.detail)); got != tc.want {
				t.Fatalf("text\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSelectExternalSeeds(t *testing.T) {
	titles := map[string]externalSeedTitle{}
	weights := map[string]float64{}
	for i := range 120 {
		id := fmt.Sprintf("m%03d", i)
		titles[id] = externalSeedTitle{key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: i + 1}}
		weights[id] = float64(i)
	}
	weights["m000"] = 0                                                                                  // not positive
	weights["no-tmdb"] = 500                                                                             // no TMDB ID
	titles["dup"] = externalSeedTitle{key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: 120}} // same title as m119
	weights["dup"] = 1000

	seeds := selectExternalSeeds(weights, titles)
	if len(seeds) != externalMaxSeeds {
		t.Fatalf("%d seeds, want %d", len(seeds), externalMaxSeeds)
	}
	if seeds[0].ContentID != "dup" || seeds[1].ContentID != "m118" {
		t.Fatalf("heaviest seeds = %s, %s; want dup (one per title), then m118", seeds[0].ContentID, seeds[1].ContentID)
	}
	for _, s := range seeds {
		if s.ContentID == "m000" || s.ContentID == "no-tmdb" || s.ContentID == "m119" {
			t.Fatalf("seed %s should not be picked", s.ContentID)
		}
	}
}

func TestExternalProfileSetSources(t *testing.T) {
	seeds := make([]ExternalSeed, 40)
	for i := range seeds {
		seeds[i].OriginalLanguage = "en"
	}
	seeds[0].OriginalLanguage, seeds[1].OriginalLanguage = "hi", "hi" // 5%
	seeds[2].OriginalLanguage = "ta"                                  // 2.5%
	got := externalProfileSetSources(seeds)
	want := []string{"set:recent:movie", "set:recent:series",
		"set:lang:hi:recent:movie", "set:lang:hi:top:movie", "set:lang:hi:recent:series", "set:lang:hi:top:series"}
	if !slices.Equal(got, want) {
		t.Fatalf("sets = %v, want %v", got, want)
	}
	for _, key := range got {
		if _, ok := externalSetByKey(key); !ok {
			t.Fatalf("set %q does not parse", key)
		}
	}
	// A profile without seeds still ranks the recent sets.
	if got := externalProfileSetSources(nil); !slices.Equal(got, want[:2]) {
		t.Fatalf("seedless sets = %v, want the recent sets", got)
	}
}

func TestChooseExternalPool(t *testing.T) {
	k := func(n int) ExternalTitleKey { return ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: n} }
	seeded := func(n, seeds int) ExternalPoolTitle {
		t := ExternalPoolTitle{Key: k(n)}
		for i := range seeds {
			t.Seeds = append(t.Seeds, ExternalPoolSeed{SourceKey: strconv.Itoa(i), BestRank: 1})
		}
		return t
	}
	setTitle := ExternalPoolTitle{Key: k(100), InSet: true}
	// Most-named first, as ExternalPool returns them.
	profileA := []ExternalPoolTitle{seeded(1, 5), seeded(2, 4), seeded(3, 3), seeded(4, 1), setTitle}
	profileB := []ExternalPoolTitle{seeded(5, 2), seeded(3, 1), seeded(9, 1), setTitle}
	owned := map[ExternalTitleKey]bool{k(2): true}

	// Each profile keeps 2 list titles, the owned one left out: A keeps 1
	// and 3, not 4; B keeps 5 and 3, not 9. Title 3 is named by both
	// profiles, so it leads the lists.
	sets := []externalSetTitles{{key: "set:recent:movie", titles: []ExternalTitleKey{k(100), k(101)}}}
	chosen, named := chooseExternalPool(sets, [][]ExternalPoolTitle{profileA, profileB}, owned, 2, 100)
	want := []ExternalTitleKey{k(100), k(101), k(3), k(1), k(5)}
	if !slices.Equal(chosen, want) {
		t.Fatalf("chosen = %v, want %v", chosen, want)
	}
	if named[k(4)] != 1 || named[k(3)] != 2 || named[k(2)] != 0 {
		t.Fatalf("named = %v", named)
	}

	// The server cap keeps sets first, then the most-named list titles.
	chosen, _ = chooseExternalPool(sets, [][]ExternalPoolTitle{profileA, profileB}, owned, 2, 4)
	if want := want[:4]; !slices.Equal(chosen, want) {
		t.Fatalf("capped = %v, want %v", chosen, want)
	}

	// Over the cap, unused stored titles go least-named first; under it,
	// none go.
	stored := []ExternalTitleKey{k(4), k(7), k(3)}
	if got := externalExcessTitles(stored, chosen, named, 5); !slices.Equal(got, []ExternalTitleKey{k(7)}) {
		t.Fatalf("excess = %v, want the unnamed title", got)
	}
	if got := externalExcessTitles(stored, chosen, named, 6); got != nil {
		t.Fatalf("excess under the cap = %v", got)
	}
}

// Sets may fill only half the server cap, so language sets cannot starve the
// list titles that are personal to each profile; the language sets share
// their room in proportion to their sizes, best-ranked titles first.
func TestChooseExternalPoolHalvesSets(t *testing.T) {
	k := func(n int) ExternalTitleKey { return ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: n} }
	keys := func(from, n int) []ExternalTitleKey {
		out := make([]ExternalTitleKey, n)
		for i := range out {
			out[i] = k(from + i)
		}
		return out
	}
	sets := []externalSetTitles{
		{key: "set:lang:hi:recent:movie", titles: keys(200, 20)},
		{key: "set:lang:hi:top:movie", titles: append([]ExternalTitleKey{k(1)}, keys(300, 10)...)}, // k(1) is in the recent set
		{key: "set:lang:ta:recent:movie", titles: keys(400, 10)},
		{key: "set:recent:movie", titles: keys(1, 6)},
	}
	var pool []ExternalPoolTitle
	for _, key := range keys(900, 20) {
		pool = append(pool, ExternalPoolTitle{Key: key, Seeds: []ExternalPoolSeed{{SourceKey: "seed", BestRank: 1}}})
	}
	// 28 slots: sets get 14, the recent set 6 of them, and the language sets
	// share 8 as 4, 2 and 2. Before the half rule the 46 set titles took
	// every slot and no list title was kept.
	chosen, _ := chooseExternalPool(sets, [][]ExternalPoolTitle{pool}, nil, 500, 28)
	var want []ExternalTitleKey
	for _, part := range [][]ExternalTitleKey{keys(1, 6), keys(200, 4), keys(300, 2), keys(400, 2), keys(900, 14)} {
		want = append(want, part...)
	}
	if !slices.Equal(chosen, want) {
		t.Fatalf("chosen = %v\nwant %v", chosen, want)
	}

	// Sets that fit leave the rest of their half to list titles.
	chosen, _ = chooseExternalPool(sets[3:], [][]ExternalPoolTitle{pool}, nil, 500, 28)
	if want := append(keys(1, 6), keys(900, 20)...); !slices.Equal(chosen, want) {
		t.Fatalf("chosen = %v\nwant %v", chosen, want)
	}
}

func TestExternalSeedFetchOrder(t *testing.T) {
	k := func(n int) ExternalTitleKey { return ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: n} }
	seeds := func(ns ...int) []ExternalSeed {
		out := make([]ExternalSeed, len(ns))
		for i, n := range ns {
			out[i].Key = k(n)
		}
		return out
	}
	got := externalSeedFetchOrder([]ExternalProfile{{Seeds: seeds(1, 2, 3)}, {Seeds: seeds(4, 1)}})
	if want := []ExternalTitleKey{k(1), k(4), k(2), k(3)}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

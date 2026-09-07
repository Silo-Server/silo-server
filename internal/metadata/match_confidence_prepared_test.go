package metadata

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func TestTitleScoreMatchesOriginalAlgorithm(t *testing.T) {
	titles := []string{
		"", "   ", "!!!", "(Unabridged)", "0", "#0", "1", "999", "1000", "2026",
		"Dungeon In My Closet 2", "Dungeon In My Closet, Book 5", "Dungeon In My Closet",
		"The Dark Tower Book II", "The Dark Tower Book 2", "The Dark Tower Book 3",
		"Dragon Saga Books 1–3", "Dragon Saga Books 1-3", "Dragon Saga Books 2–4",
		"Slaughterhouse-Five", "Slaughterhouse 5", "Malcolm X", "Malcolm 10",
		"進撃の巨人", "進撃の巨人 完全版", "三体", "三体全集", "Café", "Cafe\u0301",
		"Blåbærsyltetøy", "Атака титанов", "الأمير الصغير", "A Man in Full",
		"echo echo storm", "echo storm storm", "echo storm", "abcdefghijk", "abcdefghijkl",
	}
	for _, pair := range productionPairs {
		titles = append(titles, pair.want, pair.got)
	}
	for _, want := range titles {
		for _, candidate := range titles {
			if got, original := TitleScore(want, candidate), originalTitleScore(want, candidate); got != original {
				t.Fatalf("TitleScore(%q, %q) = %v, original = %v", want, candidate, got, original)
			}
		}
	}
}

func TestPreparedTitlePreservesYearTieBoundary(t *testing.T) {
	t.Setenv("SILO_METADATA_MATCH_MIN_SCORE", "")
	words := strings.Fields("amber birch cedar dawn ember forest granite harbor island jasmine kettle lantern meadow nickel ocean pine quartz river silver timber umber valley willow yellow zephyr brook copper desert elm feather garden hazel ivy juniper maple orchard pebble rose stone violet")
	want := strings.Join(words, " ")
	results := []SearchResult{
		{Name: strings.Join(words[:len(words)-1], " "), Year: 1900},
		{Name: strings.Join(words[:len(words)-2], " "), Year: 1999},
		{Name: strings.Join(words[:len(words)-6], " "), Year: 2000},
	}
	first, second, third := originalTitleScore(want, results[0].Name), originalTitleScore(want, results[1].Name), originalTitleScore(want, results[2].Name)
	if first-second <= 0 || first-second >= scoreTieEpsilon || second-third <= scoreTieEpsilon {
		t.Fatalf("fixture does not bracket the year tie tolerance: %v, %v, %v", first, second, third)
	}
	got, ok := selectBestMatchYear(want, 2000, results)
	original, originalOK := originalSelectBestMatchYear(want, 2000, results)
	if !ok || !originalOK || !reflect.DeepEqual(got, original) || got.result.Year != 1999 {
		t.Fatalf("year tie result = %#v/%t, original = %#v/%t", got, ok, original, originalOK)
	}
}

func TestSelectBestMatchYearMatchesOriginalAlgorithm(t *testing.T) {
	wants := []string{"", "!!!", "0", "2026", "進撃の巨人", "Café", "Malcolm X", "Dungeon In My Closet 2", "Dragon Saga Books 1–3", "echo echo storm"}
	results := []SearchResult{
		{Name: "Dungeon In My Closet, Book 5", TitleAliases: []TitleAlias{{Title: "Dungeon In My Closet 2"}}},
		{Name: "   ", OriginalTitle: "進撃の巨人", Year: 2013},
		{Name: "Attack on Titan", TitleAliases: []TitleAlias{{Title: "進撃の巨人"}, {Title: "進撃の巨人 (Light Novel)"}}, Year: 2010},
		{Name: "Café", Year: 2000},
		{Name: "Cafe\u0301", Year: 2026},
		{Name: "Dragon Saga Books 2–4", TitleAliases: []TitleAlias{{Title: "Dragon Saga Books 1-3"}}},
		{Name: "Dragon Saga Books 1-3"},
		{Name: "Malcolm 10", TitleAliases: []TitleAlias{{Title: "Malcolm X"}}},
		{Name: "echo storm storm", Year: 2025},
		{Name: "echo echo storm", Year: 1994},
		{Name: "0"}, {Name: "2026"}, {Name: "!!!"}, {},
	}
	for _, pair := range productionPairs {
		wants = append(wants, pair.want)
		results = append(results, SearchResult{Name: pair.got, Year: 1994}, SearchResult{
			Name: pair.got, Year: 2026, TitleAliases: []TitleAlias{{Title: pair.want}, {Title: pair.got + " (Unabridged)"}},
		})
	}
	for i := range results {
		results[i].ProviderIDs = map[string]string{"fixture": fmt.Sprint(i)}
	}
	rng := rand.New(rand.NewPCG(42, 965))
	for _, threshold := range []string{"", "0.5", "0.9", "1", "invalid"} {
		t.Run("threshold="+threshold, func(t *testing.T) {
			t.Setenv("SILO_METADATA_MATCH_MIN_SCORE", threshold)
			for range 5 {
				rng.Shuffle(len(results), func(i, j int) { results[i], results[j] = results[j], results[i] })
				for _, want := range wants {
					for _, year := range []int{0, 1994, 2026} {
						for _, count := range []int{0, 1, len(results)} {
							got, ok := selectBestMatchYear(want, year, results[:count])
							original, originalOK := originalSelectBestMatchYear(want, year, results[:count])
							if ok != originalOK || !reflect.DeepEqual(got, original) {
								t.Fatalf("want=%q year=%d candidates=%d: got %#v/%t, original %#v/%t", want, year, count, got, ok, original, originalOK)
							}
						}
					}
				}
			}
		})
	}
}

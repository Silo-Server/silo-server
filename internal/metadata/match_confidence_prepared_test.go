package metadata

import (
	"strings"
	"testing"
)

func TestPreparedTitlePreservesYearTieBoundary(t *testing.T) {
	t.Setenv("SILO_METADATA_MATCH_MIN_SCORE", "")
	words := strings.Fields("amber birch cedar dawn ember forest granite harbor island jasmine kettle lantern meadow nickel ocean pine quartz river silver timber umber valley willow yellow zephyr brook copper desert elm feather garden hazel ivy juniper maple orchard pebble rose stone violet")
	want := strings.Join(words, " ")
	results := []SearchResult{
		{Name: strings.Join(words[:len(words)-1], " "), Year: 1900},
		{Name: strings.Join(words[:len(words)-2], " "), Year: 1999},
		{Name: strings.Join(words[:len(words)-6], " "), Year: 2000},
	}
	got, ok := selectBestMatchYear(want, 2000, results)
	if !ok || got.result.Year != 1999 {
		t.Fatalf("year tie result = %#v/%t, want the 1999 candidate", got, ok)
	}
}

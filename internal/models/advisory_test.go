package models

import "testing"

// TestAdvisoryColumnsOnlyForMoviesAndSeries pins the type rule at the one write
// boundary: a complete advisory on any other item type, from a plugin or a
// catalog bundle, is stored as NULL, so the profile advisory-age limit can
// never hide an audiobook or ebook.
func TestAdvisoryColumnsOnlyForMoviesAndSeries(t *testing.T) {
	age := 13
	for itemType, stored := range map[string]bool{
		"movie": true, "series": true,
		"audiobook": false, "ebook": false, "podcast": false, "manga": false, "episode": false, "": false,
	} {
		gotAge, gotSource := AdvisoryColumns(itemType, &age, "commonsense")
		if (gotAge != nil) != stored || (gotSource != nil) != stored {
			t.Errorf("%q: AdvisoryColumns() = (%v, %v), want stored %v", itemType, gotAge, gotSource, stored)
		}
	}
}

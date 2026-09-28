package models

import "testing"

// TestAdvisoryColumns pins the all-or-nothing storage rule. "No advisory" has
// to reach the database as NULL in both columns however it arrives, so a
// half-populated item can never produce an age the UI cannot attribute.
func TestAdvisoryColumns(t *testing.T) {
	age := func(v int) *int { return &v }

	tests := []struct {
		name       string
		age        *int
		source     string
		wantAge    *int
		wantSource string
		wantNull   bool
	}{
		{name: "complete pair is stored", age: age(13), source: "commonsense", wantAge: age(13), wantSource: "commonsense"},
		{name: "no age", age: nil, source: "commonsense", wantNull: true},
		{name: "no source", age: age(13), source: "", wantNull: true},
		{name: "zero age", age: age(0), source: "commonsense", wantNull: true},
		{name: "negative age", age: age(-1), source: "commonsense", wantNull: true},
		{name: "neither", age: nil, source: "", wantNull: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAge, gotSource := AdvisoryColumns("movie", tt.age, tt.source)
			if tt.wantNull {
				if gotAge != nil || gotSource != nil {
					t.Fatalf("AdvisoryColumns() = (%v, %v), want both nil", gotAge, gotSource)
				}
				return
			}
			if gotAge == nil || *gotAge != *tt.wantAge {
				t.Fatalf("age = %v, want %v", gotAge, *tt.wantAge)
			}
			if gotSource == nil || *gotSource != tt.wantSource {
				t.Fatalf("source = %v, want %q", gotSource, tt.wantSource)
			}
		})
	}
}

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

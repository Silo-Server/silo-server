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
			gotAge, gotSource := AdvisoryColumns(tt.age, tt.source)
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

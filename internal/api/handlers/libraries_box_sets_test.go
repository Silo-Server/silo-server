package handlers

import "testing"

func TestValidateBoxSetSettings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		typ       string
		enabled   bool
		minMovies int
		wantErr   bool
	}{
		{"movies on", "movies", true, 0, false},
		{"mixed on with minimum", "mixed", true, 3, false},
		{"series on", "series", true, 0, true},
		{"series off", "series", false, 0, false},
		{"minimum below range", "movies", true, 1, true},
		{"minimum above range", "movies", true, 11, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled, minMovies := tc.enabled, tc.minMovies
			if err := validateBoxSetSettings(tc.typ, &enabled, &minMovies); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateBoxSetUpdate(t *testing.T) {
	t.Run("turning on for a series library is refused", func(t *testing.T) {
		req := LibraryUpdateRequest{BoxSetsEnabled: new(true)}
		if err := validateBoxSetUpdate("series", false, &req); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("retyping a library away from movies turns box sets off", func(t *testing.T) {
		req := LibraryUpdateRequest{}
		if err := validateBoxSetUpdate("series", true, &req); err != nil {
			t.Fatal(err)
		}
		if req.BoxSetsEnabled == nil || *req.BoxSetsEnabled {
			t.Fatalf("box_sets_enabled = %v, want false", req.BoxSetsEnabled)
		}
	})
	t.Run("movie library keeps its setting", func(t *testing.T) {
		req := LibraryUpdateRequest{BoxSetMinMovies: new(3)}
		if err := validateBoxSetUpdate("movies", true, &req); err != nil {
			t.Fatal(err)
		}
		if req.BoxSetsEnabled != nil {
			t.Fatalf("box_sets_enabled = %v, want unchanged", *req.BoxSetsEnabled)
		}
	})
}

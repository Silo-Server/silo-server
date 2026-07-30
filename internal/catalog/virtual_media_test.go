package catalog

import "testing"

func TestValidateVirtualMedia(t *testing.T) {
	tests := []struct {
		name    string
		input   VirtualMedia
		wantErr bool
	}{
		{"movie", VirtualMedia{LibraryID: "1", MediaType: "movie", Title: "Example", IMDbID: "tt1", VirtualURI: "aiostreams://movie/tt1"}, false},
		{"series", VirtualMedia{LibraryID: "2", MediaType: "series", Title: "Example", TVDBID: "42", VirtualURI: "aiostreams://series/tt1"}, false},
		{"wrong type", VirtualMedia{LibraryID: "1", MediaType: "show", Title: "Example", IMDbID: "tt1", VirtualURI: "aiostreams://series/tt1"}, true},
		{"non virtual URI", VirtualMedia{LibraryID: "1", MediaType: "movie", Title: "Example", IMDbID: "tt1", VirtualURI: "https://example.test/file"}, true},
		{"missing identity", VirtualMedia{LibraryID: "1", MediaType: "movie", Title: "Example", VirtualURI: "aiostreams://movie/unknown"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVirtualMedia(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestVirtualContentIDPrefersCanonicalProvider(t *testing.T) {
	series := virtualContentID(VirtualMedia{MediaType: "series", TVDBID: "450088", TMDBID: "1", IMDbID: "tt1"})
	if series != "series-tvdb-450088" {
		t.Fatalf("series id=%q", series)
	}
	movie := virtualContentID(VirtualMedia{MediaType: "movie", TMDBID: "878361", IMDbID: "tt12226632"})
	if movie != "movie-tmdb-878361" {
		t.Fatalf("movie id=%q", movie)
	}
}

func TestVirtualLibraryCompatible(t *testing.T) {
	if !virtualLibraryCompatible("movies", "movie") || !virtualLibraryCompatible("series", "series") || !virtualLibraryCompatible("mixed", "movie") {
		t.Fatal("expected compatible library")
	}
	if virtualLibraryCompatible("movies", "series") {
		t.Fatal("series unexpectedly accepted by movies library")
	}
}

func TestRuntimeSeconds(t *testing.T) {
	if got := runtimeSeconds(129); got != 7740 {
		t.Fatalf("runtimeSeconds(129) = %d, want 7740", got)
	}
	if got := runtimeSeconds(0); got != 0 {
		t.Fatalf("runtimeSeconds(0) = %d, want 0", got)
	}
}

func TestNormalizeVirtualReconcileIDsUsesConcreteEmptyArrays(t *testing.T) {
	if got := normalizeVirtualKeepIDs(nil); got == nil || len(got) != 0 {
		t.Fatalf("normalizeVirtualKeepIDs(nil) = %#v, want non-nil empty slice", got)
	}
	if got := normalizeVirtualLibraryIDs(nil); got == nil || len(got) != 0 {
		t.Fatalf("normalizeVirtualLibraryIDs(nil) = %#v, want non-nil empty slice", got)
	}
	keep := []string{"movie-tmdb-1"}
	if got := normalizeVirtualKeepIDs(keep); len(got) != 1 || got[0] != keep[0] {
		t.Fatalf("normalizeVirtualKeepIDs changed non-empty input: %#v", got)
	}
}

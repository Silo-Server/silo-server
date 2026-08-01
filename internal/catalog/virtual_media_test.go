package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func validVirtualMovie() VirtualMedia {
	return VirtualMedia{
		LibraryID:  "1",
		MediaType:  "movie",
		Title:      "Example",
		IMDbID:     "tt1",
		TMDBID:     "1",
		VirtualURI: "virtual://movie/tt1",
	}
}

func validVirtualSeries() VirtualMedia {
	return VirtualMedia{
		LibraryID: "2",
		MediaType: "series",
		Title:     "Example",
		IMDbID:    "tt2",
		TVDBID:    "42",
		Episodes: []VirtualEpisode{{
			SeasonNumber:  1,
			EpisodeNumber: 2,
			Title:         "Episode 2",
			VirtualURI:    "virtual://series/tt2/1/2",
		}},
	}
}

func TestValidateVirtualMediaAcceptsCanonicalPayloads(t *testing.T) {
	tests := []VirtualMedia{
		validVirtualMovie(),
		validVirtualSeries(),
		{
			LibraryID: "1", MediaType: "movie", Title: "Profiles", IMDbID: "tt3",
			Variants: []VirtualMediaVariant{
				{VirtualURI: "virtual://movie/tt3?profile=4K+HDR", Label: "4K HDR"},
				{VirtualURI: "virtual://movie/tt3?profile=1080p&results=all", Label: "More results"},
			},
		},
	}
	for i, input := range tests {
		if err := validateVirtualMedia(input); err != nil {
			t.Fatalf("payload %d rejected: %v", i, err)
		}
	}
}

func TestValidateVirtualMediaRejectsSeriesLevelPlaybackSources(t *testing.T) {
	for name, input := range map[string]VirtualMedia{
		"base URI": {
			LibraryID: "2", MediaType: "series", Title: "Series", TVDBID: "42",
			VirtualURI: "virtual://series/tvdb/42",
		},
		"base variant": {
			LibraryID: "2", MediaType: "series", Title: "Series", TVDBID: "42",
			Variants: []VirtualMediaVariant{{VirtualURI: "virtual://series/tvdb/42?profile=1080p"}},
		},
		"no episodes": {
			LibraryID: "2", MediaType: "series", Title: "Series", TVDBID: "42",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateVirtualMedia(input); err == nil {
				t.Fatal("expected series-level playback source to be rejected")
			}
		})
	}
}

func TestValidateVirtualMediaRejectsUnsafeAndLegacyURIsRecursively(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*VirtualMedia)
	}{
		{"unsupported scheme", func(in *VirtualMedia) { in.VirtualURI = "custom://movie/tt1" }},
		{"http URL", func(in *VirtualMedia) { in.VirtualURI = "https://example.test/file" }},
		{"local path", func(in *VirtualMedia) { in.VirtualURI = "/etc/passwd" }},
		{"wrong media host", func(in *VirtualMedia) { in.VirtualURI = "virtual://series/tt1" }},
		{"userinfo", func(in *VirtualMedia) { in.VirtualURI = "virtual://user@movie/tt1" }},
		{"port", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie:80/tt1" }},
		{"fragment", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie/tt1#fragment" }},
		{"encoded path", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie/tt1%2Fetc" }},
		{"unknown query", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie/tt1?url=https%3A%2F%2Fexample.test" }},
		{"conflicting result query", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie/tt1?result=a&results=all" }},
		{"duplicate query", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie/tt1?profile=a&profile=b" }},
		{"noncanonical query encoding", func(in *VirtualMedia) { in.VirtualURI = "virtual://movie/tt1?profile=4K%20HDR" }},
		{"episode local path", func(in *VirtualMedia) {
			*in = validVirtualSeries()
			in.Episodes[0].VirtualURI = "/etc/passwd"
		}},
		{"episode http variant", func(in *VirtualMedia) {
			*in = validVirtualSeries()
			in.Episodes[0].VirtualURI = ""
			in.Episodes[0].Variants = []VirtualMediaVariant{{VirtualURI: "https://example.test/episode"}}
		}},
		{"episode coordinate mismatch", func(in *VirtualMedia) {
			*in = validVirtualSeries()
			in.Episodes[0].VirtualURI = "virtual://series/tt2/1/3"
		}},
		{"top-level variant wrong scheme", func(in *VirtualMedia) {
			in.VirtualURI = ""
			in.Variants = []VirtualMediaVariant{{VirtualURI: "file:///etc/passwd"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validVirtualMovie()
			tt.mutate(&input)
			err := validateVirtualMedia(input)
			if !errors.Is(err, ErrInvalidVirtualMedia) {
				t.Fatalf("error=%v, want ErrInvalidVirtualMedia", err)
			}
		})
	}
}

func TestValidateVirtualMediaEnforcesCardinalityAndStringBounds(t *testing.T) {
	t.Run("too many variants", func(t *testing.T) {
		input := validVirtualMovie()
		input.VirtualURI = ""
		input.Variants = make([]VirtualMediaVariant, maxVirtualVariantsPerMedia+1)
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("oversized variant list was accepted")
		}
	})
	t.Run("too many episodes", func(t *testing.T) {
		input := validVirtualSeries()
		input.Episodes = make([]VirtualEpisode, maxVirtualEpisodes+1)
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("oversized episode list was accepted")
		}
	})
	t.Run("too many languages", func(t *testing.T) {
		input := validVirtualMovie()
		input.VirtualURI = ""
		input.Variants = []VirtualMediaVariant{{
			VirtualURI:     "virtual://movie/tt1?profile=1080p",
			AudioLanguages: make([]string, maxVirtualLanguages+1),
		}}
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("oversized language list was accepted")
		}
	})
	t.Run("oversized URI", func(t *testing.T) {
		input := validVirtualMovie()
		input.VirtualURI = "virtual://movie/" + strings.Repeat("a", maxVirtualURIBytes)
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("oversized URI was accepted")
		}
	})
	t.Run("oversized title", func(t *testing.T) {
		input := validVirtualMovie()
		input.Title = strings.Repeat("a", maxVirtualTitleBytes+1)
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("oversized title was accepted")
		}
	})
	t.Run("negative file size", func(t *testing.T) {
		input := validVirtualMovie()
		input.VirtualURI = ""
		input.Variants = []VirtualMediaVariant{{VirtualURI: "virtual://movie/tt1", FileSize: -1}}
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("negative file size was accepted")
		}
	})
	t.Run("control character", func(t *testing.T) {
		input := validVirtualMovie()
		input.Title = "bad\x00title"
		if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
			t.Fatal("control character was accepted")
		}
	})
}

func TestValidateVirtualMediaRejectsDuplicateURIs(t *testing.T) {
	input := validVirtualMovie()
	input.VirtualURI = ""
	input.Variants = []VirtualMediaVariant{
		{VirtualURI: "virtual://movie/tt1?profile=1080p"},
		{VirtualURI: "virtual://movie/tt1?profile=1080p"},
	}
	if !errors.Is(validateVirtualMedia(input), ErrInvalidVirtualMedia) {
		t.Fatal("duplicate virtual URIs were accepted")
	}
}

func TestValidateCanonicalVirtualURIAcceptsNamespacedFallbackIDs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		uri       string
		mediaType string
		season    int
		episode   int
	}{
		{name: "tmdb movie", uri: "virtual://movie/tmdb/603", mediaType: "movie"},
		{name: "tvdb series episode", uri: "virtual://series/tvdb/393159/3/1", mediaType: "series", season: 3, episode: 1},
		{name: "tmdb series episode", uri: "virtual://series/tmdb/202555/1/2", mediaType: "series", season: 1, episode: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateCanonicalVirtualURI(tt.uri, tt.mediaType, tt.season, tt.episode); err != nil {
				t.Fatalf("validateCanonicalVirtualURI(): %v", err)
			}
		})
	}
}

func TestValidateCanonicalVirtualURIRejectsInvalidNamespaces(t *testing.T) {
	for _, tt := range []struct {
		uri       string
		mediaType string
		season    int
		episode   int
	}{
		{uri: "virtual://movie/tvdb/603", mediaType: "movie"},
		{uri: "virtual://movie/tmdb/0", mediaType: "movie"},
		{uri: "virtual://movie/not-an-id", mediaType: "movie"},
		{uri: "virtual://series/tvdb/not-numeric/1/1", mediaType: "series", season: 1, episode: 1},
		{uri: "virtual://series/other/123/1/1", mediaType: "series", season: 1, episode: 1},
	} {
		if err := validateCanonicalVirtualURI(tt.uri, tt.mediaType, tt.season, tt.episode); err == nil {
			t.Fatalf("validateCanonicalVirtualURI(%q) succeeded", tt.uri)
		}
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

func TestNormalizeSeriesVirtualMediaCreatesEpisodeSources(t *testing.T) {
	reg := &VirtualMediaRegistrar{}
	t.Run("top-level series URI attaches to episode", func(t *testing.T) {
		input := VirtualMedia{
			LibraryID: "2", MediaType: "series", Title: "Series", TVDBID: "42",
			VirtualURI: "virtual://series/tvdb/42?profile=1080p",
		}
		norm := reg.normalizeSeriesVirtualMedia(context.Background(), input)
		if err := validateVirtualMedia(norm); err != nil {
			t.Fatalf("normalized series failed validation: %v", err)
		}
		if len(norm.Episodes) != 1 {
			t.Fatalf("len(Episodes) = %d, want 1", len(norm.Episodes))
		}
		if norm.Episodes[0].VirtualURI != "virtual://series/tvdb/42/1/1?profile=1080p" {
			t.Fatalf("Episode VirtualURI = %q, want virtual://series/tvdb/42/1/1?profile=1080p", norm.Episodes[0].VirtualURI)
		}
		if norm.VirtualURI != "" {
			t.Fatalf("top-level VirtualURI was not cleared: %q", norm.VirtualURI)
		}
	})

	t.Run("top-level series variants attach to episodes", func(t *testing.T) {
		input := VirtualMedia{
			LibraryID: "2", MediaType: "series", Title: "Series", TVDBID: "42",
			Variants: []VirtualMediaVariant{{VirtualURI: "virtual://series/tvdb/42?profile=1080p", Label: "1080p"}},
			Episodes: []VirtualEpisode{{SeasonNumber: 1, EpisodeNumber: 2, Title: "Episode 2"}},
		}
		norm := reg.normalizeSeriesVirtualMedia(context.Background(), input)
		if err := validateVirtualMedia(norm); err != nil {
			t.Fatalf("normalized series failed validation: %v", err)
		}
		if len(norm.Episodes[0].Variants) != 1 {
			t.Fatalf("len(Episodes[0].Variants) = %d, want 1", len(norm.Episodes[0].Variants))
		}
		if norm.Episodes[0].Variants[0].VirtualURI != "virtual://series/tvdb/42/1/2?profile=1080p" {
			t.Fatalf("Episode Variant VirtualURI = %q, want virtual://series/tvdb/42/1/2?profile=1080p", norm.Episodes[0].Variants[0].VirtualURI)
		}
		if norm.Variants != nil || norm.VirtualURI != "" {
			t.Fatalf("series-level sources were not cleared: VirtualURI=%q, Variants=%#v", norm.VirtualURI, norm.Variants)
		}
	})
}

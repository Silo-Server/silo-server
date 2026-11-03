package naming

import "testing"

func TestParseVariantHints_Edition(t *testing.T) {
	for _, tt := range []struct {
		name       string
		path       string
		wantKey    string
		wantSource string // not checked when empty
	}{
		{"plex folder edition tag", "/movies/Movie (1982) {edition-Final Cut}/Movie (1982) - 2160p.mkv",
			"final_cut", "plex_tag_folder"},
		{"heuristic extended directors cut fan edit", "/movies/Movie (1982)/Movie (1982) Extended Directors Cut Fan Edit 1080p.mkv",
			"extended_director_cut_fan_edit", "heuristic_file"},
		{"composes unrated director cut from release stem", "/movies/alt-cuts/1080p/Borderland (2007)/Borderland.2007.Unrated.Directors.Cut.BluRay.1080p.DTS-HD.MA.5.1.AVC.REMUX-FraMeSToR.mkv",
			"unrated_director_cut", "heuristic_file"},
		{"justice is gray", "/movies/Zack Snyders Justice League Justice Is Gray (2021)/Zack.Snyders.Justice.League.Justice.Is.Gray.2021.2160p.HMAX.WEB-DL.DDP5.1.Atmos.HDR.HEVC-TOMMY.mkv",
			"justice_is_gray", "heuristic_file"},
		{"christmas edition title is not an edition", "/movies/The Christmas Edition (1941)/The Christmas Edition (1941) 720p HDTV x264.mkv",
			"", ""},
		{"final cut title words are not an edition", "/movies/Urban Legends Final Cut (2000)/Urban Legends Final Cut (2000) 1080p BluRay x264.mkv",
			"", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hints := ParseVariantHints(tt.path, "movies")
			if hints == nil {
				t.Fatal("expected hints")
			}
			if got := hints.EditionKey; got != tt.wantKey {
				t.Fatalf("EditionKey = %q, want %q", got, tt.wantKey)
			}
			if got := hints.EditionSource; tt.wantSource != "" && got != tt.wantSource {
				t.Fatalf("EditionSource = %q, want %q", got, tt.wantSource)
			}
		})
	}
}

func TestParseVariantHints_MultiEpisodeRangeDottedSeparator(t *testing.T) {
	for _, tt := range []struct {
		name  string
		path  string
		start int
		end   int
	}{
		{"two digits", "/tv/Show Name/Season 01/Show.Name.s01.e01-e02.mkv", 1, 2},
		{"four digits", "/tv/Show Name/Season 23/Show.Name.s23.e1162-e1163.mkv", 1162, 1163},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hints := ParseVariantHints(tt.path, "series")
			if hints == nil {
				t.Fatal("expected hints")
			}
			if got, want := hints.PresentationKind, "multi_episode"; got != want {
				t.Fatalf("PresentationKind = %q, want %q", got, want)
			}
			if got, want := hints.MultiEpisodeStart, tt.start; got != want {
				t.Fatalf("MultiEpisodeStart = %d, want %d", got, want)
			}
			if got, want := hints.MultiEpisodeEnd, tt.end; got != want {
				t.Fatalf("MultiEpisodeEnd = %d, want %d", got, want)
			}
		})
	}
}

func TestStripComparisonSafeEditionSuffix_DoesNotAlterDistinctGreyTitle(t *testing.T) {
	if got, want := StripComparisonSafeEditionSuffix("Fifty Shades of Grey"), "Fifty Shades of Grey"; got != want {
		t.Fatalf("StripComparisonSafeEditionSuffix() = %q, want %q", got, want)
	}
}

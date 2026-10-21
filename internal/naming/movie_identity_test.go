package naming

import "testing"

func TestParseInferMovieStem_PrefersBracketedYearOverInTitleNumber(t *testing.T) {
	tests := []struct {
		name        string
		stem        string
		folderTitle string
		folderYear  int
		wantTitle   string
		wantYear    int
	}{
		{
			name:        "automania 2000",
			stem:        "Automania 2000 (1963) [Remux-1080p 8-bit AVC FLAC 2.0]-NiDO",
			folderTitle: "Automania 2000",
			folderYear:  1963,
			wantTitle:   "Automania 2000",
			wantYear:    1963,
		},
		{
			name:        "bracketed year with square brackets",
			stem:        "Equalizer 2000 [1987] [Remux-1080p AVC DTS-HD MA 2.0]-GROUP",
			folderTitle: "Equalizer 2000",
			folderYear:  1987,
			wantTitle:   "Equalizer 2000",
			wantYear:    1987,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stem := parseInferMovieStem(tt.stem, tt.folderTitle, tt.folderYear)
			if stem.Title != tt.wantTitle {
				t.Fatalf("Title = %q, want %q", stem.Title, tt.wantTitle)
			}
			if stem.Year != tt.wantYear {
				t.Fatalf("Year = %d, want %d", stem.Year, tt.wantYear)
			}
		})
	}
}

func TestInferRootAssignments_DoesNotMarkBracketedYearMovieAmbiguous(t *testing.T) {
	roots, _ := InferRootAssignments(
		[]string{
			"/mnt/unionfs/movies/70s/Automania 2000 (1963) {imdb-tt0056841} {tmdb-206998}/Automania 2000 (1963) [Remux-1080p 8-bit AVC FLAC 2.0]-NiDO.mkv",
		},
		"movie",
		1,
		nil,
	)
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1", len(roots))
	}
	if got, want := roots[0].State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := roots[0].Title, "Automania 2000"; got != want {
		t.Fatalf("Title = %q, want %q", got, want)
	}
	if got, want := roots[0].Year, 1963; got != want {
		t.Fatalf("Year = %d, want %d", got, want)
	}
}

func TestInferTitlesCoherent_PunctuationAndStyledNumerals(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
	}{
		{
			name:  "ampersand variant",
			left:  "Cowboys and Aliens",
			right: "Cowboys & Aliens",
		},
		{
			name:  "apostrophe variant",
			left:  "Whats Your Number",
			right: "What's Your Number?",
		},
		{
			name:  "styled numeral variant",
			left:  "Alien 3",
			right: "Alien³",
		},
		{
			name:  "comparison safe edition suffix variant",
			left:  "Zack Snyders Justice League Justice Is Gray",
			right: "Zack Snyder's Justice League",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !InferTitlesCoherent(tt.left, tt.right) {
				t.Fatalf("InferTitlesCoherent(%q, %q) = false, want true", tt.left, tt.right)
			}
		})
	}
}

func TestInferGroupIdentity_StripsComparisonSafeEditionSuffixFromBaseTitle(t *testing.T) {
	group := InferGroupIdentity(
		"/movies/Zack Snyders Justice League Justice Is Gray (2021)/Zack.Snyders.Justice.League.Justice.Is.Gray.2021.2160p.HMAX.WEB-DL.DDP5.1.Atmos.HDR.HEVC-TOMMY.mkv",
		"movies",
		RootAssignment{
			RootPath:     "/movies/Zack Snyders Justice League Justice Is Gray (2021)",
			InferredType: "movie",
		},
	)

	if got, want := group.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := group.BaseTitle, "Zack Snyders Justice League"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
}

package naming

import "testing"

type editionHintCase struct {
	name        string
	filePath    string
	libraryType string
	roots       []string
	wantKey     string
	wantSource  string
}

func TestParseVariantHintsEditionScopingAndPrecedence(t *testing.T) {
	runEditionHintCases(t, []editionHintCase{
		{
			name:        "ignores heuristic edition token above library root",
			filePath:    "/mnt/Remastered/Movies/Alien (1979)/Alien (1979).mkv",
			libraryType: "movies",
			roots:       []string{"/mnt/Remastered/Movies"},
			wantKey:     "",
			wantSource:  "",
		},
		{
			name:        "ignores plex edition tag above library root",
			filePath:    "/mnt/{edition-Extended}/Movies/Alien (1979)/Alien (1979).mkv",
			libraryType: "movies",
			roots:       []string{"/mnt/{edition-Extended}/Movies"},
			wantKey:     "",
			wantSource:  "",
		},
		{
			name:        "ignores library root name itself for flat files",
			filePath:    "/mnt/Remastered/Alien (1979).mkv",
			libraryType: "movies",
			roots:       []string{"/mnt/Remastered"},
			wantKey:     "",
			wantSource:  "",
		},
		{
			name:        "ignores grouping folder heuristic inside library root",
			filePath:    "/movies/Film Noir/Alien (1979)/Alien (1979).mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "",
			wantSource:  "",
		},
		{
			name:        "unrooted path ignores ancestor mount directory",
			filePath:    "/mnt/Uncut/Movies/Alien (1979)/Alien (1979).mkv",
			libraryType: "movies",
			wantKey:     "",
			wantSource:  "",
		},
		{
			name:        "empty filename plex tag does not hide the next one",
			filePath:    "/movies/Alien (1979) {edition-Final Cut}/Alien (1979) {edition- } {edition-Theatrical}.mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "theatrical",
			wantSource:  "plex_tag_file",
		},
		{
			name:        "filename plex tag overrides folder plex tag",
			filePath:    "/movies/Blade Runner (1982) {edition-Final Cut}/Blade Runner (1982) {edition-Theatrical}.mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "theatrical",
			wantSource:  "plex_tag_file",
		},
		{
			name:        "folder plex tag applies across nested disc folder inside root",
			filePath:    "/movies/Blade Runner (1982) {edition-Final Cut}/Disc 1/Blade Runner (1982).mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "final_cut",
			wantSource:  "plex_tag_folder",
		},
		{
			name:        "explicit folder plex tag still beats filename heuristic",
			filePath:    "/movies/Blade Runner (1982) {edition-Final Cut}/Blade Runner (1982) Extended.mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "final_cut",
			wantSource:  "plex_tag_folder",
		},
		{
			name:        "filename heuristic overrides immediate folder heuristic",
			filePath:    "/movies/Blade Runner (1982) Director's Cut/Blade Runner (1982) Theatrical.mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "theatrical",
			wantSource:  "heuristic_file",
		},
		{
			name:        "immediate item folder heuristic applies when filename has none",
			filePath:    "/movies/Blade Runner (1982) Director's Cut/Blade Runner (1982).mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "director_cut",
			wantSource:  "heuristic_folder",
		},
	})
}

// The item folder is the movie or show folder, above any disc or season folder.
func TestParseVariantHintsEditionItemFolder(t *testing.T) {
	runEditionHintCases(t, []editionHintCase{
		{
			name:        "movie folder heuristic applies across disc folder",
			filePath:    "/movies/Blade Runner (1982) Director's Cut/Disc 1/Blade Runner (1982).mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "director_cut",
			wantSource:  "heuristic_folder",
		},
		{
			name:        "movie folder heuristic applies across disc image folders",
			filePath:    "/movies/Blade Runner (1982) Director's Cut/BDMV/STREAM/00001.m2ts",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "director_cut",
			wantSource:  "heuristic_folder",
		},
		{
			name:        "show folder heuristic applies across season folder",
			filePath:    "/tv/Show (2000) Extended/Season 01/Show S01E01.mkv",
			libraryType: "series",
			roots:       []string{"/tv"},
			wantKey:     "extended",
			wantSource:  "heuristic_folder",
		},
		{
			name:        "season folder heuristic applies before show folder",
			filePath:    "/tv/Friends/Season 01 Extended/Friends S01E01.mkv",
			libraryType: "series",
			roots:       []string{"/tv"},
			wantKey:     "extended",
			wantSource:  "heuristic_folder",
		},
		{
			name:        "movie folder named like a disc-image folder is the item folder",
			filePath:    "/movies/Remastered/Backup/Backup.mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "",
			wantSource:  "",
		},
		{
			name:        "yearless filename title words are not its edition",
			filePath:    "/movies/Uncut Gems (2019) Remastered/Uncut Gems.mkv",
			libraryType: "movies",
			roots:       []string{"/movies"},
			wantKey:     "remastered",
			wantSource:  "heuristic_folder",
		},
	})
}

func runEditionHintCases(t *testing.T, tests []editionHintCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseVariantHints(tc.filePath, tc.libraryType, tc.roots...)
			if got.EditionKey != tc.wantKey || got.EditionSource != tc.wantSource {
				t.Fatalf("ParseVariantHints(%q) = (key=%q, source=%q), want (key=%q, source=%q)",
					tc.filePath, got.EditionKey, got.EditionSource, tc.wantKey, tc.wantSource)
			}
		})
	}
}

package naming

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestMovieFolderBracketedYearPreservesTechnicalWords(t *testing.T) {
	for _, title := range []string{"The Lantern Opus", "Voyage 4K", "The UHD Journey", "The HDR Chronicle", "The Blu-ray Companion"} {
		for _, providerTag := range []string{"", " {tmdb-12345}"} {
			t.Run(title+providerTag, func(t *testing.T) {
				folder := fmt.Sprintf("%s (2014)%s", title, providerTag)
				gotTitle, year, trusted := parseInferFolderTitleYear(folder)
				if gotTitle != title || year != 2014 || !trusted {
					t.Fatalf("folder = %q (%d), trusted=%v; want %q (2014), trusted", gotTitle, year, trusted, title)
				}
				path := filepath.Join("/movies", folder, title+" (2014) [1080p H264].mkv")
				ctx := ResolvePathContext(path, "movies", "/movies")
				if ctx.Title != title || ctx.Year != 2014 {
					t.Fatalf("context = %+v, want trusted folder title and year", ctx)
				}
				roots, assignments := InferRootAssignments([]string{path}, "movies", 1, nil, "/movies")
				if len(roots) != 1 || roots[0].Title != title || roots[0].Year != 2014 || roots[0].State != "resolved" {
					t.Fatalf("roots = %+v, want resolved trusted folder identity", roots)
				}
				group := InferGroupIdentity(path, "movies", assignments[path])
				if group.BaseTitle != title || group.BaseYear != 2014 || group.State != "resolved" {
					t.Fatalf("group = %+v, want resolved trusted folder identity", group)
				}
			})
		}
	}
}

func TestMovieFolderIdentityRequiresDatedFilenameContradiction(t *testing.T) {
	tests := []struct {
		name       string
		filename   string
		wantState  string
		wantStrong bool
	}{
		{
			name:      "undated release uses trusted folder",
			filename:  "Lantern.Voyage.Special.Broadcast.1080p.HDTV.x264-GROUP.mkv",
			wantState: "resolved",
		},
		{
			name:      "undated generic export uses trusted folder",
			filename:  "Local.Export.1080p.H264.mkv",
			wantState: "resolved",
		},
		{
			name:       "different title and year remain ambiguous",
			filename:   "Another.Story.2012.1080p.BluRay.mkv",
			wantState:  "ambiguous",
			wantStrong: true,
		},
		{
			name:       "different title with same year remains ambiguous",
			filename:   "Another Story (2008) 1080p.mkv",
			wantState:  "ambiguous",
			wantStrong: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("/movies/Lantern Voyage (2008)", tt.filename)
			roots, assignments := InferRootAssignments([]string{path}, "movies", 1, nil, "/movies")
			assignment := assignments[path]
			if assignment.HasStrongContradiction != tt.wantStrong {
				t.Fatalf("HasStrongContradiction = %v, want %v", assignment.HasStrongContradiction, tt.wantStrong)
			}
			if len(roots) != 1 || roots[0].State != tt.wantState {
				t.Fatalf("roots = %+v, want one %s root", roots, tt.wantState)
			}
			group := InferGroupIdentity(path, "movies", assignment)
			if group.State != tt.wantState || group.BaseTitle != "Lantern Voyage" || group.BaseYear != 2008 {
				t.Fatalf("group = %+v, want %s trusted folder identity", group, tt.wantState)
			}
		})
	}
}

func TestMovieStemDuplicateYearRequiresFolderCorroboration(t *testing.T) {
	tests := []struct {
		name        string
		stem        string
		folderTitle string
		folderYear  int
		wantTitle   string
		wantYear    int
	}{
		{
			name:        "duplicate release year",
			stem:        "Lantern Voyage 2008 (2008) HDTV",
			folderTitle: "Lantern Voyage",
			folderYear:  2008,
			wantTitle:   "Lantern Voyage",
			wantYear:    2008,
		},
		{
			name:        "duplicate release year with square brackets",
			stem:        "Lantern.Voyage.2008.[2008].HDTV",
			folderTitle: "Lantern Voyage",
			folderYear:  2008,
			wantTitle:   "Lantern Voyage",
			wantYear:    2008,
		},
		{
			name:        "numeric title differs from release year",
			stem:        "Voyage 2008 (2017) HDTV",
			folderTitle: "Voyage 2008",
			folderYear:  2017,
			wantTitle:   "Voyage 2008",
			wantYear:    2017,
		},
		{
			name:        "numeric title equals release year",
			stem:        "Voyage 2008 (2008) HDTV",
			folderTitle: "Voyage 2008",
			folderYear:  2008,
			wantTitle:   "Voyage 2008",
			wantYear:    2008,
		},
		{
			name:      "no folder corroboration",
			stem:      "Voyage 2008 (2008) HDTV",
			wantTitle: "Voyage 2008",
			wantYear:  2008,
		},
		{
			name:        "unrelated folder does not strip title number",
			stem:        "Voyage 2008 (2008) HDTV",
			folderTitle: "Another Story",
			folderYear:  2008,
			wantTitle:   "Voyage 2008",
			wantYear:    2008,
		},
		{
			name:        "article difference does not establish duplicate",
			stem:        "The Voyage 2008 (2008) HDTV",
			folderTitle: "Voyage",
			folderYear:  2008,
			wantTitle:   "The Voyage 2008",
			wantYear:    2008,
		},
		{
			name:        "different bare year remains part of title",
			stem:        "Voyage 2007 (2008) HDTV",
			folderTitle: "Voyage",
			folderYear:  2008,
			wantTitle:   "Voyage 2007",
			wantYear:    2008,
		},
		{
			name:        "different bracketed year remains a contradiction",
			stem:        "Voyage 2008 (2007) HDTV",
			folderTitle: "Voyage",
			folderYear:  2008,
			wantTitle:   "Voyage 2008",
			wantYear:    2007,
		},
		{
			name:        "undated folder does not establish duplicate",
			stem:        "Voyage 2008 (2008) HDTV",
			folderTitle: "Voyage",
			wantTitle:   "Voyage 2008",
			wantYear:    2008,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stem := parseInferMovieStem(tt.stem, tt.folderTitle, tt.folderYear)
			if stem.Title != tt.wantTitle || stem.Year != tt.wantYear {
				t.Fatalf("stem = %+v, want %q (%d)", stem, tt.wantTitle, tt.wantYear)
			}
		})
	}
}

func TestMovieFolderRetainsEpisodeContradiction(t *testing.T) {
	path := "/movies/Lantern Voyage (2008)/Another.Show.S01E01.1080p.mkv"
	roots, assignments := InferRootAssignments([]string{path}, "movies", 1, nil, "/movies")
	if !assignments[path].HasStrongContradiction || len(roots) != 1 || roots[0].State != "ambiguous" {
		t.Fatalf("episode pattern lost its contradiction: assignment=%+v roots=%+v", assignments[path], roots)
	}
}

func TestDuplicateMovieYearKeepsRootAndGroupResolved(t *testing.T) {
	path := "/movies/Lantern Voyage (2008)/Lantern Voyage 2008 (2008) HDTV.mkv"
	roots, assignments := InferRootAssignments([]string{path}, "movies", 1, nil, "/movies")
	if assignments[path].HasStrongContradiction || len(roots) != 1 || roots[0].State != "resolved" {
		t.Fatalf("duplicate year contradicts trusted folder: assignment=%+v roots=%+v", assignments[path], roots)
	}
	group := InferGroupIdentity(path, "movies", assignments[path])
	if group.State != "resolved" || group.BaseTitle != "Lantern Voyage" || group.BaseYear != 2008 {
		t.Fatalf("group = %+v, want resolved trusted folder identity", group)
	}
}

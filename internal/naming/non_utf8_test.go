package naming

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// Names in a legacy 8-bit encoding reach naming as fspath-encoded paths.
// Titles come out as display text (the Latin-1 byte 0xe5 reads as "å"), and
// every path in the result keeps the encoding, so it still names the file.

func TestParseFilenameReadsLatin1MovieName(t *testing.T) {
	root := "/media/movies"
	path := fspath.Encode(root + "/B\xe5rd (2020)/B\xe5rd (2020) [Bluray-1080p].mkv")

	hints := ParseFilename(path, "movies", root)
	if hints.Title != "Bård" || hints.Year != 2020 {
		t.Fatalf("ParseFilename = %q (%d), want %q (2020)", hints.Title, hints.Year, "Bård")
	}

	_, assignments := InferRootAssignments([]string{path}, "movies", 1, nil, root)
	assignment, ok := assignments[path]
	if !ok {
		t.Fatalf("no root assignment for %q: %+v", path, assignments)
	}
	if assignment.Title != "Bård" {
		t.Fatalf("assignment title = %q, want %q", assignment.Title, "Bård")
	}
	if want := fspath.Encode(root + "/B\xe5rd (2020)"); assignment.RootPath != want {
		t.Fatalf("assignment root = %q, want the encoded folder %q", assignment.RootPath, want)
	}

	group := InferGroupIdentity(path, "movies", assignment)
	if group.BaseTitle != "Bård" || group.BaseYear != 2020 {
		t.Fatalf("group title = %q (%d), want %q (2020)", group.BaseTitle, group.BaseYear, "Bård")
	}
	if group.State != "resolved" {
		t.Fatalf("group state = %q, want resolved: folder and file name agree", group.State)
	}
	if !utf8.ValidString(group.ContentGroupKey) {
		t.Fatalf("group key %q is not valid UTF-8", group.ContentGroupKey)
	}
	// The Latin-1 name and its UTF-8 spelling are the same title.
	utf8Path := root + "/Bård (2020)/Bård (2020) [WEBDL-1080p].mkv"
	_, utf8Assignments := InferRootAssignments([]string{utf8Path}, "movies", 1, nil, root)
	if other := InferGroupIdentity(utf8Path, "movies", utf8Assignments[utf8Path]); other.ContentGroupKey != group.ContentGroupKey {
		t.Fatalf("group keys differ: %q vs %q", group.ContentGroupKey, other.ContentGroupKey)
	}
}

func TestParseFilenameReadsLatin1EpisodeName(t *testing.T) {
	root := "/media/tv"
	show := root + "/Takk for alt (2023) {imdb-}"
	path := fspath.Encode(show + "/Season 01/Takk for alt (2023) - S01E01 - Takk for alt B\xe5rd Ylvis\xe5ker [WEBDL-1080p][AAC 2.0][x264]-UNGERDAS.mkv")

	ctx := ResolvePathContext(path, "series", root)
	if ctx.Title != "Takk for alt" || ctx.Year != 2023 || ctx.SeasonNum != 1 || ctx.EpisodeNum != 1 {
		t.Fatalf("ResolvePathContext = %q (%d) S%dE%d, want Takk for alt (2023) S1E1", ctx.Title, ctx.Year, ctx.SeasonNum, ctx.EpisodeNum)
	}
	if ctx.RootPath != show {
		t.Fatalf("root = %q, want %q", ctx.RootPath, show)
	}

	suffix := EpisodeTitleSuffix(path, root)
	if want := "Takk for alt Bård Ylvisåker [WEBDL-1080p][AAC 2.0][x264]-UNGERDAS"; suffix != want {
		t.Fatalf("EpisodeTitleSuffix = %q, want %q", suffix, want)
	}
}

func TestExtraTitleFromLatin1Name(t *testing.T) {
	path := fspath.Encode("/media/movies/B\xe5rd (2020)/extras/Bak kulissene p\xe5 settet-behindthescenes.mkv")
	if got, want := ExtraTitleFromFile(path), "Bak kulissene på settet"; got != want {
		t.Fatalf("ExtraTitleFromFile = %q, want %q", got, want)
	}
}

// An escaped byte must read as the letter it displays as wherever the
// heuristics look at letters, so a Latin-1 name classifies like its UTF-8
// spelling. "BåS01E01" has no episode boundary before the "S".
func TestLatin1NameClassifiesLikeUTF8(t *testing.T) {
	root := "/media/mixed"
	for _, name := range []string{"B\xe5S01E01.mkv", "Show/B\xe5S01E01.mkv", "Show (2020)/Show (2020) Bl\xe5S01E02.mkv"} {
		encoded := fspath.Encode(root + "/" + name)
		utf8Path := fspath.Display(encoded)

		got := ResolvePathContext(encoded, "mixed", root)
		want := ResolvePathContext(utf8Path, "mixed", root)
		if got.Type != want.Type || got.HasEpisodePattern != want.HasEpisodePattern || got.Title != want.Title {
			t.Fatalf("%q: ResolvePathContext = %s/%v/%q, UTF-8 spelling gives %s/%v/%q",
				utf8Path, got.Type, got.HasEpisodePattern, got.Title, want.Type, want.HasEpisodePattern, want.Title)
		}
		if !strings.HasPrefix(encoded, got.RootPath) || fspath.Display(got.RootPath) != want.RootPath {
			t.Fatalf("%q: root = %q, want the encoded form of %q", utf8Path, got.RootPath, want.RootPath)
		}

		_, gotAssignments := InferRootAssignments([]string{encoded}, "mixed", 1, nil, root)
		_, wantAssignments := InferRootAssignments([]string{utf8Path}, "mixed", 1, nil, root)
		gotAssignment, wantAssignment := gotAssignments[encoded], wantAssignments[utf8Path]
		if gotAssignment.InferredType != wantAssignment.InferredType || gotAssignment.Title != wantAssignment.Title {
			t.Fatalf("%q: assignment = %s/%q, UTF-8 spelling gives %s/%q",
				utf8Path, gotAssignment.InferredType, gotAssignment.Title, wantAssignment.InferredType, wantAssignment.Title)
		}
		if gotAssignment.FilePath != encoded || !strings.HasPrefix(encoded, gotAssignment.RootPath) || fspath.Display(gotAssignment.RootPath) != wantAssignment.RootPath {
			t.Fatalf("%q: assignment paths = %q, %q; want encoded paths", utf8Path, gotAssignment.FilePath, gotAssignment.RootPath)
		}
	}
}

func TestLatin1EditionKeyMatchesUTF8(t *testing.T) {
	root := "/media/movies"
	encoded := ParseVariantHints(fspath.Encode(root+"/M (2020)/M (2020) {edition-B\xe5rd}.mkv"), "movies", root)
	utf8Hints := ParseVariantHints(root+"/M (2020)/M (2020) {edition-Bård}.mkv", "movies", root)
	if encoded.EditionKey != utf8Hints.EditionKey || encoded.EditionRaw != "Bård" {
		t.Fatalf("edition = %q (%q), want %q (%q)", encoded.EditionKey, encoded.EditionRaw, utf8Hints.EditionKey, "Bård")
	}
}

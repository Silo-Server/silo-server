package naming

import "testing"

func TestParseFolderIDs_BracketedBareImdb(t *testing.T) {
	cases := []struct {
		name, folder, wantImdb string
	}{
		{"square brackets", "17 Blocks (2021) [tt10011226]", "tt10011226"},
		{"curly braces", "Some Show {tt1234567}", "tt1234567"},
		{"8-digit tt", "Movie (2024) [tt12345678]", "tt12345678"},
	}
	for _, c := range cases {
		got := ParseFolderIDs(c.folder)
		if got == nil || got.ImdbID != c.wantImdb {
			t.Errorf("%s: ParseFolderIDs(%q) = %+v, want ImdbID=%q", c.name, c.folder, got, c.wantImdb)
		}
	}
	// must NOT change existing behavior:
	if got := ParseFolderIDs("Movie [imdb-tt1375666]"); got == nil || got.ImdbID != "tt1375666" {
		t.Errorf("structured imdb regressed: %+v", got)
	}
	if got := ParseFolderIDs("Some Movie (2020) [BB]"); got != nil {
		t.Errorf("non-tt bracket tag must not parse as id, got %+v", got)
	}
	if got := ParseFolderIDs("17 Blocks (2021)"); got != nil {
		t.Errorf("no-id folder must return nil, got %+v", got)
	}
}

func TestParseStructuredFolderIDs_BracketedBareImdb(t *testing.T) {
	got := ParseStructuredFolderIDs("17 Blocks (2021) [tt10011226]")
	if got == nil || got.ImdbID != "tt10011226" {
		t.Fatalf("ParseStructuredFolderIDs bare imdb = %+v, want ImdbID tt10011226", got)
	}

	got = ParseStructuredFolderIDs("Some Movie {tmdb-12345} [tt7654321]")
	if got == nil || got.TmdbID != "12345" || got.ImdbID != "tt7654321" {
		t.Fatalf("ParseStructuredFolderIDs mixed ids = %+v, want tmdb+imdb", got)
	}

	got = ParseStructuredFolderIDs("Some Movie [imdb-tt1375666] [tt7654321]")
	if got == nil || got.ImdbID != "tt1375666" {
		t.Fatalf("structured imdb should win over bare imdb, got %+v", got)
	}
}

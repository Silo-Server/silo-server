package naming

import "testing"

// Bare numbers in folder names are never provider IDs. This mirrors
// Jellyfin's path-attribute model: only explicit bracket tags (and
// unambiguous tt-prefixed IMDb ids) carry identity. Titles legitimately end
// in numbers, and a misparsed bare ID becomes a trusted match hint that
// silently mismatches or blocks matching.
func TestParseFolderIDs_BareNumbersAreNeverIDs(t *testing.T) {
	for _, c := range []string{
		// Numeric-only titles (anime and otherwise).
		"86",
		"22 7",
		// Titles ending in a short number.
		"District 9",
		"Apollo 13",
		"Ocean's 11",
		"THX 1138",
		"Stranger Things 4",
		// Season-style directory names.
		"Season 01",
		// Titles ending in a long number that looks like a plausible ID.
		"Beverly Hills 90210",
		// Bare trailing numbers that previously parsed as IDs.
		"Some Show 81189",
		"進撃の巨人 73743",
	} {
		if got := ParseFolderIDs(c); got != nil {
			t.Errorf(`ParseFolderIDs(%q) = %+v, want nil`, c, got)
		}
	}
}

func TestParseFolderIDs_StructuredTagsStillParse(t *testing.T) {
	got := ParseFolderIDs("{tmdb-27205}")
	if got == nil || got.TmdbID != "27205" {
		t.Errorf(`ParseFolderIDs("{tmdb-27205}") = %+v, want TmdbID="27205"`, got)
	}

	got = ParseFolderIDs("Some Show (2010) [tvdbid-81189]")
	if got == nil || got.TvdbID != "81189" {
		t.Errorf(`ParseFolderIDs("Some Show (2010) [tvdbid-81189]") = %+v, want TvdbID="81189"`, got)
	}

	// Trailing bare IMDb ids stay supported — the tt prefix is unambiguous.
	got = ParseFolderIDs("Some Movie (2010) tt1375666")
	if got == nil || got.ImdbID != "tt1375666" {
		t.Errorf(`ParseFolderIDs("Some Movie (2010) tt1375666") = %+v, want ImdbID="tt1375666"`, got)
	}

	// A structured tag for one provider must not swallow a trailing bare IMDb
	// id for another — both merge.
	got = ParseFolderIDs("Some Show (2010) [tvdbid-81189] tt1375666")
	if got == nil || got.TvdbID != "81189" || got.ImdbID != "tt1375666" {
		t.Errorf(`ParseFolderIDs("Some Show (2010) [tvdbid-81189] tt1375666") = %+v, want TvdbID="81189" and ImdbID="tt1375666"`, got)
	}

	// An explicit structured imdb tag still wins over a trailing bare id.
	got = ParseFolderIDs("Some Movie [imdb-tt1111111] tt2222222")
	if got == nil || got.ImdbID != "tt1111111" {
		t.Errorf(`ParseFolderIDs("Some Movie [imdb-tt1111111] tt2222222") = %+v, want ImdbID="tt1111111"`, got)
	}
}

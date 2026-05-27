package naming

import "testing"

func TestParseFolderIDs_NumericTitleIsNotAnID(t *testing.T) {
	// Numeric-only anime titles must NOT be parsed as a bare trailing tvdb/tmdb id.
	if got := ParseFolderIDs("86", "series"); got != nil {
		t.Errorf(`ParseFolderIDs("86","series") = %+v, want nil`, got)
	}
	if got := ParseFolderIDs("22 7", "series"); got != nil {
		t.Errorf(`ParseFolderIDs("22 7","series") = %+v, want nil`, got)
	}

	// A real bare trailing id with title text must still be parsed.
	got := ParseFolderIDs("Some Show 81189", "series")
	if got == nil || got.TvdbID != "81189" {
		t.Errorf(`ParseFolderIDs("Some Show 81189","series") = %+v, want TvdbID="81189"`, got)
	}

	// Structured tags must still win regardless of letters.
	got = ParseFolderIDs("{tmdb-27205}", "movies")
	if got == nil || got.TmdbID != "27205" {
		t.Errorf(`ParseFolderIDs("{tmdb-27205}","movies") = %+v, want TmdbID="27205"`, got)
	}

	// Non-Latin (CJK) title with a trailing real id must still parse —
	// unicode.IsLetter covers kana/kanji, so this is treated as title + id.
	got = ParseFolderIDs("進撃の巨人 73743", "series")
	if got == nil || got.TvdbID != "73743" {
		t.Errorf(`ParseFolderIDs("進撃の巨人 73743","series") = %+v, want TvdbID="73743"`, got)
	}

	// Numeric-only title for a movies library is also not an id.
	if got := ParseFolderIDs("86", "movies"); got != nil {
		t.Errorf(`ParseFolderIDs("86","movies") = %+v, want nil`, got)
	}
}

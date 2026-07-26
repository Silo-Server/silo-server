package catalog

import "testing"

// Real values seen rejected in production: media_items.tmdb_id held TMDB's
// "id-slug" URL form while the presence lookup attached the bare numeric id, so
// the backfill failed and the client kept offering to request titles already in
// the library.
func TestSameTMDBIDAcceptsTheSlugURLForm(t *testing.T) {
	cases := []struct {
		stored string
		want   string
		same   bool
	}{
		{"1931-disney-s-adventures-of-the-gummi-bears", "1931", true},
		{"206709-belascoaran-pi", "206709", true},
		{"1931", "1931", true},
		{" 1931 ", "1931", true},
		// Different titles must still conflict — the slug is decoration, the
		// number is the identity.
		{"1931-disney-s-adventures-of-the-gummi-bears", "19310", false},
		{"206709-belascoaran-pi", "1931", false},
		// Not an id at all: no digits, or digits not followed by a separator.
		{"tt0111161", "1931", false},
		{"12ab", "12", false},
	}
	for _, tc := range cases {
		if got := sameTMDBID(tc.stored, tc.want); got != tc.same {
			t.Errorf("sameTMDBID(%q, %q) = %v, want %v", tc.stored, tc.want, got, tc.same)
		}
	}
}

func TestNormalizeTMDBIDLeavesNonIdentifiersAlone(t *testing.T) {
	if got := normalizeTMDBID("tt0111161"); got != "tt0111161" {
		t.Errorf("normalizeTMDBID mangled a non-numeric id: %q", got)
	}
	if got := normalizeTMDBID(""); got != "" {
		t.Errorf("normalizeTMDBID(%q) = %q", "", got)
	}
}

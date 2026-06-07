package models

import "testing"

func TestPersonKindAudiobookRoles(t *testing.T) {
	cases := []struct {
		kind PersonKind
		want string
	}{
		{PersonKindAuthor, "Author"},
		{PersonKindNarrator, "Narrator"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("%d.String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

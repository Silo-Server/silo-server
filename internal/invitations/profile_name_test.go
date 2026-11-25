package invitations

import (
	"testing"
	"unicode/utf8"
)

// TestProfileNameFromEmail: the default profile name capitalizes the first
// character whole. It catches a name that starts outside ASCII being cut
// mid-character, which the profile insert then refuses and the join fails.
func TestProfileNameFromEmail(t *testing.T) {
	for in, want := range map[string]string{
		"marco@example.com": "Marco",
		"marco":             "Marco",
		"éloise":            "Éloise",
		"张伟":                "张伟",
		"  ":                "",
		"":                  "",
	} {
		got := profileNameFromEmail(in)
		if got != want || !utf8.ValidString(got) {
			t.Errorf("profileNameFromEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

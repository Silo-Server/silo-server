package auth

import "testing"

// looksLikeEmail gates the email-column fallback in
// LocalProvider.Authenticate: only inputs that parse as a bare address may
// trigger the second lookup, so plain usernames keep the exact pre-fallback
// behavior (one lookup, one failure mode).
func TestLooksLikeEmail(t *testing.T) {
	valid := []string{
		"marco@example.com",
		"anna.k+silo@sub.example.co.uk",
		" marco@example.com ", // surrounding whitespace is trimmed
	}
	for _, input := range valid {
		if !looksLikeEmail(input) {
			t.Errorf("looksLikeEmail(%q) = false, want true", input)
		}
	}

	invalid := []string{
		"marco",                       // ordinary username
		"marco@",                      // no domain
		"@example.com",                // no local part
		"Marco <marco@example.com>",   // display-name form, not a bare address
		"marco@example.com, b@x.com",  // address list
		"marco example@example.com x", // trailing junk
		"",
	}
	for _, input := range invalid {
		if looksLikeEmail(input) {
			t.Errorf("looksLikeEmail(%q) = true, want false", input)
		}
	}
}

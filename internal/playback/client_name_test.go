package playback

import (
	"strings"
	"testing"
)

// TestGrantedAppClientNameIsReservedAsStored: the reserved name is judged on
// the value a reader stores (normalized and cut to the stored length), folded
// so that case, spacing, punctuation, invisible characters, fullwidth forms
// and a letter swapped for one from another script cannot slip it past, while
// a name of another length, or one wholly in another script, is not taken.
// Catches padding pushed past the cut, zero-width padding and look-alike
// letters recording another caller as another app.
func TestGrantedAppClientNameIsReservedAsStored(t *testing.T) {
	for name, want := range map[string]bool{
		"Granted App": true,
		"granted-app": true,
		"Granted App" + strings.Repeat(" ", 200) + "x":      true,
		"Granted App" + strings.Repeat("\u200b", 200) + "x": true,
		"Gr\u200banted App": true,
		"Gr\u0430nted App":  true, // Cyrillic a
		"\uff27ranted App":  true, // fullwidth G
		"\u0430\u0431\u0432\u0433\u0434\u0435\u0436\u0437\u0438\u0439": false, // ten Cyrillic letters
		"Granted App 2": false,
		"Silo Web":      false,
	} {
		if got := IsGrantedAppClientName(name); got != want {
			t.Errorf("%q: reserved %v, want %v", name, got, want)
		}
	}
}

// TestDeclaredVersionShape: a version, build or channel a session another
// app's key opened declares is kept only when it looks like one. Catches a
// client name carried in the version into the label beside the reserved name.
func TestDeclaredVersionShape(t *testing.T) {
	for v, want := range map[string]bool{
		"2.4.0": true, "1400": true, "beta-2": true, "1.0+build_5": true,
		"(Silo for Apple TV)": false, "Silo for Apple TV": false, "": false, ".1": false,
		strings.Repeat("1", 33): false,
	} {
		if got := LooksLikeClientVersion(v); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

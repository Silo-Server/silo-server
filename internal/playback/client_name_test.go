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
// Catches padding pushed past the cut, zero-width padding, invisible letters
// and look-alike letters recording another caller as another app, and a real
// client name being dropped.
func TestGrantedAppClientNameIsReservedAsStored(t *testing.T) {
	for name, want := range map[string]bool{
		"Granted App": true,
		"granted-app": true,
		"Granted App" + strings.Repeat(" ", 200) + "x":      true,
		"Granted App" + strings.Repeat("\u200b", 200) + "x": true,
		"Gr\u200banted App":                              true,
		"Gr\u0430nted App":                               true, // Cyrillic a
		"\uff27ranted App":                               true, // fullwidth G
		"Granted\u3164App":                               true, // Hangul filler, a letter that renders as nothing
		"Granted App\u3164":                              true,
		"Granted\uffa0App":                               true, // halfwidth Hangul filler
		"Granted\u115fApp":                               true, // Hangul choseong filler
		"Granted\u1160App":                               true, // Hangul jungseong filler
		"Gr\u0430nt\u0435\u0501 \u0410\u0440\u0440":      true, // six Cyrillic look-alikes
		"Gr\u0430\u043ft\u0435\u0501 \u0410\u0440\u0440": true, // seven
		"\u0261\u0433\u0430\u043f\u0442\u0435\u0501 \u0410\u0440\u0440": true, // every letter a look-alike
		"\u0262\u0280\u1d00\u0274\u1d1b\u1d07\u1d05 \u1d00\u1d18\u1d18": true, // small capitals
		"Gr\u00e1nted \u00c1pp": true, // accents
		"\u0430\u0431\u0432\u0433\u0434\u0435\u0436\u0437\u0438\u0439": false, // ten Cyrillic letters
		"\u03b1\u03b2\u03b3\u03b4\u03b5\u03b6\u03b7\u03b8\u03b9\u03ba": false, // ten Greek letters
		"\u0627\u0628\u062a\u062b\u062c\u062d\u062e\u062f\u0630\u0631": false, // ten Arabic letters
		"\u5149\u5f71\u653e\u6620\u5ba4\u5a92\u4f53\u5e93\u5f71\u9662": false, // ten CJK characters
		"Granted App 2":     false,
		"Silo Web":          false,
		"Silo Apple TV":     false,
		"Silo iPadOS":       false,
		"Silo for Apple TV": false,
		"Silo Android":      false,
		"Silo Android TV":   false,
	} {
		if got := IsGrantedAppClientName(name); got != want {
			t.Errorf("%q: reserved %v, want %v", name, got, want)
		}
	}
}

// TestDeclaredVersionShape: a version or build a session another app's key
// opened declares is kept only when it starts with a digit (or "v" and a
// digit), and a channel only when it is a known one. Catches a client name
// carried in the version, build or channel into the label beside the reserved
// name ("Granted App Silo-for-Apple-TV", "Granted App (Silo-for-Apple-TV)").
func TestDeclaredVersionShape(t *testing.T) {
	for v, want := range map[string]bool{
		"2.4.0": true, "1400": true, "v1.2": true, "1.0+build_5": true, "3.1.0-beta.2": true,
		"(Silo for Apple TV)": false, "Silo-for-Apple-TV": false, "beta-2": false, "v": false, "": false, ".1": false,
		strings.Repeat("1", 33): false,
	} {
		if got := LooksLikeClientVersion(v); got != want {
			t.Errorf("version %q: %v, want %v", v, got, want)
		}
	}
	for c, want := range map[string]bool{"release": true, "TestFlight": true, "beta": true, "Silo-for-Apple-TV": false, "": false} {
		if got := IsKnownClientChannel(c); got != want {
			t.Errorf("channel %q: %v, want %v", c, got, want)
		}
	}
}

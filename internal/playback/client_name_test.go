package playback

import (
	"strings"
	"testing"
)

// TestGrantedAppClientNameIsReservedAsStored: only the reserved name itself
// is reserved, judged as it would be stored: any case, with spaces, controls,
// format characters, marks and default-ignorable characters anywhere and
// padding past the stored length ignored. A look-alike is a different name
// and is kept on purpose: a person's own client can already put any name it
// likes in the lists, and dropping real names to defend a string that proves
// nothing would be a regression. Catches the reserved name slipping past by
// padding or invisible characters, and a real or look-alike name being
// dropped.
func TestGrantedAppClientNameIsReservedAsStored(t *testing.T) {
	for name, want := range map[string]bool{
		// The same string: reserved.
		"Granted App":   true,
		"granted app":   true,
		"GRANTED APP":   true,
		"  Granted App": true,
		"GrantedApp":    true,
		"Granted   App": true,
		"Granted App" + strings.Repeat(" ", 200) + "x":      true, // padding past the stored length
		"Granted App" + strings.Repeat("\u200b", 200) + "x": true, // zero-width padding past it
		"Gr\u200banted App": true, // zero-width space
		"Granted\u00a0App":  true, // no-break space
		"Gran\u00adted App": true, // soft hyphen
		"Grant\u2060ed App": true, // word joiner
		"Granted \u200eApp": true, // direction mark
		"Granted\u3164App":  true, // Hangul filler, a letter that renders as nothing
		"Granted App\u3164": true,
		"Granted\uffa0App":  true, // halfwidth Hangul filler
		"Granted\u115fApp":  true, // Hangul choseong filler
		"Granted\u1160App":  true, // Hangul jungseong filler
		"Gra\u0301nted App": true, // combining mark on its own code point
		"Gran\x00ted App":   true, // NUL inside
		"Gran\x07ted App":   true, // control character inside
		// Kept on purpose: a look-alike is a different name, not this one.
		"Gr\u0430nted App": false, // Cyrillic a, deliberately kept
		"\uff27ranted App": false, // fullwidth G, deliberately kept
		"\u0262\u0280\u1d00\u0274\u1d1b\u1d07\u1d05 \u1d00\u1d18\u1d18": false, // small capitals, deliberately kept
		"Granted A\u2374\u2374": false, // APL rho as p, deliberately kept
		"Grantecl App":          false, // "cl" for "d", deliberately kept
		"Granted-App":           false, // punctuation is part of a name, deliberately kept
		"Gr\u00e1nted App":      false, // a precomposed accented letter is another letter, deliberately kept
		"\u0430\u0431\u0432\u0433\u0434\u0435\u0436\u0437\u0438\u0439": false, // a real ten-letter Cyrillic name
		"\u5bb6\u5ead\u5f71\u9662\u64ad\u653e\u5668App":                false, // seven CJK characters and "App"
		"\u0430\u0431\u0432\u0433\u0434":                               false, // a real five-letter Cyrillic name
		// Real names: kept.
		"Granted App 2":     false,
		"Granted App Pro":   false,
		"The Granted App":   false,
		"Silo Web":          false,
		"Silo Apple TV":     false,
		"Silo iPadOS":       false,
		"Silo Android TV":   false,
		"Silo for Apple TV": false,
		"Jellyfin Web":      false,
		"Findroid":          false,
	} {
		if got := IsGrantedAppClientName(name); got != want {
			t.Errorf("%q: reserved %v, want %v", name, got, want)
		}
	}
}

// TestDeclaredVersionShape: a version or build a session another app's key
// opened declares is kept only as numbers with a couple of known suffixes
// (declaredVersion), and a channel only when it is a known one. Catches a
// client name carried in the version, build or channel into the label beside
// the reserved name ("Granted App 2-Silo-for-Apple-TV", "Granted App
// (Silo-for-Apple-TV)").
func TestDeclaredVersionShape(t *testing.T) {
	for v, want := range map[string]bool{
		"2.4.0": true, "v2.4": true, "2.4.0-beta.1+5": true, "1.0+build5": true, "3.2.1-rc2": true,
		"2024.11.26": true, "1.4.0+g1a2b3c4d": true, "1400": true,
		"2-Silo-for-Apple-TV": false, "1.0+Silo_for_Apple_TV": false, "2.4.0-SiloTV": false,
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

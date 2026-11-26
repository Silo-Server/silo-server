package playback

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Silo-Server/silo-server/internal/clientmetadata"
)

// GrantedAppClientName is the client name the server records for every
// request of a login session another app's key opened, whatever name the app
// declares: such an app could otherwise name its plays after the person's own
// apps in the lists that show who played what. It says that another app made
// the play, not which app. A name another caller declares is dropped when it
// would be stored as one reading as this (IsGrantedAppClientName).
const GrantedAppClientName = "Granted App"

// grantedAppClientKey is GrantedAppClientName's comparison key.
var grantedAppClientKey = []rune(clientNameKey(GrantedAppClientName))

// clientNameLookalikes maps letters that read as one of the reserved name's
// letters (g, r, a, n, t, e, d, p) to that letter. It applies after the NFKD
// fold, lower-casing and the removal of combining marks, so accented Latin
// letters are already plain and capitals already small. Chosen by hand from
// the Unicode confusables data (confusables.txt) for those eight letters: the
// Cyrillic, Greek and Armenian letters that render like them, the Latin small
// capitals, and the Latin-extended letters a reader would take for the plain
// one. It is not complete.
var clientNameLookalikes = map[rune]rune{
	// a
	'\u0430': 'a', // Cyrillic
	'\u03b1': 'a', // Greek
	'\u0251': 'a', // Latin alpha
	'\u1d00': 'a', // small capital
	// d
	'\u0501': 'd', // Cyrillic komi de
	'\u0257': 'd', // d with hook
	'\u0111': 'd', // d with stroke
	'\u1d05': 'd', // small capital
	// e
	'\u0435': 'e', // Cyrillic
	'\u04bd': 'e', // Cyrillic abkhasian che
	'\u03b5': 'e', // Greek
	'\u025b': 'e', // open e
	'\u1d07': 'e', // small capital
	// g
	'\u0261': 'g', // script g
	'\u0262': 'g', // small capital
	'\u0581': 'g', // Armenian
	'\u01e5': 'g', // g with stroke
	// n
	'\u043f': 'n', // Cyrillic
	'\u03b7': 'n', // Greek
	'\u0578': 'n', // Armenian
	'\u057c': 'n', // Armenian
	'\u0274': 'n', // small capital
	// p
	'\u0440': 'p', // Cyrillic
	'\u03c1': 'p', // Greek
	'\u0584': 'p', // Armenian
	'\u1d18': 'p', // small capital
	'\u01a5': 'p', // p with hook
	// r
	'\u0433': 'r', // Cyrillic
	'\u0280': 'r', // small capital
	'\u0580': 'r', // Armenian
	'\u027c': 'r', // r with long leg
	// t
	'\u0442': 't', // Cyrillic
	'\u03c4': 't', // Greek
	'\u1d1b': 't', // small capital
	'\u0167': 't', // t with stroke
}

// IsGrantedAppClientName reports whether a declared client name would be
// stored as one that reads as the reserved name. It judges the value every
// reader stores: the name as clientmetadata.Normalize leaves it (controls
// scrubbed, trimmed, cut to clientmetadata.Name characters), so padding
// pushed past the cut cannot hide it. That value is folded (clientNameKey):
// NFKD, lower case, combining marks and every character that is not a letter
// or digit dropped, default-ignorable code points dropped (the Hangul fillers
// are letters that render as nothing), and known look-alike letters mapped to
// the plain letter (clientNameLookalikes). The name is reserved when the key
// has as many characters as the reserved name's, and each position either
// equals the reserved key's letter or is outside ASCII, with at least one
// position equal.
//
// So it catches case, spacing, punctuation, invisible characters, padding
// past the cut, fullwidth and mathematical forms, accents, and letters the
// table knows. Two things still pass: a spelling that uses no letter the
// table knows and no plain letter of the name, and a name of a different
// length made to look similar.
func IsGrantedAppClientName(name string) bool {
	key := []rune(clientNameKey(clientmetadata.Normalize(name, clientmetadata.Name)))
	if len(key) != len(grantedAppClientKey) {
		return false
	}
	equal := false
	for i, r := range key {
		switch {
		case r == grantedAppClientKey[i]:
			equal = true
		case r < utf8.RuneSelf:
			return false
		}
	}
	return equal
}

// WithoutGrantedAppClientName is name, or "" when it is the reserved name: a
// caller that declares it is recorded with no client name.
func WithoutGrantedAppClientName(name string) string {
	if IsGrantedAppClientName(name) {
		return ""
	}
	return name
}

func clientNameKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return -1
		}
		r = unicode.ToLower(r)
		if plain, ok := clientNameLookalikes[r]; ok {
			return plain
		}
		return r
	}, norm.NFKD.String(name))
}

// maxDeclaredVersionLen bounds a client version or build that a session
// another app's key opened may declare.
const maxDeclaredVersionLen = 32

// LooksLikeClientVersion reports whether a declared version or build looks
// like one: a digit, or "v" and a digit, then letters, digits, '.', '+', '_'
// or '-', at most 32 characters. A session another app's key opened has any
// other value dropped, so a version cannot carry a client name into the label
// beside GrantedAppClientName ("Granted App Silo-for-Apple-TV").
func LooksLikeClientVersion(v string) bool {
	if len(v) > maxDeclaredVersionLen {
		return false
	}
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		rest = v
	}
	if rest == "" || rest[0] < '0' || rest[0] > '9' {
		return false
	}
	for i := 0; i < len(v); i++ {
		if !isVersionByte(v[i]) {
			return false
		}
	}
	return true
}

// isVersionByte reports whether c may appear in a declared version or build:
// an ASCII letter or digit, '.', '+', '_' or '-'.
func isVersionByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '+' || c == '_' || c == '-'
}

// knownClientChannels are the distribution channels a session another app's
// key opened may declare; the full label shows the channel in parentheses, so
// any other value is dropped.
var knownClientChannels = []string{"release", "stable", "beta", "alpha", "rc", "testflight", "debug", "dev", "nightly", "canary", "internal"}

// IsKnownClientChannel reports whether a declared channel is one of
// knownClientChannels, in any case.
func IsKnownClientChannel(c string) bool {
	for _, known := range knownClientChannels {
		if strings.EqualFold(c, known) {
			return true
		}
	}
	return false
}

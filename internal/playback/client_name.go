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
// the play, not which app. No other caller is recorded under it
// (IsGrantedAppClientName).
const GrantedAppClientName = "Granted App"

// grantedAppClientKey is GrantedAppClientName's comparison key.
var grantedAppClientKey = []rune(clientNameKey(GrantedAppClientName))

// IsGrantedAppClientName reports whether a declared client name would be
// stored as one that reads as the reserved name. It judges the value every
// reader stores: the name as clientmetadata.Normalize leaves it (controls
// scrubbed, trimmed, cut to clientmetadata.Name characters), so padding
// pushed past the cut cannot hide it. That value is folded with NFKC and
// lower-cased, and everything but letters and digits is dropped (spaces,
// punctuation, zero-width and other invisible characters). The name is
// reserved when what is left has as many characters as the reserved name's
// key and, position by position, each character either equals the key's or
// is not an ASCII letter or digit, with at least half of them equal.
//
// This catches case, spacing, punctuation and invisible characters, fullwidth
// forms (NFKC folds them), and a letter swapped for one that looks like it
// from another script. It does not catch a name of a different length made to
// look similar, and it lets through a name of the same length wholly in
// another script.
func IsGrantedAppClientName(name string) bool {
	key := []rune(clientNameKey(clientmetadata.Normalize(name, clientmetadata.Name)))
	if len(key) != len(grantedAppClientKey) {
		return false
	}
	equal := 0
	for i, r := range key {
		switch {
		case r == grantedAppClientKey[i]:
			equal++
		case r < utf8.RuneSelf:
			return false
		}
	}
	return equal*2 >= len(key)
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
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, norm.NFKC.String(name))
}

// maxDeclaredVersionLen bounds a client version, build or channel that a
// session another app's key opened may declare.
const maxDeclaredVersionLen = 32

// LooksLikeClientVersion reports whether a declared version, build or
// channel looks like one: a letter or digit, then letters, digits, '.', '+',
// '_' or '-', at most 32 characters. A session another app's key opened has
// any other value dropped, so a version cannot carry a client name into the
// label beside GrantedAppClientName ("Granted App (Silo for Apple TV)").
func LooksLikeClientVersion(v string) bool {
	if v == "" || len(v) > maxDeclaredVersionLen {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '+' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}

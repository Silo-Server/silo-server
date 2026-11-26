package playback

import (
	"strings"
	"unicode"
)

// GrantedAppClientName is the client name the server records for every
// request of a login session another app's key opened, whatever name the app
// declares: such an app could otherwise name its plays after the person's own
// apps in the lists that show who played what. It says that another app made
// the play, not which app. No other caller is recorded under it
// (IsGrantedAppClientName).
const GrantedAppClientName = "Granted App"

// IsGrantedAppClientName reports whether a declared client name is the one
// the server reserves for another app's session. It compares the letters and
// digits alone, in any case, so spacing, punctuation and invisible characters
// do not slip a lookalike past it.
func IsGrantedAppClientName(name string) bool {
	return clientNameKey(name) == clientNameKey(GrantedAppClientName)
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
	}, name)
}

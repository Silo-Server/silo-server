package playback

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/clientmetadata"
)

// GrantedAppClientName is the client name the server records for every
// request of a login session another app's key opened, whatever name the app
// declares: such an app could otherwise name its plays after the person's own
// apps in the lists that show who played what. It says that another app made
// the play, not which app. The name in those lists is what a client says
// about itself, except for a granted app's, which the server sets; a client
// that is not a granted app can still choose a name that looks like this one,
// so the lists are not proof of which app played.
const GrantedAppClientName = "Granted App"

// grantedAppClientKey is GrantedAppClientName's comparison key.
var grantedAppClientKey = clientNameKey(GrantedAppClientName)

// IsGrantedAppClientName reports whether a declared client name is the
// reserved name itself, as it would be stored: its key (clientNameKey of the
// name as clientmetadata.Normalize leaves it) equals the reserved name's key.
// Nothing else is reserved: a different letter, symbol or punctuation mark is
// a different name, however it looks.
func IsGrantedAppClientName(name string) bool {
	return clientNameKey(clientmetadata.Normalize(name, clientmetadata.Name)) == grantedAppClientKey
}

// WithoutGrantedAppClientName is name, or "" when it is the reserved name: a
// caller that declares it is recorded with no client name.
func WithoutGrantedAppClientName(name string) string {
	if IsGrantedAppClientName(name) {
		return ""
	}
	return name
}

// clientNameKey lower-cases a name and drops what renders as nothing or as
// separation: separators (spaces of every kind), format characters, combining
// marks, controls and default-ignorable code points (the Hangul fillers among
// them). Everything else, letters of any script, digits, symbols and
// punctuation, stays as it is; no Unicode normalization is applied.
func clientNameKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Z, unicode.Cf, unicode.Mn, unicode.Me, unicode.Mc, unicode.Cc, unicode.Other_Default_Ignorable_Code_Point) {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
}

// maxDeclaredVersionLen bounds a client version or build that a session
// another app's key opened may declare.
const maxDeclaredVersionLen = 32

// declaredVersion is the shape of a version or build a session another app's
// key opened may declare: an optional "v", one to four dot-separated numbers,
// then at most two suffixes, each introduced by '-', '+' or '.' and each being
// digits only (up to 10), a hexadecimal run of 7 to 12 characters (optionally
// after "g", as git describe writes a commit), or one of a few words
// optionally followed by an optional '.' and up to six digits.
var declaredVersion = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}([-+.]([0-9]{1,10}|g?[0-9a-fA-F]{7,12}|(?i:alpha|beta|rc|dev|pre|snapshot|nightly|build|a|b)\.?[0-9]{0,6})){0,2}$`)

// LooksLikeClientVersion reports whether a declared version or build has the
// declaredVersion shape and at most 32 characters. A session another app's
// key opened has any other value dropped, so a version cannot carry a client
// name into the label beside GrantedAppClientName ("Granted App
// 2-Silo-for-Apple-TV").
func LooksLikeClientVersion(v string) bool {
	return len(v) <= maxDeclaredVersionLen && declaredVersion.MatchString(v)
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

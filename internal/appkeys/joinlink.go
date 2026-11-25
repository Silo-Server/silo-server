package appkeys

import "strings"

// JoinLinkForm is one of the two ways a join link opens the app.
type JoinLinkForm string

// Join link forms.
const (
	JoinLinkHTTPS JoinLinkForm = "https"
	JoinLinkSilo  JoinLinkForm = "silo"
)

// JoinLink is a link that hands an app a server address and an app code:
// "<prefix>v1.<base64url of the server address>.<app code>". Both forms
// carry the same fragment, so the link page's host never receives it.
type JoinLink struct {
	Form    JoinLinkForm
	Address string
	AppCode string
}

// String builds the link. Address must be a canonical server address and
// AppCode a base64url app code.
func (l JoinLink) String() string {
	prefix := joinLinkHTTPS
	if l.Form == JoinLinkSilo {
		prefix = joinLinkScheme
	}
	return prefix + joinLinkVersion + "." + encoding.EncodeToString([]byte(l.Address)) + "." + l.AppCode
}

// ParseJoinLink reads a join link of either form.
func ParseJoinLink(link string) (JoinLink, error) {
	var out JoinLink
	var fragment string
	switch {
	case strings.HasPrefix(link, joinLinkHTTPS):
		out.Form, fragment = JoinLinkHTTPS, link[len(joinLinkHTTPS):]
	case strings.HasPrefix(link, joinLinkScheme):
		out.Form, fragment = JoinLinkSilo, link[len(joinLinkScheme):]
	default:
		return JoinLink{}, fail(CodeJoinPrefix)
	}
	parts := strings.Split(fragment, ".")
	if len(parts) != 3 {
		return JoinLink{}, fail(CodeJoinParts)
	}
	if parts[0] != joinLinkVersion {
		return JoinLink{}, fail(CodeJoinVersion)
	}
	address, err := encoding.DecodeString(parts[1])
	if err != nil {
		return JoinLink{}, fail(CodeJoinAddress)
	}
	canonical, err := CanonicalServerAddress(string(address))
	if err != nil || canonical != string(address) {
		return JoinLink{}, fail(CodeJoinAddress)
	}
	code, err := encoding.DecodeString(parts[2])
	if err != nil || len(code) != AppCodeSize {
		return JoinLink{}, fail(CodeJoinAppCode)
	}
	out.Address, out.AppCode = canonical, parts[2]
	return out, nil
}

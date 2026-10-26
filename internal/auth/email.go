package auth

import (
	"errors"
	netmail "net/mail"
	"net/netip"
	"strings"
)

// ErrInvalidEmail is returned for an address that is not a single, bare
// mailbox with a dotted domain.
var ErrInvalidEmail = errors.New("invalid email address")

// ValidateEmail checks one account or notification address and returns it
// canonicalised (surrounding whitespace removed).
//
// net/mail alone is too permissive for our purposes: it accepts display
// names, comments, and bare hostnames such as "admin@siloserver", which is
// a legal local address but never what someone typing into a sign-up form
// means. So on top of the RFC 5322 parse the address must be exactly what
// was typed (no display name or comments) and the domain must contain a dot
// with something on both sides of it.
func ValidateEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	at := strings.LastIndexByte(trimmed, '@')
	if at < 0 {
		return "", ErrInvalidEmail
	}
	domain := trimmed[at+1:]
	if strings.HasPrefix(domain, "[") {
		// Go 1.27 changed how net/mail reads IPv6 domain literals (it now
		// requires the RFC 5321 "IPv6:" tag and rejects the bare form), so the
		// literal is checked here and net/mail judges only the local part.
		// That keeps the answer the same whichever toolchain built the server,
		// and in step with the web client's copy of this rule.
		if !validDomainLiteral(domain) || !isBareMailbox(trimmed[:at]+"@example.com") {
			return "", ErrInvalidEmail
		}
		return trimmed, nil
	}
	if !isBareMailbox(trimmed) {
		return "", ErrInvalidEmail
	}
	dot := strings.LastIndexByte(domain, '.')
	if dot <= 0 || dot == len(domain)-1 {
		return "", ErrInvalidEmail
	}
	return trimmed, nil
}

// isBareMailbox reports whether net/mail parses addr as exactly itself: a
// valid mailbox with no display name, comments, or angle brackets.
func isBareMailbox(addr string) bool {
	parsed, err := netmail.ParseAddress(addr)
	return err == nil && parsed.Address == addr
}

// validDomainLiteral accepts "[IPv4]" and "[IPv6]", the forms net/mail
// accepted before Go 1.27. The RFC 5321 "IPv6:" tag is refused.
func validDomainLiteral(domain string) bool {
	inner, ok := strings.CutSuffix(domain[1:], "]")
	if !ok {
		return false
	}
	addr, err := netip.ParseAddr(inner)
	return err == nil && addr.Zone() == ""
}

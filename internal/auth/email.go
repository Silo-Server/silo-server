package auth

import (
	"errors"
	netmail "net/mail"
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
	if trimmed == "" {
		return "", ErrInvalidEmail
	}
	parsed, err := netmail.ParseAddress(trimmed)
	if err != nil || parsed.Address != trimmed {
		return "", ErrInvalidEmail
	}
	at := strings.LastIndexByte(trimmed, '@')
	domain := trimmed[at+1:]
	if strings.HasPrefix(domain, "[") {
		// A bracketed IP literal has no dot to require; net/mail already
		// checked its shape.
		return trimmed, nil
	}
	dot := strings.LastIndexByte(domain, '.')
	if dot <= 0 || dot == len(domain)-1 {
		return "", ErrInvalidEmail
	}
	return trimmed, nil
}

package appkeys

import (
	"net/netip"
	"strconv"
	"strings"
)

// Addresses have exactly one spelling. A reader lower-cases an upper-case
// scheme and host and refuses every other spelling instead of rewriting it:
// an IPv4 host that is not four decimal numbers, a host with any character
// outside ASCII (checked before anything is lower-cased), a trailing dot, a
// port with a leading zero or the scheme's default port written out, user
// information, and an IPv6 host with a zone or an embedded IPv4 part.

// CanonicalOrigin returns the origin of an http or https address:
// scheme://host, with :port when the port is not the scheme's default. Any
// path, query or fragment is dropped. An address that isn't in its one
// spelling is refused with CodeNotAnAddress.
func CanonicalOrigin(input string) (string, error) {
	origin, _, err := splitAddress(input)
	return origin, err
}

// isCanonicalOrigin reports whether s is an origin already written in its
// one spelling, with nothing after the authority.
func isCanonicalOrigin(s string) bool {
	origin, err := CanonicalOrigin(s)
	return err == nil && origin == s
}

// CanonicalServerAddress returns the server address form of an http or https
// address: its origin and its path without trailing slashes. The query and
// fragment are dropped. A path with an empty, "." or ".." segment, a
// character a path can't hold unescaped, or an escape that isn't upper-case
// or isn't needed is refused with CodeNotAnAddress.
func CanonicalServerAddress(input string) (string, error) {
	origin, rest, err := splitAddress(input)
	if err != nil {
		return "", err
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimRight(rest, "/")
	if rest == "" {
		return origin, nil
	}
	for _, segment := range strings.Split(rest[1:], "/") {
		if !validPathSegment(segment) {
			return "", fail(CodeNotAnAddress)
		}
	}
	return origin + rest, nil
}

// splitAddress returns the canonical origin of input and what follows the
// authority (a path, query or fragment, or "").
func splitAddress(input string) (string, string, error) {
	for i := 0; i < len(input); i++ {
		if input[i] >= 0x80 {
			return "", "", fail(CodeNotAnAddress)
		}
	}
	scheme, after, ok := strings.Cut(input, "://")
	scheme = strings.ToLower(scheme)
	if !ok || (scheme != "http" && scheme != "https") {
		return "", "", fail(CodeNotAnAddress)
	}
	end := strings.IndexAny(after, "/?#")
	if end < 0 {
		end = len(after)
	}
	authority, rest := after[:end], after[end:]
	host, port, err := canonicalAuthority(authority, scheme)
	if err != nil {
		return "", "", err
	}
	origin := scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	return origin, rest, nil
}

// canonicalAuthority splits host[:port] and returns both in their one
// spelling; the port is "" when it is the default.
func canonicalAuthority(authority, scheme string) (string, string, error) {
	if strings.Contains(authority, "@") {
		return "", "", fail(CodeNotAnAddress)
	}
	hostPart, portPart, hasPort, ok := splitHostPort(authority)
	if !ok {
		return "", "", fail(CodeNotAnAddress)
	}
	host, ok := canonicalHost(hostPart)
	if !ok {
		return "", "", fail(CodeNotAnAddress)
	}
	if !hasPort {
		return host, "", nil
	}
	port, ok := canonicalPort(portPart, scheme)
	if !ok {
		return "", "", fail(CodeNotAnAddress)
	}
	return host, port, nil
}

// splitHostPort splits an authority into its host (an IPv6 host with its
// brackets) and the port after a colon, if any.
func splitHostPort(authority string) (host, port string, hasPort, ok bool) {
	if !strings.HasPrefix(authority, "[") {
		host, port, hasPort = strings.Cut(authority, ":")
		return host, port, hasPort, true
	}
	closing := strings.Index(authority, "]")
	if closing < 0 {
		return "", "", false, false
	}
	host, tail := authority[:closing+1], authority[closing+1:]
	if tail == "" {
		return host, "", false, true
	}
	if !strings.HasPrefix(tail, ":") {
		return "", "", false, false
	}
	return host, tail[1:], true, true
}

// canonicalPort refuses an empty port, a leading zero, a port out of range
// and the scheme's default written out.
func canonicalPort(port, scheme string) (string, bool) {
	if port == "" || port[0] == '0' || len(port) > 5 {
		return "", false
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return "", false
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n > 65535 || (scheme == "http" && n == 80) || (scheme == "https" && n == 443) {
		return "", false
	}
	return port, true
}

// canonicalHost returns an IPv6 literal in its RFC 5952 form, or a name or
// IPv4 address lower-cased.
func canonicalHost(host string) (string, bool) {
	if strings.HasPrefix(host, "[") {
		return canonicalIPv6(host)
	}
	host = strings.ToLower(host)
	if host == "" || len(host) > 253 {
		return "", false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if !validHostLabel(label) {
			return "", false
		}
	}
	if looksNumeric(labels[len(labels)-1]) {
		return host, validIPv4(labels)
	}
	return host, true
}

func canonicalIPv6(host string) (string, bool) {
	literal := host[1 : len(host)-1]
	// A zone ("%25en0") or a dotted IPv4 tail is refused before parsing.
	if strings.ContainsAny(literal, "%.") {
		return "", false
	}
	addr, err := netip.ParseAddr(literal)
	if err != nil || !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
		return "", false
	}
	return "[" + addr.String() + "]", true
}

// validHostLabel accepts a label of 1 to 63 characters from a-z, 0-9, '-'
// and '_', in any position: home network names such as my_nas are real. An
// empty label is a leading, trailing or doubled dot.
func validHostLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// looksNumeric reports whether a host's last label, already lower-cased,
// makes it an IPv4 address to a URL parser: all digits, or 0x followed by
// zero or more hexadecimal digits (a bare 0x reads as zero). Any other label,
// 0xroom included, is a name.
func looksNumeric(label string) bool {
	if rest, ok := strings.CutPrefix(label, "0x"); ok {
		return strings.Trim(rest, "0123456789abcdef") == ""
	}
	return label != "" && strings.Trim(label, "0123456789") == ""
}

// validIPv4 accepts exactly four decimal numbers from 0 to 255 with no
// leading zeros.
func validIPv4(labels []string) bool {
	if len(labels) != 4 {
		return false
	}
	for _, label := range labels {
		if len(label) > 3 || (len(label) > 1 && label[0] == '0') {
			return false
		}
		n, err := strconv.Atoi(label)
		if err != nil || n > 255 || strconv.Itoa(n) != label {
			return false
		}
	}
	return true
}

// validPathSegment accepts a non-empty segment of characters a path holds
// unescaped, with escapes only where needed and written in upper case. A
// segment that is "." or ".." once unescaped is refused.
func validPathSegment(segment string) bool {
	if segment == "" {
		return false
	}
	var decoded strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if c == '%' {
			if i+2 >= len(segment) || !upperHex(segment[i+1]) || !upperHex(segment[i+2]) {
				return false
			}
			v := unhex(segment[i+1])<<4 | unhex(segment[i+2])
			if unreserved(v) {
				return false
			}
			decoded.WriteByte(v)
			i += 2
			continue
		}
		if !unreserved(c) && !subDelimOrColonAt(c) {
			return false
		}
		decoded.WriteByte(c)
	}
	d := decoded.String()
	return d != "." && d != ".."
}

func upperHex(c byte) bool { return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') }

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'A' + 10
}

// unreserved is RFC 3986's unreserved set.
func unreserved(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// subDelimOrColonAt is the rest of RFC 3986's pchar.
func subDelimOrColonAt(c byte) bool {
	return strings.IndexByte("!$&'()*+,;=:@", c) >= 0
}

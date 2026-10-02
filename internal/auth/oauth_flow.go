package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// The pieces of an OAuth flow that are pure functions: the browser-binding
// cookie, the native app's PKCE and state values, the fixed app redirect,
// and the path-only return target (docs/architecture/external-sign-in.md).

// NativeAppRedirectURI is the only redirect a native flow ends on: a
// reverse-DNS private-use scheme (RFC 8252 section 7.1) the iOS, iPadOS,
// macOS and Android apps claim. The client never supplies it.
const NativeAppRedirectURI = "org.siloserver.silo:/auth/callback"

// PKCEMethodS256 is the only code challenge method a native flow accepts.
const PKCEMethodS256 = "S256"

// oauthBinderCookiePrefix names the browser-binding cookies. Each flow has
// its own cookie, keyed by its state, so two sign-ins started in two tabs
// do not overwrite each other's binding.
const oauthBinderCookiePrefix = "silo_oauth_"

// OAuthCompletionCookieName names the cookie that binds a web completion
// code to the browser the callback answered. The callback sets it on the
// complete paths of both API versions; a later web sign-in finishing in the
// same browser replaces it.
const OAuthCompletionCookieName = "silo_oauth_complete"

// oauthCompletionCookieMaxAge is the completion cookie's lifetime: the
// code's plus a minute of clock skew.
const oauthCompletionCookieMaxAge = int((oauthCompletionTTL + time.Minute) / time.Second)

// maxAppStateLength bounds the native app's opaque state value.
const maxAppStateLength = 512

// maxNextLength bounds the return path a flow keeps.
const maxNextLength = 2048

// newOAuthBinder returns a fresh browser binder and the hash stored with
// the flow. 256 random bits, base64url.
func newOAuthBinder() (value, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generating oauth binder: %w", err)
	}
	value = base64.RawURLEncoding.EncodeToString(buf)
	return value, oauthBinderHash(value), nil
}

func oauthBinderHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// oauthBinderMatches compares a presented binder with the stored hash in
// constant time. A flow without a stored hash never matches.
func oauthBinderMatches(presented, storedHash string) bool {
	if presented == "" || storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(oauthBinderHash(presented)), []byte(storedHash)) == 1
}

// oauthCompletionCookie builds the completion cookie for the complete
// operation at completeURL, with the binding cookie's attributes.
func oauthCompletionCookie(value, completeURL string) *http.Cookie {
	cookie := oauthBinderCookie("", value, completeURL, oauthCompletionCookieMaxAge)
	cookie.Name = OAuthCompletionCookieName
	return cookie
}

// OAuthBinderCookieName is the name of the binding cookie of the flow with
// this state.
func OAuthBinderCookieName(state string) string {
	sum := sha256.Sum256([]byte(state))
	return oauthBinderCookiePrefix + hex.EncodeToString(sum[:8])
}

// oauthBinderCookie builds the binding cookie: HttpOnly, SameSite=Lax (the
// provider's redirect back is a top-level GET, which Lax admits), Secure on
// an https public URL, and scoped to the callback path. maxAge < 0 clears
// it.
func oauthBinderCookie(state, value, callbackURL string, maxAge int) *http.Cookie {
	path := "/"
	secure := false
	if u, err := url.Parse(callbackURL); err == nil {
		if u.Path != "" {
			path = u.Path
		}
		secure = strings.EqualFold(u.Scheme, schemeHTTPS)
	}
	return &http.Cookie{
		Name:     OAuthBinderCookieName(state),
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// isPKCEUnreserved reports whether r is in RFC 7636's unreserved set.
func isPKCEUnreserved(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '.', r == '_', r == '~':
		return true
	}
	return false
}

func allPKCEUnreserved(value string) bool {
	for _, r := range value {
		if !isPKCEUnreserved(r) {
			return false
		}
	}
	return true
}

// ValidCodeChallenge reports whether value is an S256 code challenge: the
// unpadded base64url encoding of a SHA-256 digest, 43 characters.
func ValidCodeChallenge(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// ValidCodeVerifier reports whether value is an RFC 7636 code verifier:
// 43 to 128 unreserved characters.
func ValidCodeVerifier(value string) bool {
	return len(value) >= 43 && len(value) <= 128 && allPKCEUnreserved(value)
}

// PKCEChallengeS256 is BASE64URL(SHA256(verifier)).
func PKCEChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidAppState reports whether value can be the native app's state: 1 to
// 512 unreserved characters, so it round-trips through the app redirect
// unchanged.
func ValidAppState(value string) bool {
	return value != "" && len(value) <= maxAppStateLength && allPKCEUnreserved(value)
}

// checkCompletionClient is the check a redemption passes before reuse and
// expiry are looked at: a native code needs its verifier
// (checkCompletionVerifier), a web code the value of the completion cookie
// the callback set in the browser it answered, so a code that reaches
// another browser, say an attacker's own code sent to a victim to sign the
// victim in as the attacker, is refused.
func checkCompletionClient(c OAuthCompletion, verifier, browser string) error {
	if c.Kind == OAuthFlowNative {
		return checkCompletionVerifier(c, verifier)
	}
	if !oauthBinderMatches(browser, c.BrowserHash) {
		return ErrOAuthCompletionBrowser
	}
	return nil
}

// checkCompletionVerifier enforces the PKCE binding of a completion code: a
// native code needs the verifier of its challenge, a web code takes none.
func checkCompletionVerifier(c OAuthCompletion, verifier string) error {
	if c.Kind != OAuthFlowNative {
		if verifier != "" {
			return fmt.Errorf("%w: a web completion code takes no code_verifier", ErrOAuthInvalidGrant)
		}
		return nil
	}
	if verifier == "" {
		return fmt.Errorf("%w: code_verifier required", ErrOAuthInvalidGrant)
	}
	if !ValidCodeVerifier(verifier) ||
		subtle.ConstantTimeCompare([]byte(PKCEChallengeS256(verifier)), []byte(c.CodeChallenge)) != 1 {
		return fmt.Errorf("%w: code_verifier does not match", ErrOAuthInvalidGrant)
	}
	return nil
}

// normalizeOAuthNext keeps next only when it is a same-origin path: it
// starts with one slash and neither it nor any percent-decoded form starts
// with "//" or "/\", contains a backslash or a control character (browsers
// drop tabs and newlines, so "/\t/host" would become "//host"), or parses
// with a scheme, host or user info. Anything else is "/".
func normalizeOAuthNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || len(next) > maxNextLength || !strings.HasPrefix(next, "/") {
		return "/"
	}
	candidate := next
	for range 4 {
		if strings.HasPrefix(candidate, "//") || strings.ContainsRune(candidate, '\\') {
			return "/"
		}
		for _, r := range candidate {
			if r < 0x20 || r == 0x7f {
				return "/"
			}
		}
		decoded, err := url.PathUnescape(candidate)
		if err != nil {
			return "/"
		}
		if decoded == candidate {
			break
		}
		candidate = decoded
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	return next
}

// withQuery adds key=value pairs to a path-only URL, keeping its query and
// fragment.
func withQuery(path string, pairs ...string) string {
	u, err := url.Parse(path)
	if err != nil {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	for i := 0; i+1 < len(pairs); i += 2 {
		q.Set(pairs[i], pairs[i+1])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// URL schemes the origin checks compare.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// sameOrigin reports whether scheme://host names the origin of base.
// Hosts compare case-insensitively with default ports made explicit.
func sameOrigin(scheme, host string, base *url.URL) bool {
	if scheme == "" || base == nil || !strings.EqualFold(scheme, base.Scheme) {
		return false
	}
	return hostWithPort(host, scheme) == hostWithPort(base.Host, base.Scheme)
}

func hostWithPort(host, scheme string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name = strings.Trim(host, "[]")
		port = "80"
		if strings.EqualFold(scheme, schemeHTTPS) {
			port = "443"
		}
	}
	if addr, err := netip.ParseAddr(name); err == nil {
		name = addr.String()
	}
	return net.JoinHostPort(name, port)
}

// providerErrorReason maps an OAuth error the provider redirected back with
// (RFC 6749 section 4.1.2.1) to a sign-in failure reason.
func providerErrorReason(code string) string {
	switch code {
	case "access_denied", "login_required", "interaction_required",
		"consent_required", "account_selection_required":
		return OAuthReasonNotPermitted
	case "temporarily_unavailable", "server_error":
		return OAuthReasonProviderUnavailable
	}
	return OAuthReasonLoginFailed
}

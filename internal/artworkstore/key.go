package artworkstore

import (
	"fmt"
	"path"
	"strings"
)

const (
	// MaxKeyLength bounds a whole logical key. The deterministic artwork/v1
	// grammar is far shorter; the limit exists so an untrusted key from a
	// signed URL can never build a pathological path.
	MaxKeyLength = 1024

	// MaxKeyComponentLength bounds one path component. 255 bytes is the
	// smallest common filesystem limit, so a key accepted here is storable on
	// every supported backend.
	MaxKeyComponentLength = 255
)

// ValidateKey enforces the logical-key grammar every backend shares. It is the
// single gate: an accepted key is safe to join beneath a filesystem root and
// safe to prefix into a bucket.
//
// Accepted: one or more '/'-separated components of ASCII letters, digits,
// '-', '_' and '.', already in canonical form.
//
// Rejected: empty keys, absolute paths, "." and ".." components, empty or
// repeated separators, trailing separators, backslashes, NUL and every other
// byte outside the set above (which also excludes whitespace and non-ASCII, so
// filesystems that normalize Unicode cannot alias two distinct keys), any
// component longer than MaxKeyComponentLength, and any key longer than
// MaxKeyLength.
//
// Components may not begin with '.', which additionally makes the store marker
// file and in-flight temporary files unreachable through any valid key.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: key is empty", ErrInvalidKey)
	}
	if len(key) > MaxKeyLength {
		return fmt.Errorf("%w: key exceeds %d bytes", ErrInvalidKey, MaxKeyLength)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: key is absolute: %s", ErrInvalidKey, key)
	}
	for i := 0; i < len(key); i++ {
		if !allowedKeyByte(key[i]) {
			return fmt.Errorf("%w: key contains a disallowed byte at offset %d", ErrInvalidKey, i)
		}
	}
	for _, component := range strings.Split(key, "/") {
		switch {
		case component == "":
			return fmt.Errorf("%w: key has an empty path component: %s", ErrInvalidKey, key)
		case len(component) > MaxKeyComponentLength:
			return fmt.Errorf("%w: key component exceeds %d bytes: %s", ErrInvalidKey, MaxKeyComponentLength, key)
		case strings.HasPrefix(component, "."):
			// Covers "." and ".." as well as reserved dot-files.
			return fmt.Errorf("%w: key component starts with a dot: %s", ErrInvalidKey, key)
		}
	}
	if path.Clean(key) != key {
		return fmt.Errorf("%w: key is not in canonical form: %s", ErrInvalidKey, key)
	}
	return nil
}

func allowedKeyByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c == '.', c == '/':
		return true
	default:
		return false
	}
}

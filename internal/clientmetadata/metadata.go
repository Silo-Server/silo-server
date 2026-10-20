// Package clientmetadata owns the native client display-label bounds.
package clientmetadata

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Character limits match the native API JSON Schema maxLength values.
const (
	Name      = 128
	Version   = 64
	Build     = 64
	Channel   = 32
	UserAgent = 512
)

// Normalize scrubs invalid UTF-8 and controls, trims whitespace and caps runes.
func Normalize(value string, maxLen int) string {
	// A text column takes neither invalid UTF-8 nor a NUL, and Postgres refuses
	// the whole statement for either. The per-node session upserts share one
	// transaction, so a single malformed client string would stop that entire
	// node from reconciling — not just its own row — until the session goes
	// away. Both scrubs are needed: NUL is perfectly valid UTF-8, so
	// ToValidUTF8 leaves it, and a v3 start body can carry one as the JSON
	// escape (headers cannot — net/http rejects bytes below 0x20).
	// Control characters are stripped wholesale rather than just NUL: none of
	// them belong in an identity label rendered in the admin UI and written to
	// structured logs.
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		value = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, value)
	}
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	// Clamp by runes, not bytes. These bounds are published to clients as JSON
	// Schema maxLength, which counts characters — a byte clamp would silently
	// cut a value the contract calls valid, and cutting mid-rune would produce
	// exactly the invalid UTF-8 scrubbed above.
	runes := 0
	for offset := range value {
		if runes == maxLen {
			return value[:offset]
		}
		runes++
	}
	return value
}

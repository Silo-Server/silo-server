package auditmutation

import (
	"strings"
	"unicode/utf8"
)

// MaxUserAgentBytes bounds diagnostic audit metadata, not the request header
// used by authentication or device identification. Ordinary client headers fit
// within this prefix; longer values are recorded without rejecting the request.
const MaxUserAgentBytes = 512

// BoundUserAgent returns an owned, PostgreSQL-safe diagnostic prefix. Bound the
// raw input before normalization so replacement cannot allocate for a full header.
func BoundUserAgent(raw string) string {
	return boundUserAgent(raw, MaxUserAgentBytes)
}

func boundUserAgent(raw string, limit int) string {
	text := userAgentPrefix(raw, limit)
	// Match logstream.SafeText without importing its dependency on this package.
	text = strings.ToValidUTF8(text, "\uFFFD")
	text = strings.ReplaceAll(text, "\x00", "\uFFFD")
	return strings.Clone(userAgentPrefix(text, limit))
}

// Decode at most limit bytes, retaining invalid bytes for normalization while
// avoiding an artificial replacement character at a split valid rune.
func userAgentPrefix(text string, limit int) string {
	end := 0
	for end < len(text) && end < limit {
		_, size := utf8.DecodeRuneInString(text[end:])
		if end+size > limit {
			break
		}
		end += size
	}
	return text[:end]
}

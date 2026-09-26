package logstream

import (
	"bytes"
	"strings"
)

// Postgres text columns reject NUL and invalid UTF-8, and jsonb rejects the
// \u0000 escape. One such value fails the whole multi-row INSERT, and log
// fields carry client input and file system data: request paths, User-Agent
// headers, file names. The consumers pass every text value through SafeText
// and the encoded attrs through SafeJSON, so a stray byte costs a replacement
// character instead of the batch.

const replacementChar = "\uFFFD"

var nulEscape = []byte(`\u0000`)

// SafeText returns s with each run of invalid UTF-8 and each NUL byte replaced
// by U+FFFD. It returns s unchanged, without allocating, when there is nothing
// to replace.
func SafeText(s string) string {
	s = strings.ToValidUTF8(s, replacementChar)
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", replacementChar)
	}
	return s
}

// SafeJSON returns b, the output of encoding/json, with each \u0000 escape
// replaced by \ufffd. encoding/json already replaces invalid UTF-8. It returns
// b unchanged when there is nothing to replace.
func SafeJSON(b []byte) []byte {
	if !bytes.Contains(b, nulEscape) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 == len(b) {
			out = append(out, b[i])
			continue
		}
		if bytes.HasPrefix(b[i+1:], nulEscape[1:]) {
			out = append(out, `\ufffd`...)
			i += len(nulEscape) - 1
			continue
		}
		// Copy any other escape whole, so an escaped backslash followed by
		// the text "u0000" is left alone.
		out = append(out, b[i], b[i+1])
		i++
	}
	return out
}

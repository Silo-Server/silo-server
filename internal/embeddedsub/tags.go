package embeddedsub

import "bytes"

// HasOverrideTags reports whether any packet's text holds an ASS override
// block, such as {\an8} or {\i1}. SubRip tracks muxed from streaming
// sources often carry them, and some players print them literally. It is
// meant for SubRip and other plain-text tracks; ASS events are built of
// such blocks.
func HasOverrideTags(packets []Packet) bool {
	for _, p := range packets {
		if hasOverrideBlock(p.Data) {
			return true
		}
	}
	return false
}

// hasOverrideBlock reports a "{\" that a "}" closes on the same line.
func hasOverrideBlock(b []byte) bool {
	for {
		i := bytes.Index(b, []byte(`{\`))
		if i < 0 {
			return false
		}
		b = b[i+2:]
		end := bytes.IndexAny(b, "}{\n")
		if end > 0 && b[end] == '}' {
			return true
		}
	}
}

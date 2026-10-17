// Package fspath carries file-system paths whose bytes are not valid UTF-8
// through the parts of Silo that only accept UTF-8 text: PostgreSQL text
// columns, JSON (the API and the transcode-node protocol), protobuf, and the
// search index.
//
// A Linux file name is an arbitrary byte string. A library that was filled by
// an old tool can hold names in a legacy 8-bit encoding, such as a Latin-1
// "å" stored as the single byte 0xe5. PostgreSQL rejects such a string
// (SQLSTATE 22021) and encoding/json silently replaces each bad byte with
// U+FFFD, which loses the name for good.
//
// Silo therefore keeps every path in encoded form: Encode maps each byte of
// an invalid UTF-8 sequence to one code point in a reserved Private Use range
// and leaves valid UTF-8 untouched, so paths that were already UTF-8 (all
// existing rows) are byte-for-byte the same encoded or not. Decode reverses
// it exactly. The encoding works on single bytes and never produces '/', so
// path joins, prefix matches in SQL, and node path mappings behave the same
// on encoded paths as on raw ones.
//
// The rule for callers: names read from the file system (directory listings,
// symlink targets, file-watch events) are encoded once, where they enter
// Silo; every system call or child process that receives a path decodes it.
// The wrappers in this package (Open, Stat, ReadDir, Command, ...) do the
// decoding. Decode leaves a string without reserved code points unchanged, so
// it is safe on paths that never needed encoding, such as configured roots.
//
// Display turns an encoded name into text for people and for title parsing:
// each escaped byte is read as Windows-1252, the most common legacy encoding
// for such names, so "B\xe5rd" displays as "Bård".
package fspath

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// escapeBase is the code point an escaped byte is added to. Raw bytes
// 0x80-0xff map to U+10FE80-U+10FEFF in Supplementary Private Use Area-B,
// which real file names do not use, and which stays clear of the
// noncharacters U+10FFFE and U+10FFFF.
const escapeBase = 0x10FE00

const (
	escapeFirst = escapeBase + 0x80
	escapeLast  = escapeBase + 0xff
)

// escapeLeadByte is the first byte of every escape code point's UTF-8 form
// and of every code point above U+FFFFF. A string without it holds no escapes.
const escapeLeadByte = 0xf4

func isEscape(r rune) bool { return r >= escapeFirst && r <= escapeLast }

// Encode returns raw as valid UTF-8. Valid UTF-8 without reserved code points
// is returned unchanged. Each byte of an invalid sequence becomes one reserved
// code point. A reserved code point that is really in the name is escaped
// byte by byte as well, so Decode(Encode(raw)) == raw for every input.
//
// Encode only names that come straight from the file system. Encoding a path
// that is already encoded escapes it twice.
func Encode(raw string) string {
	if utf8.ValidString(raw) && strings.IndexByte(raw, escapeLeadByte) < 0 {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw) + len(raw)/2)
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(escapeBase + rune(raw[i]))
		case isEscape(r):
			for j := range size {
				b.WriteRune(escapeBase + rune(raw[i+j]))
			}
		default:
			b.WriteString(raw[i : i+size])
		}
		i += size
	}
	return b.String()
}

// Decode returns the raw path for an encoded one. Strings without reserved
// code points, including raw paths, are returned unchanged.
func Decode(path string) string {
	if strings.IndexByte(path, escapeLeadByte) < 0 {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); {
		r, size := utf8.DecodeRuneInString(path[i:])
		if isEscape(r) {
			b.WriteByte(byte(r - escapeBase)) //nolint:gosec // isEscape bounds r - escapeBase to 0x80-0xff
		} else {
			b.WriteString(path[i : i+size])
		}
		i += size
	}
	return b.String()
}

// Display returns text for showing or parsing an encoded name: each escaped
// byte is read as Windows-1252, and a byte that has no printable character
// there becomes U+FFFD. Other text is returned unchanged.
func Display(path string) string {
	if strings.IndexByte(path, escapeLeadByte) < 0 {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		if !isEscape(r) {
			b.WriteRune(r)
			continue
		}
		decoded := charmap.Windows1252.DecodeByte(byte(r - escapeBase)) //nolint:gosec // isEscape bounds r - escapeBase to 0x80-0xff
		if unicode.IsControl(decoded) {
			decoded = utf8.RuneError
		}
		b.WriteRune(decoded)
	}
	return b.String()
}

// IsEncoded reports whether path holds escaped bytes, that is, whether the
// file-system name it stands for is not valid UTF-8.
func IsEncoded(path string) bool {
	if strings.IndexByte(path, escapeLeadByte) < 0 {
		return false
	}
	for _, r := range path {
		if isEscape(r) {
			return true
		}
	}
	return false
}

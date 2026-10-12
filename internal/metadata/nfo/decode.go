package nfo

import (
	"bytes"
	"encoding/xml"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}

	xmlEncodingDeclRe = regexp.MustCompile(`^\s*<\?xml[^>]*?\bencoding\s*=\s*["']([^"']+)["']`)
)

// newNFODecoder returns the one XML decoder every NFO parse uses. NFO files
// come from many writers (Kodi, Jellyfin, Ember, tinyMediaManager, hand
// edits), so it reads them the way those tools tolerate them: a declared
// legacy charset is decoded, HTML entities such as &eacute; resolve, a bare
// '&' or a missing end tag does not reject the whole file, and an HTML <br>
// in a plot closes itself instead of swallowing the text after it.
func newNFODecoder(data []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = nfoCharsetReader
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	return decoder
}

// nfoCharsetReader decodes the legacy charset a document declares.
// normalizeNFOText leaves a declaration in place only for such a charset; it
// rewrites the declaration to UTF-8 once it has produced UTF-8 itself.
func nfoCharsetReader(label string, input io.Reader) (io.Reader, error) {
	if reader, err := charset.NewReaderLabel(label, input); err == nil {
		return reader, nil
	}
	return input, nil
}

// normalizeNFOText returns text the decoder can read, as UTF-8 wherever the
// encoding can be settled here:
//
//   - a UTF-16 byte-order mark: converted to UTF-8;
//   - valid UTF-8 after a UTF-8 BOM, or valid UTF-8 with non-ASCII bytes: read
//     as UTF-8 whatever the declaration says (accented text in a legacy
//     charset is almost never valid UTF-8, so the bytes outrank a stale
//     declaration);
//   - a declared legacy charset (ISO-8859-1, Windows-1252, Shift_JIS, ...):
//     left for the decoder's CharsetReader;
//   - anything else that is not valid UTF-8: read as Windows-1252, the
//     superset of ISO-8859-1 that Windows tools write without declaring it.
//
// When the result is UTF-8, a declared encoding is rewritten to UTF-8 so the
// decoder does not convert the text twice, and control characters XML
// forbids (a stray NUL, ^Z, vertical tab) are dropped instead of rejecting
// the file.
func normalizeNFOText(data []byte) []byte {
	utf8BOMSeen := false
	switch {
	case bytes.HasPrefix(data, utf8BOM):
		data, utf8BOMSeen = data[len(utf8BOM):], true
	case bytes.HasPrefix(data, utf16LEBOM):
		return utf8Document(transcode(data, unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM)))
	case bytes.HasPrefix(data, utf16BEBOM):
		return utf8Document(transcode(data, unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM)))
	}
	// Forbidden controls go first, so a stray byte before the declaration
	// cannot hide it. ESC stays: ISO-2022 charsets switch character sets with
	// it, and bytes below 0x20 are never part of a multi-byte character in the
	// other ASCII-compatible charsets the reader decodes.
	data = withoutForbiddenControls(data, true)
	legacy := declaresLegacyCharset(data)
	switch {
	case utf8.Valid(data) && (utf8BOMSeen || !legacy || hasNonASCII(data)):
		return utf8Document(data)
	case legacy:
		return data
	}
	return utf8Document(transcode(data, charmap.Windows1252))
}

// transcode converts data to UTF-8, keeping the original bytes if the
// decoder rejects them; the XML decoder then reports the file as unreadable.
func transcode(data []byte, enc encoding.Encoding) []byte {
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return data
	}
	return decoded
}

// declaresLegacyCharset reports whether the XML declaration names a charset
// other than UTF-8 or UTF-16 (aliases such as latin1, cp1252, or UCS-2 are
// resolved to their canonical encoding).
func declaresLegacyCharset(data []byte) bool {
	loc := xmlEncodingDecl(data)
	if loc == nil {
		return false
	}
	enc, name := charset.Lookup(string(data[loc[2]:loc[3]]))
	return enc != nil && name != "utf-8" && name != "utf-16le" && name != "utf-16be"
}

// utf8Document marks UTF-8 text as such for the decoder: control characters
// XML 1.0 forbids are removed, then a declared encoding becomes UTF-8.
func utf8Document(data []byte) []byte {
	data = withoutForbiddenControls(data, false)
	if loc := xmlEncodingDecl(data); loc != nil {
		data = slices.Concat(data[:loc[2]], []byte("UTF-8"), data[loc[3]:])
	}
	return data
}

// withoutForbiddenControls drops the C0 control characters XML 1.0 forbids
// (all below 0x20 but tab, newline, and carriage return), keeping ESC when
// asked. They are single bytes, so the filter is safe on UTF-8 and on
// ASCII-compatible legacy charsets alike.
func withoutForbiddenControls(data []byte, keepESC bool) []byte {
	forbidden := func(b byte) bool {
		return b < 0x20 && b != '\t' && b != '\n' && b != '\r' && (b != 0x1B || !keepESC)
	}
	if !slices.ContainsFunc(data, forbidden) {
		return data
	}
	out := make([]byte, 0, len(data))
	for _, b := range data {
		if !forbidden(b) {
			out = append(out, b)
		}
	}
	return out
}

func hasNonASCII(data []byte) bool {
	return slices.ContainsFunc(data, func(b byte) bool { return b >= utf8.RuneSelf })
}

// xmlEncodingDecl returns the submatch indexes of the encoding value in the
// document's XML declaration, or nil.
func xmlEncodingDecl(data []byte) []int {
	head := data[:min(len(data), 256)]
	return xmlEncodingDeclRe.FindSubmatchIndex(head)
}

// decodeNFORoot decodes the document's root element into v and reads what
// follows it: the character data where a hybrid NFO keeps its provider link,
// and whether another <episodedetails> root follows (a multi-episode file).
func decodeNFORoot(data []byte, v any) (trailing string, moreEpisodes bool, err error) {
	decoder := newNFODecoder(data)
	if err := decoder.Decode(v); err != nil {
		return "", false, err
	}
	var rest strings.Builder
	for tok, ok := nextToken(decoder); ok; tok, ok = nextToken(decoder) {
		switch t := tok.(type) {
		case xml.CharData:
			rest.Write(t)
		case xml.StartElement:
			return rest.String(), t.Name.Local == "episodedetails", nil
		}
	}
	return rest.String(), false, nil
}

// nextToken reads past the root element. Reading stops at the end of input
// or at anything the lenient decoder cannot tokenize; the root itself has
// already decoded, so neither is an error.
func nextToken(decoder *xml.Decoder) (xml.Token, bool) {
	tok, err := decoder.Token()
	return tok, err == nil
}

// parseNFOInt reads a numeric element leniently: "2019", " 3 ", "2.0", and a
// leading number followed by text ("2019-05-01", "3 of 10") all yield their
// number. Text with no leading number, or a number that is not whole ("2.5",
// "2e3", "0x10"), reports false, so one bad value drops only its own field
// instead of the whole file.
func parseNFOInt(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	if whole, fraction, ok := strings.Cut(s, "."); ok && strings.Trim(fraction, "0") == "" {
		s = whole // "2.0"
	}
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	digitsStart := end
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == digitsStart || (end < len(s) && strings.ContainsRune(".eExX", rune(s[end]))) {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

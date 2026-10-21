package subtitles

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/language"

	"github.com/Silo-Server/silo-server/internal/lang"
)

// DecodeText returns the text of a subtitle as UTF-8, the encoding every
// subtitle response declares. Files authored before UTF-8 was common are
// often in a legacy code page that only the subtitle's language identifies.
//
// UTF-8 input, and anything that is not a text format, is returned as it is,
// including a UTF-8 BOM; so is UTF-8 with a few damaged bytes, which reads
// better as it is than decoded whole. A UTF-16 file needs its BOM. Bytes containing NUL
// are binary (a VobSub .sub) and stay untouched. Other input is decoded from
// the code page its language uses, chosen between that language's common
// alternatives by the bytes themselves. Without a language, or for a language
// written in Latin script, Windows-1252 applies only when the bytes look like
// Latin text; any other input is returned unchanged, since a client that
// detects encodings itself can still read it.
//
// Offline clients keep stored subtitles by DeliveryRevision: a change here
// that alters delivered bytes needs a new revision suffix there.
func DecodeText(format SubtitleFormat, languageTag string, data []byte) []byte {
	if !isTextSubtitleFormat(format) || mostlyUTF8(data) {
		return data
	}
	var enc encoding.Encoding
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		enc = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		enc = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM)
	case bytes.IndexByte(data, 0) >= 0:
		return data
	default:
		enc = legacyEncoding(languageTag, data)
	}
	if enc == nil {
		return data
	}
	out, err := enc.NewDecoder().Bytes(data)
	// A byte the code page leaves undefined means the guess was wrong.
	if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
		return data
	}
	return out
}

func isTextSubtitleFormat(format SubtitleFormat) bool {
	return SupportsRetime(format) || normalizeFormat(format) == FormatSUB
}

// mostlyUTF8 reports whether data is UTF-8, allowing a few invalid bytes
// among more valid multibyte characters. Text in a legacy code page almost
// never forms a valid multibyte sequence.
func mostlyUTF8(data []byte) bool {
	if utf8.Valid(data) {
		return true
	}
	var multibyte, invalid int
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		switch {
		case r == utf8.RuneError && size == 1:
			invalid++
		case size > 1:
			multibyte++
		}
		data = data[size:]
	}
	return multibyte > invalid
}

// legacyEncoding picks the code page a subtitle in languageTag was most
// likely saved in, or nil when there is no safe choice.
func legacyEncoding(languageTag string, data []byte) encoding.Encoding {
	tag, err := language.Parse(lang.CompatibleTag(languageTag))
	if err != nil {
		return latinEncoding(data)
	}
	base, _ := tag.Base()
	script, scriptConf := tag.Script()
	switch base.String() {
	case "ru", "be", "bg", "mk":
		return cyrillicEncoding(charmap.KOI8R, data)
	case "uk":
		return cyrillicEncoding(charmap.KOI8U, data)
	case "sr":
		// Serbian subtitles are written in either script; KOI8 has no
		// Serbian letters.
		if (script.String() == "Latn" && scriptConf == language.Exact) || looksLatin(data) {
			return centralEuropeanEncoding(data)
		}
		return charmap.Windows1251
	case "pl", "cs", "sk", "sl", "hu", "ro", "hr", "bs":
		return centralEuropeanEncoding(data)
	case "el":
		return charmap.Windows1253
	case "tr", "az":
		return charmap.Windows1254
	case "he", "yi":
		return charmap.Windows1255
	case "ar", "fa", "ur":
		return charmap.Windows1256
	case "et", "lv", "lt":
		return charmap.Windows1257
	case "vi":
		return charmap.Windows1258
	case "th":
		return charmap.Windows874
	case "ja":
		return japanese.ShiftJIS
	case "ko":
		return korean.EUCKR
	case "zh":
		// Big5 text decodes as GB18030 without error, so the tag has to
		// name the script or the region.
		if _, regionConf := tag.Region(); scriptConf != language.Exact && regionConf != language.Exact {
			return nil
		}
		if script.String() == "Hant" {
			return traditionalchinese.Big5
		}
		return simplifiedchinese.GB18030
	}
	return latinEncoding(data)
}

func latinEncoding(data []byte) encoding.Encoding {
	if looksLatin(data) {
		return charmap.Windows1252
	}
	return nil
}

// looksLatin reports whether data reads as Latin-script text: accented
// letters are a small share of the letters. Cyrillic, Greek, Hebrew, and
// Arabic text in a single-byte code page is nearly all high bytes.
func looksLatin(data []byte) bool {
	var ascii, high int
	for _, b := range data {
		switch {
		case b >= 0x80:
			high++
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
			ascii++
		}
	}
	return high*4 < ascii
}

// cyrillicEncoding tells Windows-1251 from KOI8 by the commonest letters,
// о, а, and и, which the two code pages place at different bytes in either
// case.
func cyrillicEncoding(koi8 encoding.Encoding, data []byte) encoding.Encoding {
	var windows, koi int
	for _, b := range data {
		switch b {
		case 0xEE, 0xCE, 0xE0, 0xC0, 0xE8, 0xC8:
			windows++
		case 0xCF, 0xEF, 0xC1, 0xE1, 0xC9, 0xE9:
			koi++
		}
	}
	if koi > windows {
		return koi8
	}
	return charmap.Windows1251
}

// centralEuropeanEncoding tells Windows-1250 from ISO-8859-2. ISO-8859-2 has
// only control codes in 0x80-0x9F, where Windows-1250 keeps common letters
// such as ś, š, and ž; the letters ISO-8859-2 moves into 0xA1-0xBC are
// symbols in Windows-1250.
func centralEuropeanEncoding(data []byte) encoding.Encoding {
	isoLetter := false
	for _, b := range data {
		switch b {
		case 0xA1, 0xA6, 0xAC, 0xB1, 0xB6, 0xBC:
			isoLetter = true
		}
		if b >= 0x80 && b <= 0x9F {
			return charmap.Windows1250
		}
	}
	if isoLetter {
		return charmap.ISO8859_2
	}
	return charmap.Windows1250
}

package settingscontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf16"
)

// This file holds the two checks encoding/json will not make for us. Both cover
// input that Go accepts by silently changing it, which is the dangerous shape
// for a contract whose whole promise is that every peer agrees on the bytes.

// rejectQuotedNumber reports an error if a value declared numeric arrived as a
// JSON string.
//
// json.Number is a string kind, so encoding/json unmarshals `"1.5"` into it
// without complaint and Float64/Int64 then parse the quoted digits happily. The
// value would validate, and NormalizeValue would store the quoted form into
// jsonb — where every consumer reading it as a number, and every client
// comparing canonical bytes, disagrees with the row.
func rejectQuotedNumber(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		return errors.New("a numeric setting must not be sent as a JSON string")
	}
	return nil
}

// maxJSONDepth bounds the recursion in the duplicate-key scan. Setting values
// arrive from the network, and the deepest schema the contract declares nests
// three levels, so this is far above anything legitimate and still cannot be
// driven into a stack overflow.
const maxJSONDepth = 64

var errJSONTooDeep = fmt.Errorf("JSON nests deeper than %d levels", maxJSONDepth)

// rejectDuplicateKeys reports an error if any object in the document repeats a
// property name.
//
// encoding/json and the schema validator both keep the last occurrence, so
// {"fontSize":"small","fontSize":"large"} validates and stores "large" with no
// complaint. Which one wins is a property of the parser rather than of the
// contract: a client generated against a different JSON library can disagree
// about what it just sent, and the manifest's canonical form has no way to
// represent the duplicate at all. RFC 8785 leaves this to the caller, so the
// caller rejects it.
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		// Malformed JSON is the caller's problem to report, with its own
		// message. Nothing here is a duplicate key.
		return nil //nolint:nilerr // parse errors surface from the real decode
	}
	return scanDuplicateKeys(decoder, token, 0)
}

func scanDuplicateKeys(decoder *json.Decoder, token json.Token, depth int) error {
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxJSONDepth {
		return errJSONTooDeep
	}

	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return nil //nolint:nilerr // as above
			}
			name, ok := nameToken.(string)
			if !ok {
				return nil
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("object repeats the property %q", name)
			}
			seen[name] = struct{}{}

			valueToken, err := decoder.Token()
			if err != nil {
				return nil //nolint:nilerr // as above
			}
			if err := scanDuplicateKeys(decoder, valueToken, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			itemToken, err := decoder.Token()
			if err != nil {
				return nil //nolint:nilerr // as above
			}
			if err := scanDuplicateKeys(decoder, itemToken, depth+1); err != nil {
				return err
			}
		}
	}

	// Consume the closing delimiter so the caller resumes in the right place.
	if _, err := decoder.Token(); err != nil {
		return nil //nolint:nilerr // as above
	}
	return nil
}

// rejectLoneSurrogates reports an error if any string literal contains a \u
// escape for an unpaired UTF-16 surrogate.
//
// encoding/json replaces one with U+FFFD and reports success, so the server
// would generate canonical bytes and an ETag for an artifact a conforming
// client must refuse: RFC 8785 requires canonicalization to terminate here.
// Silently substituting a replacement character also means the value read back
// is not the value written, which no setting should ever do.
func rejectLoneSurrogates(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		switch {
		case !inString:
			if raw[i] == '"' {
				inString = true
			}
		case raw[i] == '"':
			inString = false
		case raw[i] == '\\':
			if i+1 >= len(raw) {
				return errors.New("string ends in an incomplete escape")
			}
			if raw[i+1] != 'u' {
				// Any other two-character escape; skip both bytes.
				i++
				continue
			}
			code, err := parseHex4(raw, i+2)
			if err != nil {
				return err
			}
			i += 5
			if !utf16.IsSurrogate(rune(code)) {
				continue
			}
			if code >= 0xDC00 {
				return fmt.Errorf(
					"\\u%04X is a trailing surrogate with no leading surrogate before it", code)
			}
			// A leading surrogate must be followed immediately by a trailing one.
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return fmt.Errorf(
					"\\u%04X is a leading surrogate with no trailing surrogate after it", code)
			}
			low, err := parseHex4(raw, i+3)
			if err != nil {
				return err
			}
			if low < 0xDC00 || low > 0xDFFF {
				return fmt.Errorf(
					"\\u%04X is followed by \\u%04X, which is not a trailing surrogate", code, low)
			}
			i += 6
		}
	}
	return nil
}

func parseHex4(raw []byte, at int) (uint64, error) {
	if at+4 > len(raw) {
		return 0, errors.New("incomplete \\u escape")
	}
	code, err := strconv.ParseUint(string(raw[at:at+4]), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("malformed \\u escape %q", raw[at:at+4])
	}
	return code, nil
}

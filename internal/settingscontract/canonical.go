package settingscontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// canonicalOnce guards the four derived representations below. All of them are
// pure functions of files embedded at compile time, so they are computed once
// and handed out as copies rather than rebuilt per request: a conditional GET
// of the manifest would otherwise pay a full parse and re-serialize just to
// answer 304.
var (
	canonicalOnce      sync.Once
	canonicalManifest  []byte
	canonicalPublic    []byte
	canonicalETag      string
	canonicalPublicTag string
	canonicalErr       error
)

func computeCanonical() {
	raw, err := RawBytes()
	if err != nil {
		canonicalErr = err
		return
	}

	canonicalManifest, canonicalErr = canonicalize(raw)
	if canonicalErr != nil {
		return
	}

	canonicalPublic, canonicalErr = buildPublic(raw)
	if canonicalErr != nil {
		return
	}

	canonicalETag, canonicalErr = digestWithSchemas(canonicalManifest)
	if canonicalErr != nil {
		return
	}
	canonicalPublicTag, canonicalErr = digestWithSchemas(canonicalPublic)
}

// CanonicalBytes returns the RFC 8785 (JCS) canonicalization of the manifest:
// UTF-8, object keys sorted by code point, no insignificant whitespace.
//
// Two servers built from the same manifest produce identical bytes, which is
// what makes the ETag comparable across deployments and generated client code
// reproducible.
func CanonicalBytes() ([]byte, error) {
	canonicalOnce.Do(computeCanonical)
	if canonicalErr != nil {
		return nil, canonicalErr
	}
	return append([]byte(nil), canonicalManifest...), nil
}

// ETag returns the entity tag for the contract, formatted as a strong HTTP
// entity tag.
//
// The digest covers the value schemas under schemas/ as well as the manifest.
// Those files decide which object values the server accepts, so a change to one
// changes the contract even though manifest.json is byte-identical. Folding
// them in means the tag is a validator for the contract rather than for the
// served bytes alone: a client whose conditional GET misses re-reads a body
// that may not have changed, which is the cheap direction to be wrong in. The
// alternative — 304 forever while the server silently validates against a
// different schema — is the drift this contract exists to prevent.
func ETag() (string, error) {
	canonicalOnce.Do(computeCanonical)
	if canonicalErr != nil {
		return "", canonicalErr
	}
	return canonicalETag, nil
}

// PublicBytes returns the canonicalized manifest with maintainer-only fields
// removed. This is what GET /api/v1/settings/manifest serves.
//
// Only "notes" is stripped today. Internal storage bindings, when they exist,
// are stripped here too: the public manifest must never name a table or column.
func PublicBytes() ([]byte, error) {
	canonicalOnce.Do(computeCanonical)
	if canonicalErr != nil {
		return nil, canonicalErr
	}
	return append([]byte(nil), canonicalPublic...), nil
}

// PublicETag returns the entity tag for the public manifest projection. It
// differs from ETag because stripping notes changes the bytes clients see.
func PublicETag() (string, error) {
	canonicalOnce.Do(computeCanonical)
	if canonicalErr != nil {
		return "", canonicalErr
	}
	return canonicalPublicTag, nil
}

// buildPublic strips maintainer-only fields and canonicalizes in one pass. The
// decoded document is handed straight to the canonical writer, which already
// understands map[string]any / []any / json.Number, rather than being
// re-marshaled and re-parsed.
func buildPublic(raw []byte) ([]byte, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}

	root, _ := doc.(map[string]any)
	definitions, _ := root["definitions"].([]any)
	for _, entry := range definitions {
		if def, ok := entry.(map[string]any); ok {
			delete(def, "notes")
		}
	}

	return canonicalizeValue(doc)
}

// digestWithSchemas hashes a canonical manifest projection together with every
// value schema, keyed by filename so a rename is a change too.
func digestWithSchemas(manifest []byte) (string, error) {
	schemas, err := SchemaBytes()
	if err != nil {
		return "", err
	}

	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	digest := sha256.New()
	digest.Write(manifest)
	for _, name := range names {
		canonical, err := canonicalize(schemas[name])
		if err != nil {
			return "", fmt.Errorf("canonicalizing value schema %s: %w", name, err)
		}
		// Length-prefixed so no combination of names and bodies can collide
		// with a different combination.
		// digest is a hash.Hash; its Write never returns an error.
		_, _ = fmt.Fprintf(digest, "\n%d:%s\n%d:", len(name), name, len(canonical))
		digest.Write(canonical)
	}

	sum := digest.Sum(nil)
	return `"` + hex.EncodeToString(sum) + `"`, nil
}

func decodeJSON(raw []byte) (any, error) {
	// Both checks run before the decode that would paper over what they catch.
	// This is the shared entry point for the manifest, its public projection and
	// every value schema, so all three get the same guarantees.
	if err := rejectLoneSurrogates(raw); err != nil {
		return nil, fmt.Errorf("canonicalization refused a lone surrogate: %w", err)
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return nil, fmt.Errorf("canonicalization refused a duplicate key: %w", err)
	}

	var doc any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing JSON for canonicalization: %w", err)
	}
	return doc, nil
}

func canonicalize(raw []byte) ([]byte, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	return canonicalizeValue(doc)
}

func canonicalizeValue(doc any) ([]byte, error) {
	var out bytes.Buffer
	if err := writeCanonical(&out, doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")

	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}

	case json.Number:
		serialized, err := canonicalNumber(typed)
		if err != nil {
			return err
		}
		out.WriteString(serialized)

	case string:
		writeCanonicalString(out, typed)

	case []any:
		out.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')

	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		// JCS sorts by UTF-16 code unit. For the ASCII key space this contract
		// uses, byte order and code-unit order agree.
		sort.Strings(keys)

		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalString(out, key)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')

	default:
		return fmt.Errorf("unexpected JSON value of type %T", value)
	}

	return nil
}

// writeCanonicalString escapes a string the way ECMAScript's JSON.stringify
// does, which is what JCS requires.
//
// encoding/json is deliberately not used here: json.Marshal HTML-escapes <, >
// and &, which JCS emits literally. Nothing in the manifest triggers that
// today, so the difference would first appear as an ETag that silently
// disagrees with every conforming client the moment a label contains an
// ampersand.
func writeCanonicalString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
}

// canonicalNumber renders a number the way ECMAScript's Number::toString does,
// which is what JCS requires: 3.0 becomes "3", 0.05 stays "0.05".
func canonicalNumber(number json.Number) (string, error) {
	parsed, err := number.Float64()
	if err != nil {
		return "", fmt.Errorf("parsing number %s: %w", number, err)
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return "", fmt.Errorf("NaN and infinities cannot be canonicalized: %s", number)
	}
	return formatECMAScript(parsed)
}

// formatECMAScript implements ECMA-262 Number::toString for radix 10.
//
// strconv is used only for the shortest round-tripping digits and the decimal
// exponent; the layout is reassembled here because Go's own formats differ from
// ECMAScript in three ways that all silently change the digest: 'g' switches to
// exponential at 1e-5 where ECMAScript switches at 1e-7, Go zero-pads the
// exponent ("1e-07" vs "1e-7"), and Go prints negative zero as "-0" where
// ECMAScript and JCS print "0".
func formatECMAScript(value float64) (string, error) {
	if value == 0 {
		return "0", nil
	}

	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}

	// "d.dddde±dd" with the fewest digits that round-trip.
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	separator := strings.IndexByte(scientific, 'e')
	if separator < 0 {
		return "", fmt.Errorf("unexpected float formatting for %v", value)
	}
	exponent, err := strconv.Atoi(scientific[separator+1:])
	if err != nil {
		return "", fmt.Errorf("parsing exponent of %v: %w", value, err)
	}
	digits := strings.Replace(scientific[:separator], ".", "", 1)

	// ECMA-262 names these k (digit count) and n (position of the decimal
	// point relative to the digits), where the value is digits × 10^(n-k).
	k := len(digits)
	n := exponent + 1

	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k), nil
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits, nil
	}

	suffix := "e+" + strconv.Itoa(n-1)
	if n-1 < 0 {
		suffix = "e-" + strconv.Itoa(1-n)
	}
	if k == 1 {
		return sign + digits + suffix, nil
	}
	return sign + digits[:1] + "." + digits[1:] + suffix, nil
}

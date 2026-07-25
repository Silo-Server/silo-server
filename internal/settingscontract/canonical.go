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
)

// CanonicalBytes returns the RFC 8785 (JCS) canonicalization of the manifest:
// UTF-8, object keys sorted by code point, no insignificant whitespace.
//
// Two servers built from the same manifest produce identical bytes, which is
// what makes the ETag comparable across deployments and generated client code
// reproducible.
func CanonicalBytes() ([]byte, error) {
	raw, err := RawBytes()
	if err != nil {
		return nil, err
	}
	return canonicalize(raw)
}

// ETag returns the SHA-256 digest of the canonical bytes, formatted as a strong
// HTTP entity tag.
func ETag() (string, error) {
	canonical, err := CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return `"` + hex.EncodeToString(sum[:]) + `"`, nil
}

// PublicBytes returns the canonicalized manifest with maintainer-only fields
// removed. This is what GET /api/v1/settings/manifest serves.
//
// Only "notes" is stripped today. Internal storage bindings, when they exist,
// are stripped here too: the public manifest must never name a table or column.
func PublicBytes() ([]byte, error) {
	raw, err := RawBytes()
	if err != nil {
		return nil, err
	}

	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	definitions, _ := doc["definitions"].([]any)
	for _, entry := range definitions {
		def, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		delete(def, "notes")
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding public manifest: %w", err)
	}
	return canonicalize(encoded)
}

// PublicETag returns the entity tag for the public manifest projection. It
// differs from ETag because stripping notes changes the bytes clients see.
func PublicETag() (string, error) {
	public, err := PublicBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(public)
	return `"` + hex.EncodeToString(sum[:]) + `"`, nil
}

func canonicalize(raw []byte) ([]byte, error) {
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing JSON for canonicalization: %w", err)
	}

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
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Errorf("encoding string: %w", err)
		}
		out.Write(encoded)

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
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return fmt.Errorf("encoding key %q: %w", key, err)
			}
			out.Write(encodedKey)
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
	if parsed == math.Trunc(parsed) && math.Abs(parsed) < 1e21 {
		return strconv.FormatFloat(parsed, 'f', 0, 64), nil
	}
	return strconv.FormatFloat(parsed, 'g', -1, 64), nil
}

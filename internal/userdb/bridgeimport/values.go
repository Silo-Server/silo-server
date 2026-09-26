package bridgeimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const maxImportedValueBytes = 16 << 20

// importValue rejects lossy or ambiguous conversion. Errors never contain the
// source value; callers may annotate only a fixed table/column identifier.
func importValue(column SourceColumn, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch column.Kind {
	case integerColumn:
		n, ok := value.(int64)
		if !ok {
			return nil, errors.New("invalid integer")
		}
		return n, nil
	case booleanColumn:
		// The source expression preserves storage values while preventing the
		// driver from decoding declared BOOLEAN columns before validation.
		n, ok := value.(int64)
		if !ok || (n != 0 && n != 1) {
			return nil, errors.New("invalid boolean")
		}
		return n == 1, nil
	case realColumn:
		var n float64
		switch v := value.(type) {
		case float64:
			n = v
		case int64:
			n = float64(v)
		default:
			return nil, errors.New("invalid number")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("nonfinite number")
		}
		return n, nil
	default:
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > maxImportedValueBytes {
			return nil, errors.New("unrepresentable text")
		}
		switch column.Kind {
		case instantColumn, instantTextColumn:
			instant, err := time.Parse(time.RFC3339Nano, text)
			if err != nil || instant.Year() < 1 || instant.Year() > 9999 || (column.Kind == instantColumn && instant.Nanosecond()%1000 != 0) {
				return nil, errors.New("unrepresentable timestamp")
			}
			if column.Kind == instantTextColumn {
				return text, nil
			}
			return instant.UTC(), nil
		case jsonColumn:
			if err := validateImportJSON(text); err != nil {
				return nil, err
			}
		}
		return text, nil
	}
}

// jsonb normalizes object keys and numeric formatting. Duplicate object members
// are refused before PostgreSQL could silently discard one of their values.
func validateImportJSON(value string) error {
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return errors.New("unrepresentable JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unrepresentable JSON")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 128 {
		return errors.New("JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case string:
		if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			return errors.New("invalid JSON text")
		}
	case json.Delim:
		switch value {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] || strings.ContainsRune(name, 0) {
					return errors.New("invalid JSON member")
				}
				keys[name] = true
				if err := validateJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := validateJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	return nil
}

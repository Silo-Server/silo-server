package diagnostics

import (
	"errors"
	"testing"
)

func TestParseShortIDRejectsAmbiguousCharacters(t *testing.T) {
	for _, raw := range []string{"ABCDEF12345I", "ABCDEF12345L", "ABCDEF12345O", "ABCDEF12345U"} {
		if _, err := ParseShortID(raw); !errors.Is(err, ErrInvalidShortID) {
			t.Fatalf("ParseShortID(%q) error = %v, want ErrInvalidShortID", raw, err)
		}
	}
}

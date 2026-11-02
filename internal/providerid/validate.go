// Package providerid contains validation shared by provider-ID ingestion
// boundaries.
package providerid

import "strings"

// IsPositiveDecimal reports whether value is a non-zero base-10 integer
// written using ASCII digits only. Leading zeroes are accepted because
// providers may treat the original text as their canonical identifier.
func IsPositiveDecimal(value string) bool {
	if value == "" {
		return false
	}

	nonZero := false
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
		if r != '0' {
			nonZero = true
		}
	}
	return nonZero
}

// IsIMDbID reports whether value is an IMDb title id: "tt" (in any case)
// followed by 7 to 10 digits that are not all zero, as in tt0133093.
// Surrounding whitespace is ignored.
func IsIMDbID(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	digits, ok := strings.CutPrefix(value, "tt")
	return ok && len(digits) >= 7 && len(digits) <= 10 && IsPositiveDecimal(digits)
}

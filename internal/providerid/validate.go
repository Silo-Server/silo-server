// Package providerid contains validation shared by provider-ID ingestion
// boundaries.
package providerid

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

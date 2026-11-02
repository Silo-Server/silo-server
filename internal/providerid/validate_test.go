package providerid

import "testing"

func TestIsPositiveDecimal(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"":       false,
		"0":      false,
		"000":    false,
		"1":      true,
		"001":    true,
		"123456": true,
		"-1":     false,
		"1.0":    false,
		"１２３":    false,
		"12a":    false,
	}
	for value, want := range tests {
		if got := IsPositiveDecimal(value); got != want {
			t.Errorf("IsPositiveDecimal(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestIsIMDbID(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"tt0133093":     true,
		" TT0133093 ":   true,
		"tt12345678":    true,
		"tt1234567890":  true,
		"tt0000000":     false,
		"tt123456":      false,
		"tt12345678901": false,
		"tt01a3093":     false,
		"0133093":       false,
		"":              false,
	}
	for value, want := range tests {
		if got := IsIMDbID(value); got != want {
			t.Errorf("IsIMDbID(%q) = %v, want %v", value, got, want)
		}
	}
}

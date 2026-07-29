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

package access

import "testing"

func TestCertificationParentalLimits(t *testing.T) {
	for _, tc := range []struct {
		rating, ceiling string
		allowed         bool
	}{
		{"AU-M", "AU-M", true}, {"AU-MA15+", "AU-M", false},
		{"AU-MA15+", "AU-MA15+", true}, {"AU-R18+", "AU-MA15+", false},
		{"R", "AU-MA15+", false}, {"TV-MA", "AU-MA15+", false},
		{"PG-13", "AU-M", true}, {"AU-M", "PG-13", true},
		{"AU-X18+", "AU-R18+", false}, {"AU-CTC", "AU-R18+", false},
		{"", "AU-G", false}, {"NR", "", true},
		{"NC-17", "R", true}, {"TV-MA", "R", true},
	} {
		if got := RatingAllowed(tc.rating, tc.ceiling); got != tc.allowed {
			t.Errorf("%s under %s = %v", tc.rating, tc.ceiling, got)
		}
	}
}

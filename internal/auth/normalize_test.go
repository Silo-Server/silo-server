package auth

import "testing"

func TestNormalizeUsername(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trims surrounding spaces", "  john  ", "john"},
		{"trims tabs and newlines", "\t john\n", "john"},
		{"preserves internal spacing", "john doe", "john doe"},
		{"preserves case", "JohnDoe", "JohnDoe"},
		{"whitespace only becomes empty", "   ", ""},
		{"already clean is unchanged", "john", "john"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeUsername(tc.in); got != tc.want {
				t.Errorf("NormalizeUsername(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trims surrounding spaces", "  user@example.com  ", "user@example.com"},
		{"trims tabs and newlines", "\tuser@example.com\n", "user@example.com"},
		{"preserves case (not lowercased)", "User@Example.COM", "User@Example.COM"},
		{"whitespace only becomes empty", "  ", ""},
		{"already clean is unchanged", "user@example.com", "user@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeEmail(tc.in); got != tc.want {
				t.Errorf("NormalizeEmail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

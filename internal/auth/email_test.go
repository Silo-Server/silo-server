package auth

import "testing"

func TestValidateEmail(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"admin@example.com", "admin@example.com", true},
		{"  admin@example.com ", "admin@example.com", true},
		{"first.last+tag@sub.example.co.uk", "first.last+tag@sub.example.co.uk", true},
		{"admin@[192.0.2.1]", "admin@[192.0.2.1]", true},
		{"admin@[2001:db8::1]", "admin@[2001:db8::1]", true},
		{"admin@[not-an-ip]", "", false},
		{"admin@[999.1.1.1]", "", false},
		{"admin@[IPv6:2001:db8::1]", "", false},
		{"admin@siloserver", "", false},
		{"admin@", "", false},
		{"@example.com", "", false},
		{"admin@example.", "", false},
		{"admin@.example", "", false},
		{"", "", false},
		{"   ", "", false},
		{"not an email", "", false},
		{"Admin <admin@example.com>", "", false},
		{"admin@example.com, other@example.com", "", false},
	} {
		got, err := ValidateEmail(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateEmail(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

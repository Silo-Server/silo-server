package bookid

import "testing"

func TestCanonicalISBN(t *testing.T) {
	tests := []struct {
		raw  string
		want string // "" means not an ISBN
	}{
		{"0306406152", "9780306406157"},
		{"0-306-40615-2", "9780306406157"},
		{" 0 306 40615 2 ", "9780306406157"},
		{"080442957X", "9780804429573"},
		{"080442957x", "9780804429573"},
		{"0-8044-2957-X", "9780804429573"},
		{"ISBN-13: 978-0-306-40615-7", "9780306406157"},
		{"ISBN-10: 0-306-40615-2", "9780306406157"},
		{"ISBN 0-306-40615-2", "9780306406157"},
		{"isbn:9780306406157", "9780306406157"},
		{"urn:isbn:9780306406157", "9780306406157"},
		{"URN:ISBN:0306406152", "9780306406157"},
		{"9780306406157", "9780306406157"},
		{"978-0-306-40615-7", "9780306406157"},
		{"9791090636071", "9791090636071"},
		{"979-10-90636-07-1", "9791090636071"},
		// A valid EAN-13 that is not an ISBN.
		{"4006381333931", ""},
		// Wrong check digits.
		{"0306406153", ""},
		{"9780306406158", ""},
		// X anywhere but the last place of an ISBN-10.
		{"X306406152", ""},
		// Wrong lengths.
		{"030640615", ""},
		{"97803064061570", ""},
		{"978030640615", ""},
		{"", ""},
		{"   ", ""},
		{"not an isbn", ""},
	}
	for _, tt := range tests {
		got, ok := CanonicalISBN(tt.raw)
		if ok != (tt.want != "") || got != tt.want {
			t.Errorf("CanonicalISBN(%q) = %q, %v; want %q", tt.raw, got, ok, tt.want)
		}
	}
}

func TestCanonicalProviderID(t *testing.T) {
	tests := []struct {
		provider, value, want string
	}{
		{"isbn", "0-306-40615-2", "9780306406157"},
		{" ISBN ", "0306406152", "9780306406157"},
		{"isbn", "unparseable", "unparseable"},
		{"asin", "0306406152", "0306406152"},
		{"hardcover", "978-0-306-40615-7", "978-0-306-40615-7"},
	}
	for _, tt := range tests {
		if got := CanonicalProviderID(tt.provider, tt.value); got != tt.want {
			t.Errorf("CanonicalProviderID(%q, %q) = %q; want %q", tt.provider, tt.value, got, tt.want)
		}
	}
}

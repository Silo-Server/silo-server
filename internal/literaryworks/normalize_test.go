package literaryworks

import "testing"

func TestNormalizeKey(t *testing.T) {
	tests := map[string]string{
		"Project Hail Mary":              "project hail mary",
		"The Last Adventure: A Novel":    "last adventure novel",
		"  A   Constance-Verity Tale!  ": "constance verity tale",
	}
	for input, want := range tests {
		if got := normalizeKey(input); got != want {
			t.Fatalf("normalizeKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPersonKey(t *testing.T) {
	if got := personKey([]string{" Andy Weir ", "Other"}); got != "andy weir" {
		t.Fatalf("personKey = %q, want primary normalized author", got)
	}
}

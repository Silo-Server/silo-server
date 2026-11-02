package catalog

import "testing"

func TestBoxSetTitle(t *testing.T) {
	for name, want := range map[string]string{
		"Harry Potter Collection":           "Harry Potter Box Set",
		"The Lord of the Rings Collection":  "The Lord of the Rings Box Set",
		"Star Wars collection":              "Star Wars Box Set",
		"Harry Potter Collection ":          "Harry Potter Box Set",
		"Collection":                        "Collection",
		"The Collection Collection":         "The Collection Box Set",
		"Harry Potter - Filmreihe":          "Harry Potter - Filmreihe",
		"Collector's Collectionism Trilogy": "Collector's Collectionism Trilogy",
	} {
		if got := BoxSetTitle(name); got != want {
			t.Errorf("BoxSetTitle(%q) = %q, want %q", name, got, want)
		}
	}
}

package imagesize

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParse(t *testing.T) {
	valid := map[string]Size{
		"":         Unset,
		"small":    Small,
		"medium":   Medium,
		"large":    Large,
		"original": Original,
		"  Large ": Large,
		"ORIGINAL": Original,
	}
	for raw, want := range valid {
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("Parse(%q) = %q, want %q", raw, got, want)
		}
	}

	for _, raw := range []string{"tiny", "w500", "smal", "huge", "0"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("Parse(%q) = nil error, want rejection", raw)
		}
	}
}

func TestFromRequest(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		got, err := FromRequest(httptest.NewRequest(http.MethodGet, "/api/v1/items", nil))
		if err != nil || got != Unset {
			t.Fatalf("FromRequest = (%q, %v), want (Unset, nil)", got, err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		got, err := FromRequest(httptest.NewRequest(http.MethodGet, "/api/v1/items?image_size=large", nil))
		if err != nil || got != Large {
			t.Fatalf("FromRequest = (%q, %v), want (large, nil)", got, err)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		if _, err := FromRequest(httptest.NewRequest(http.MethodGet, "/api/v1/items?image_size=enormous", nil)); err == nil {
			t.Fatal("FromRequest = nil error, want rejection")
		}
	})

	t.Run("nil request", func(t *testing.T) {
		got, err := FromRequest(nil)
		if err != nil || got != Unset {
			t.Fatalf("FromRequest(nil) = (%q, %v), want (Unset, nil)", got, err)
		}
	})
}

func TestVariantMatrix(t *testing.T) {
	want := map[string]map[Size]string{
		"poster": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w780",
			Original: "original",
		},
		"still": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w780",
			Original: "original",
		},
		"backdrop": {
			Small:    "w300",
			Medium:   "w1920",
			Large:    "w1920",
			Original: "original",
		},
		"logo": {
			Small:    "w500",
			Medium:   "w500",
			Large:    "w1280",
			Original: "original",
		},
		"profile": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w500",
			Original: "original",
		},
	}

	for imageType, sizes := range want {
		for size, wantVariant := range sizes {
			if got := Variant(imageType, size); got != wantVariant {
				t.Errorf("Variant(%q, %q) = %q, want %q", imageType, size, got, wantVariant)
			}
		}
	}
}

// Unset must never produce an empty key: a caller that forgets to branch should
// degrade to the historical default rather than build a broken object key.
func TestVariantUnsetFallsBackToMedium(t *testing.T) {
	for _, imageType := range []string{"poster", "still", "backdrop", "logo", "profile", "mystery"} {
		if got, want := Variant(imageType, Unset), Variant(imageType, Medium); got != want {
			t.Errorf("Variant(%q, Unset) = %q, want %q", imageType, got, want)
		}
	}
}

func TestPluginVariant(t *testing.T) {
	want := map[Size]string{
		Small:    "card",
		Medium:   "featured",
		Large:    "large",
		Original: "original",
		Unset:    "featured",
	}
	for size, wantVariant := range want {
		if got := PluginVariant(size); got != wantVariant {
			t.Errorf("PluginVariant(%q) = %q, want %q", size, got, wantVariant)
		}
	}
}

func TestNextLower(t *testing.T) {
	tests := []struct {
		imageType string
		variant   string
		want      string
		wantOK    bool
	}{
		{"poster", "w780", "w500", true},
		{"poster", "w500", "w300", true},
		{"poster", "w300", "", false},
		{"still", "w780", "w500", true},
		{"logo", "w1280", "w500", true},
		{"logo", "w500", "", false},
		{"backdrop", "w1920", "w1280", true},
		{"backdrop", "w1280", "w300", true},
		{"backdrop", "w300", "", false},
		{"profile", "w500", "w300", true},
		{"poster", "original", "", false},
		{"poster", "", "", false},
		{"poster", "wide", "", false},
	}
	for _, tt := range tests {
		got, ok := NextLower(tt.imageType, tt.variant)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("NextLower(%q, %q) = (%q, %v), want (%q, %v)", tt.imageType, tt.variant, got, ok, tt.want, tt.wantOK)
		}
	}
}

// A fallback walk must terminate for every type and every starting rung.
func TestNextLowerTerminates(t *testing.T) {
	for _, imageType := range []string{"poster", "still", "backdrop", "logo", "profile"} {
		for _, size := range All {
			variant := Variant(imageType, size)
			steps := 0
			for {
				next, ok := NextLower(imageType, variant)
				if !ok {
					break
				}
				variant = next
				steps++
				if steps > 10 {
					t.Fatalf("NextLower(%q, ...) did not terminate from %q", imageType, size)
				}
			}
			if want := Variant(imageType, Small); size != Original && variant != want {
				t.Errorf("walking down %q from %q ended at %q, want %q", imageType, size, variant, want)
			}
		}
	}
}

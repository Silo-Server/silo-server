package imagesize

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
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

func TestVariantMatrix(t *testing.T) {
	want := map[string]map[Size]string{
		"poster": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w1000",
			Original: "original",
		},
		"still": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w1280",
			Original: "original",
		},
		"backdrop": {
			Small:    "w300",
			Medium:   "w1920",
			Large:    "w2560",
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
		"banner": {
			Small:    "w500",
			Medium:   "w500",
			Large:    "w1000",
			Original: "original",
		},
		"thumb": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w1280",
			Original: "original",
		},
		"clearart": {
			Small:    "w500",
			Medium:   "w500",
			Large:    "w1000",
			Original: "original",
		},
		"disc": {
			Small:    "w300",
			Medium:   "w500",
			Large:    "w1000",
			Original: "original",
		},
	}

	for imageType, sizes := range want {
		for size, wantVariant := range sizes {
			if got := Variant(imageType, size); got != wantVariant {
				t.Errorf("Variant(%q, %q) = %q, want %q", imageType, size, got, wantVariant)
			}
		}
		// Every size must name a variant the cacher generates for the type, or
		// a request for it misses.
		generated := artworkkey.VariantNames(imageType)
		for size, variant := range sizes {
			if !slices.Contains(generated, variant) {
				t.Errorf("Variant(%q, %q) = %q is not among the generated variants %v", imageType, size, variant, generated)
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

package metadata

import (
	"slices"
	"testing"
)

type builtinStubProvider struct{ slug string }

func (p *builtinStubProvider) Slug() string       { return p.slug }
func (p *builtinStubProvider) Name() string       { return p.slug }
func (p *builtinStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func TestBuiltinProviderRegistry(t *testing.T) {
	// The test registers under a synthetic capability id so it cannot collide
	// with real registrations (the nfo package self-registers on import).
	RegisterBuiltinProvider("test-builtin-cap", func() Provider {
		return &builtinStubProvider{slug: "test-builtin-cap"}
	})

	ids := BuiltinCapabilityIDs()
	if !slices.Contains(ids, "test-builtin-cap") {
		t.Fatalf("BuiltinCapabilityIDs() = %v, want to contain test-builtin-cap", ids)
	}

	p, ok := builtinProvider("test-builtin-cap")
	if !ok || p == nil {
		t.Fatalf("builtinProvider(test-builtin-cap) = %v, %v; want constructed provider", p, ok)
	}
	if p.Slug() != "test-builtin-cap" {
		t.Errorf("provider slug = %q", p.Slug())
	}

	if _, ok := builtinProvider("unregistered"); ok {
		t.Error("builtinProvider(unregistered) = ok, want miss")
	}
}

// The chain-less fallback must skip capabilities that opt out via
// default_enabled=false (absent defaults to true) — otherwise a disabled
// seeded NFO row (or a default_enabled=false specialist plugin) would
// activate itself the moment a level's enabled-filter yields zero entries.
func TestProviderEligibleForFallback(t *testing.T) {
	cases := []struct {
		name         string
		metadataJSON string
		contentLevel string
		want         bool
	}{
		{"default enabled provider eligible", tmdbCapMetadata, "movie", true},
		{"no metadata eligible", ``, "movie", true},
		{"default_enabled=false excluded", `{"metadata":{"default_priority":{"movie":1},"default_enabled":false}}`, "movie", false},
		{"nfo capability metadata excluded", `{"display_name":"NFO Files","default_priority":{"movie":1,"series":1},"default_enabled":false}`, "movie", false},
		{"default_enabled=false excluded even on declared level", `{"metadata":{"default_priority":{"series":50},"default_enabled":false}}`, "series", false},
		{"unsupported level still excluded", tvdbCapMetadata, "movie", false},
		{"explicit true eligible", `{"metadata":{"default_priority":{"movie":2},"default_enabled":true}}`, "movie", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerEligibleForFallback([]byte(tc.metadataJSON), tc.contentLevel); got != tc.want {
				t.Fatalf("providerEligibleForFallback(%q, %q) = %v, want %v", tc.metadataJSON, tc.contentLevel, got, tc.want)
			}
		})
	}
}

package reasoning

import (
	"slices"
	"testing"
)

func TestProModelCapabilitiesAndValidation(t *testing.T) {
	for _, test := range []struct {
		model  string
		levels []string
	}{
		{"gpt-5-pro", []string{"high"}},
		{"gpt-5-pro-2025-10-06", []string{"high"}},
		{"gpt-5.2-pro", []string{"medium", "high", "xhigh"}},
		{"gpt-5.2-pro-2025-12-11", []string{"medium", "high", "xhigh"}},
		{"gpt-5.4-pro", []string{"medium", "high", "xhigh"}},
		{"gpt-5.4-pro-2026-03-05", []string{"medium", "high", "xhigh"}},
		{"gpt-5.5-pro", []string{"medium", "high", "xhigh"}},
		{"gpt-5.5-pro-2026-04-23", []string{"medium", "high", "xhigh"}},
		{"openai/GPT-5.4-Pro", []string{"medium", "high", "xhigh"}},
		{"gpt-5", []string{"minimal", "low", "medium", "high"}},
		{"gpt-5.2", []string{"none", "low", "medium", "high", "xhigh"}},
		{"gpt-5.4", []string{"none", "low", "medium", "high", "xhigh"}},
		{"gpt-5.5", []string{"none", "low", "medium", "high", "xhigh"}},
	} {
		t.Run(test.model, func(t *testing.T) {
			if levels := ForModel(test.model); !slices.Equal(levels, test.levels) {
				t.Fatalf("ForModel(%q) = %v, want %v", test.model, levels, test.levels)
			}
			if err := Validate(test.model, ""); err != nil {
				t.Fatalf("provider default rejected: %v", err)
			}
			for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
				accepted := Validate(test.model, effort) == nil
				if want := slices.Contains(test.levels, effort); accepted != want {
					t.Errorf("Validate(%q, %q) accepted = %t, want %t", test.model, effort, accepted, want)
				}
			}
		})
	}
}

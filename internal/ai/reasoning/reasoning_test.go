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
		{"gpt-5.2-pro", []string{"medium", "high", "xhigh"}},
		{"gpt-5.4-pro", []string{"medium", "high", "xhigh"}},
		{"gpt-5.5-pro", []string{"medium", "high", "xhigh"}},
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

func TestThirdPartyModelCapabilities(t *testing.T) {
	for _, test := range []struct {
		model  string
		levels []string
	}{
		{"grok-4", []string{"low", "medium", "high"}},
		{"grok-4.5", []string{"low", "medium", "high", "xhigh"}},
		{"grok-4.6", []string{"low", "medium", "high", "xhigh"}},
		{"openai/Grok-4.5", []string{"low", "medium", "high", "xhigh"}},
		{"deepseek-v4.1-flash", []string{"low", "high", "max"}},
		{"deepseek/deepseek-chat", []string{"low", "high", "max"}},
		{"llama-3.3-70b", Levels},
	} {
		t.Run(test.model, func(t *testing.T) {
			if levels := ForModel(test.model); !slices.Equal(levels, test.levels) {
				t.Fatalf("ForModel(%q) = %v, want %v", test.model, levels, test.levels)
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

func TestOmitTemperature(t *testing.T) {
	for _, test := range []struct {
		model  string
		effort string
		want   bool
	}{
		{"gpt-5", "", true},
		{"openai/o3", "", true},
		{"grok-4.5", "", false},
		{"deepseek-v4.1-flash", "", false},
		{"deepseek/deepseek-chat", "", false},
		{"grok-4.5", "low", true},
		{"llama-3.3-70b", "", false},
		{"llama-3.3-70b", "high", true},
	} {
		if got := OmitTemperature(test.model, test.effort); got != test.want {
			t.Errorf("OmitTemperature(%q, %q) = %t, want %t", test.model, test.effort, got, test.want)
		}
	}
}

func TestChatModelCapabilitiesAndValidation(t *testing.T) {
	for _, model := range []string{
		"chat-latest",
		"gpt-5-chat-latest",
		"openai/GPT-5.2-Chat-Latest",
		"openai/CHAT-LATEST",
	} {
		t.Run(model, func(t *testing.T) {
			if levels := ForModel(model); len(levels) != 0 {
				t.Fatalf("ForModel(%q) = %v, want no reasoning levels", model, levels)
			}
			if err := Validate(model, ""); err != nil {
				t.Fatalf("provider default rejected: %v", err)
			}
			for _, effort := range Levels {
				if err := Validate(model, effort); err == nil {
					t.Errorf("Validate(%q, %q) accepted unsupported reasoning effort", model, effort)
				}
			}
		})
	}
}

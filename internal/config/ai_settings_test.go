package config

import "testing"

func TestAISettingsValidateModelReasoningAndAuthentication(t *testing.T) {
	for _, test := range []struct {
		model, effort, auth string
		want                bool
	}{
		{"gpt-4o-mini", "", "api_key", true},
		{"gpt-6.1-sol", "high", "chatgpt", true},
		{"gpt-6.1-sol", "none", "chatgpt", false},
		{"gpt-4o-mini", "high", "api_key", false},
		{"custom-model", "max", "api_key", true},
	} {
		values := map[string]string{"ai.chat_model": test.model, "ai.reasoning_effort": test.effort, "ai.auth_mode": test.auth}
		err := ValidateAdminSettings(values)
		if (err == nil) != test.want {
			t.Fatalf("%v: %v", values, err)
		}
		if test.want {
			cfg, err := LoadFromDB(values)
			if err != nil || cfg.AI.AuthMode != test.auth || cfg.AI.ReasoningEffort != test.effort {
				t.Fatalf("configuration lost AI settings: %v", err)
			}
		}
	}
	if _, err := NormalizeAdminSetting("ai.auth_mode", "unrecognized"); err == nil {
		t.Fatal("unknown authentication accepted")
	}
	if _, err := NormalizeAdminSetting("ai.reasoning_effort", "unrecognized"); err == nil {
		t.Fatal("unknown reasoning level accepted")
	}
}

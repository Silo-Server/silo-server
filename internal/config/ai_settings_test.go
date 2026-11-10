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

func TestAIServiceTierAppliesOnlyToOpenAIAPIKeys(t *testing.T) {
	for _, test := range []struct {
		name, auth, tier, want string
		provider               AIProviderType
	}{
		{"project default", "api_key", "", "", AIProviderOpenAI},
		{"priority", "api_key", "priority", "priority", AIProviderOpenAI},
		{"ultrafast", "api_key", "ultrafast", "ultrafast", AIProviderOpenAI},
		{"other provider", "api_key", "priority", "", AIProviderOpenRouter},
		{"ChatGPT plan", "chatgpt", "ultrafast", "", AIProviderOpenAI},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := AIProviderRegistry{Providers: []AIProvider{{
				ID: "text", Type: test.provider, Name: "Text", BaseURL: "https://api.example.test", APIKey: "test-key", Capabilities: []AICapability{AICapabilityChat},
			}}}
			values := map[string]string{
				AIProvidersSettingKey: registry.Encode(), AITextProviderSettingKey: "text",
				"ai.auth_mode": test.auth, AIServiceTierSettingKey: test.tier,
			}
			if _, err := NormalizeAdminSetting(AIServiceTierSettingKey, test.tier); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFromDB(values)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AI.ServiceTier != test.want {
				t.Fatalf("service tier = %q, error = %v; want %q", cfg.AI.ServiceTier, err, test.want)
			}
		})
	}
	if _, err := NormalizeAdminSetting(AIServiceTierSettingKey, "unknown"); err == nil {
		t.Fatal("unknown service tier accepted")
	}
}

package config

import (
	"strings"
	"testing"
)

// The deployment's providers replace the ones with their ids, whole, join the
// others, and release the ones an earlier declaration owned.
func TestApplyDeclaredAIProviders(t *testing.T) {
	openai, err := NewDeclaredAIProvider("openai-main", AIProviderOpenAI, "", "", "sk-declared", nil)
	if err != nil {
		t.Fatal(err)
	}
	if openai.Name != "OpenAI" || openai.BaseURL != openAIBaseURL || len(openai.Capabilities) != 3 {
		t.Fatalf("declared provider = %+v, want the type's name, address and capabilities", openai)
	}
	stored := AIProviderRegistry{Version: aiProvidersVersion, Providers: []AIProvider{
		{ID: "openai-main", Type: AIProviderOpenAI, Name: "Old OpenAI", BaseURL: "https://proxy.example.com", APIKey: "sk-admin", Capabilities: []AICapability{AICapabilityChat}},
		{ID: "ollama-old", Type: AIProviderOllama, Name: "Ollama", BaseURL: ollamaBaseURL, ManagedBy: "SILO_CONFIG_FILE"},
		{ID: "groq-admin", Type: AIProviderGroq, Name: "Groq", BaseURL: groqBaseURL, APIKey: "gsk"},
	}}.Encode()

	raw, err := ApplyDeclaredAIProviders(stored, []AIProvider{openai}, "SILO_CONFIG_FILE")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := ParseAIProviderRegistry(raw)
	if err != nil {
		t.Fatal(err)
	}
	if registry.Version != aiProvidersVersion || len(registry.Providers) != 3 {
		t.Fatalf("registry = %+v, want three providers at the stored version", registry)
	}
	got, _ := registry.Find("openai-main")
	if got.Name != "OpenAI" || got.BaseURL != openAIBaseURL || got.APIKey != "sk-declared" || got.ManagedBy != "SILO_CONFIG_FILE" {
		t.Errorf("adopted provider = %+v, want the declaration whole, owned by SILO_CONFIG_FILE", got)
	}
	if released, _ := registry.Find("ollama-old"); released.ManagedBy != "" {
		t.Errorf("provider no longer declared = %+v, want it kept and released", released)
	}
	if admin, _ := registry.Find("groq-admin"); admin.APIKey != "gsk" || admin.ManagedBy != "" {
		t.Errorf("admin provider = %+v, want it untouched", admin)
	}
	if again, err := ApplyDeclaredAIProviders(raw, []AIProvider{openai}, "SILO_CONFIG_FILE"); err != nil || again != raw {
		t.Errorf("applying the same declaration again changed the registry: %v", err)
	}

	// A declared provider can't take a name another provider already has.
	groq, err := NewDeclaredAIProvider("groq-declared", AIProviderGroq, "", "", "gsk-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDeclaredAIProviders(raw, []AIProvider{openai, groq}, "SILO_CONFIG_FILE"); err == nil || !strings.Contains(err.Error(), "groq-admin") {
		t.Errorf("name collision: error = %v, want one naming groq-admin, the provider to take over or rename around", err)
	}

	// A provider keeps its type, as an admin's edit does: its bound models
	// are that type's.
	retyped, err := NewDeclaredAIProvider("groq-admin", AIProviderOpenAI, "Retyped", "", "sk-3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDeclaredAIProviders(raw, []AIProvider{retyped}, "SILO_CONFIG_FILE"); err == nil || !strings.Contains(err.Error(), "type cannot change") {
		t.Errorf("retyped provider: error = %v, want a refusal of the type change", err)
	}

	// Two declarations may not share a name; the error names both.
	other, err := NewDeclaredAIProvider("openai-batch", AIProviderOpenAI, "", "", "sk-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDeclaredAIProviders("", []AIProvider{openai, other}, "SILO_CONFIG_FILE"); err == nil || !strings.Contains(err.Error(), "openai-batch") {
		t.Errorf("duplicate declared names: error = %v, want one naming both providers", err)
	}

	// A known type's capabilities are fixed, so declaring them is refused.
	if _, err := NewDeclaredAIProvider("openai-chat", AIProviderOpenAI, "", "", "sk", []AICapability{AICapabilityChat}); err == nil {
		t.Error("capabilities on an OpenAI provider were accepted")
	}
}

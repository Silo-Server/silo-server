package config

import (
	"maps"
	"strings"
	"testing"
)

type convertedEndpoint struct{ baseURL, key string }

type aiConversionCase struct {
	name              string
	stored            map[string]string
	text, speech      convertedEndpoint
	providers         int
	speechCustomAdded bool
}

// The conversion must leave every job on the endpoint and key the legacy
// settings gave it, so an upgrade changes nothing an administrator can see.
func TestPlanAIProviderConversionKeepsEachJobOnItsEndpoint(t *testing.T) {
	type endpoint = convertedEndpoint
	for _, test := range []aiConversionCase{
		{name: "fresh install", stored: map[string]string{}},
		{
			name:      "OpenAI key on the default endpoint also serves speech",
			stored:    map[string]string{"ai.api_key": "sk-text"},
			text:      endpoint{"https://api.openai.com", "sk-text"},
			speech:    endpoint{"https://api.openai.com", "sk-text"},
			providers: 1,
		},
		{
			name:      "legacy subtitle_ai rows",
			stored:    map[string]string{"subtitle_ai.base_url": "https://llm.example.test/v1", "subtitle_ai.api_key": "legacy"},
			text:      endpoint{"https://llm.example.test/v1", "legacy"},
			providers: 1,
		},
		{
			name: "custom endpoint transcribing against the text endpoint",
			stored: map[string]string{
				"ai.base_url": "http://llama.lan:8080", "subtitle_ai.transcribe_enabled": "true",
			},
			text:              endpoint{"http://llama.lan:8080", ""},
			speech:            endpoint{"http://llama.lan:8080", ""},
			providers:         1,
			speechCustomAdded: true,
		},
		{
			name:      "speech key on the text endpoint",
			stored:    map[string]string{"ai.api_key": "sk-text", "ai.asr_api_key": "sk-speech"},
			text:      endpoint{"https://api.openai.com", "sk-text"},
			speech:    endpoint{"https://api.openai.com", "sk-speech"},
			providers: 2,
		},
		{
			name:      "speech key alone on the default endpoint",
			stored:    map[string]string{"ai.auth_mode": "chatgpt", "ai.asr_api_key": "sk-speech"},
			speech:    endpoint{"https://api.openai.com", "sk-speech"},
			providers: 1,
		},
		{
			name: "separate speech endpoint with its own key",
			stored: map[string]string{
				"ai.api_key": "sk-text", "ai.asr_base_url": "https://api.groq.com/openai", "ai.asr_api_key": "gsk-speech",
			},
			text:      endpoint{"https://api.openai.com", "sk-text"},
			speech:    endpoint{"https://api.groq.com/openai", "gsk-speech"},
			providers: 2,
		},
		{
			name: "speech endpoint borrowing the text key",
			stored: map[string]string{
				"ai.base_url": "https://gateway.example.test", "ai.api_key": "shared", "ai.asr_base_url": "http://speaches:8000",
			},
			text:      endpoint{"https://gateway.example.test", "shared"},
			speech:    endpoint{"http://speaches:8000", "shared"},
			providers: 2,
		},
		{
			name:      "Gemini chat surface",
			stored:    map[string]string{"ai.base_url": "https://generativelanguage.googleapis.com/v1beta/openai", "ai.api_key": "g-key"},
			text:      endpoint{"https://generativelanguage.googleapis.com/v1beta/openai", "g-key"},
			providers: 1,
		},
		{
			name:      "local Ollama",
			stored:    map[string]string{"ai.base_url": "http://ollama:11434"},
			text:      endpoint{"http://ollama:11434", ""},
			providers: 1,
		},
		{
			// The chat client adds /v1 to a base URL without it, so both are one
			// OpenRouter endpoint and one provider when the key is shared.
			name: "OpenRouter text without /v1 and speech with it",
			stored: map[string]string{
				"subtitle_ai.base_url": "https://openrouter.ai/api", "subtitle_ai.api_key": "or-key",
				"ai.asr_base_url": "https://openrouter.ai/api/v1", "ai.asr_api_key": "or-key",
			},
			text:      endpoint{"https://openrouter.ai/api/v1", "or-key"},
			speech:    endpoint{"https://openrouter.ai/api/v1", "or-key"},
			providers: 1,
		},
		{
			// The client appends its path after the query, so this never worked.
			name:   "endpoint with a query string",
			stored: map[string]string{"ai.base_url": "https://res.openai.azure.com/openai?api-version=2024-10-21", "ai.api_key": "az"},
		},
		{
			name:   "default endpoint saved without a key never worked",
			stored: map[string]string{"ai.base_url": "https://api.openai.com"},
		},
	} {
		t.Run(test.name, func(t *testing.T) { checkAIConversion(t, test) })
	}
}

func checkAIConversion(t *testing.T, test aiConversionCase) {
	t.Helper()
	writes, err := PlanAIProviderConversion(test.stored)
	if err != nil {
		t.Fatal(err)
	}
	converted := maps.Clone(test.stored)
	maps.Copy(converted, writes)
	if err := ValidateAdminSettings(converted); err != nil {
		t.Fatalf("converted settings do not validate: %v", err)
	}
	cfg, err := LoadFromDB(converted)
	if err != nil {
		t.Fatal(err)
	}
	if got := (convertedEndpoint{cfg.AI.BaseURL, cfg.AI.APIKey}); got != test.text {
		t.Errorf("text = %+v, want %+v", got, test.text)
	}
	if got := (convertedEndpoint{cfg.AI.ASRBaseURL, cfg.AI.ASRAPIKey}); got != test.speech {
		t.Errorf("speech = %+v, want %+v", got, test.speech)
	}
	registry, err := ParseAIProviderRegistry(converted[AIProvidersSettingKey])
	if err != nil || registry.Version != aiProvidersVersion || len(registry.Providers) != test.providers {
		t.Fatalf("registry = %+v, %v; want %d providers at version %d", registry, err, test.providers, aiProvidersVersion)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("converted registry does not validate: %v", err)
	}
	if test.speechCustomAdded {
		provider, _ := registry.Find(cfg.AI.SpeechProviderID)
		if provider.Type != AIProviderCustom || !provider.Supports(AICapabilitySpeech) {
			t.Fatalf("speech provider = %+v", provider)
		}
	}
	again, err := PlanAIProviderConversion(converted)
	if err != nil || again != nil {
		t.Fatalf("second conversion = %v, %v; want nothing to do", again, err)
	}
}

func TestPlanAIProviderConversionKeepsAdministratorBindings(t *testing.T) {
	stored := map[string]string{"ai.api_key": "sk-text", AITextProviderSettingKey: "chosen"}
	writes, err := PlanAIProviderConversion(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := writes[AITextProviderSettingKey]; ok {
		t.Fatalf("conversion replaced the administrator's text provider: %v", writes)
	}
}

func TestValidateAIProviderBindings(t *testing.T) {
	registry := `{"version":1,"providers":[{"id":"groq-1","type":"groq","name":"Groq","base_url":"https://api.groq.com/openai","api_key":"k","capabilities":["chat","speech"]}]}`
	for _, test := range []struct {
		name, key, id, wantErr string
	}{
		{name: "serves the job", key: AISpeechProviderSettingKey, id: "groq-1"},
		{name: "unset", key: AITextProviderSettingKey, id: ""},
		{name: "missing provider", key: AITextProviderSettingKey, id: "gone", wantErr: "not connected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAIProviderBindings(map[string]string{AIProvidersSettingKey: registry, test.key: test.id})
			if test.wantErr == "" && err != nil || test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("err = %v, want %q", err, test.wantErr)
			}
		})
	}
	custom := `{"version":1,"providers":[{"id":"chat-only","type":"custom","name":"Chat","base_url":"http://llm.lan","capabilities":["chat"]}]}`
	if err := ValidateAIProviderBindings(map[string]string{AIProvidersSettingKey: custom, AISpeechProviderSettingKey: "chat-only"}); err == nil {
		t.Fatal("a chat-only provider was accepted for speech-to-text")
	}
}

func TestNormalizeAIProvider(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider AIProvider
		wantErr  string
		wantCaps []AICapability
	}{
		{
			name:     "known type keeps its fixed capabilities",
			provider: AIProvider{ID: "openai-1", Type: AIProviderOpenAI, Name: "OpenAI", BaseURL: "https://api.openai.com/", APIKey: "k", Capabilities: []AICapability{AICapabilityChat}},
			wantCaps: []AICapability{AICapabilityChat, AICapabilityEmbeddings, AICapabilitySpeech},
		},
		{
			name:     "custom capabilities in display order",
			provider: AIProvider{ID: "c-1", Type: AIProviderCustom, Name: "Box", BaseURL: "http://box.lan", Capabilities: []AICapability{AICapabilitySpeech, AICapabilityChat}},
			wantCaps: []AICapability{AICapabilityChat, AICapabilitySpeech},
		},
		{name: "hosted type without a key", provider: AIProvider{ID: "g-1", Type: AIProviderGroq, Name: "Groq", BaseURL: "https://api.groq.com/openai"}, wantErr: "requires an API key"},
		{name: "custom serving nothing", provider: AIProvider{ID: "c-1", Type: AIProviderCustom, Name: "Box", BaseURL: "http://box.lan"}, wantErr: "at least one"},
		{name: "credentials in the URL", provider: AIProvider{ID: "c-1", Type: AIProviderCustom, Name: "Box", BaseURL: "https://user:pw@box.lan", Capabilities: []AICapability{AICapabilityChat}}, wantErr: "credentials"},
		{name: "unknown type", provider: AIProvider{ID: "x-1", Type: "anthropic", Name: "X", BaseURL: "https://x.test"}, wantErr: "unknown AI provider type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeAIProvider(test.provider)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(got.BaseURL, "/") || len(got.Capabilities) != len(test.wantCaps) {
				t.Fatalf("normalized = %+v", got)
			}
			for i, capability := range test.wantCaps {
				if got.Capabilities[i] != capability {
					t.Fatalf("capabilities = %v, want %v", got.Capabilities, test.wantCaps)
				}
			}
		})
	}
}

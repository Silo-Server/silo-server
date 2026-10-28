package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func aiProviderFixture(t *testing.T, values map[string]string) *AdminHandler {
	t.Helper()
	stored := map[string]string{
		config.AIProvidersSettingKey: testAIProviders(t,
			config.AIProvider{ID: "box", Type: config.AIProviderCustom, Name: "Box", BaseURL: "https://box.example.test", APIKey: "box-secret",
				Capabilities: []config.AICapability{config.AICapabilityChat, config.AICapabilitySpeech}},
			config.AIProvider{ID: "spare", Type: config.AIProviderCustom, Name: "Spare", BaseURL: "https://spare.example.test",
				Capabilities: []config.AICapability{config.AICapabilityChat, config.AICapabilityEmbeddings}},
		),
	}
	for key, value := range values {
		stored[key] = value
	}
	return &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: stored}}
}

func TestAIProviderMutationsProtectJobsAndKeys(t *testing.T) {
	newHost, newKey, empty := "https://elsewhere.example.test", "new-secret", ""
	for _, tc := range []struct {
		name     string
		values   map[string]string
		mutate   func(*AdminHandler) error
		wantErr  error
		wantText bool // the text binding survives
	}{
		{
			name:    "delete a provider a job uses",
			values:  map[string]string{config.AISpeechProviderSettingKey: "box"},
			mutate:  func(h *AdminHandler) error { return h.DeleteAIProvider(t.Context(), "box") },
			wantErr: ErrAIProviderConflict,
		},
		{
			name:   "delete the text provider kept while ChatGPT serves text",
			values: map[string]string{config.AITextProviderSettingKey: "box", "ai.auth_mode": "chatgpt"},
			mutate: func(h *AdminHandler) error { return h.DeleteAIProvider(t.Context(), "box") },
		},
		{
			name:   "drop a capability a job needs",
			values: map[string]string{config.AISpeechProviderSettingKey: "box"},
			mutate: func(h *AdminHandler) error {
				_, err := h.UpdateAIProvider(t.Context(), "box", AdminAIProviderInput{Capabilities: []string{"chat"}})
				return err
			},
			wantErr: ErrAIProviderConflict,
		},
		{
			name: "move the embeddings provider while embeddings are locked to its model",
			values: map[string]string{
				config.EmbeddingProviderSettingKey: "spare",
				"recommendations.embedding_lock":   `{"base_url":"https://spare.example.test","model":"m","source_dimensions":8}`,
			},
			mutate: func(h *AdminHandler) error {
				_, err := h.UpdateAIProvider(t.Context(), "spare", AdminAIProviderInput{BaseURL: &newHost})
				return err
			},
		},
		{
			name:   "move a keyed provider without its key",
			values: map[string]string{config.AITextProviderSettingKey: "box"},
			mutate: func(h *AdminHandler) error {
				_, err := h.UpdateAIProvider(t.Context(), "box", AdminAIProviderInput{BaseURL: &newHost})
				return err
			},
			wantErr:  ErrAIProviderInvalid,
			wantText: true,
		},
		{
			name:   "move a keyed provider with a key",
			values: map[string]string{config.AITextProviderSettingKey: "box"},
			mutate: func(h *AdminHandler) error {
				_, err := h.UpdateAIProvider(t.Context(), "box", AdminAIProviderInput{BaseURL: &newHost, APIKey: &newKey})
				return err
			},
			wantText: true,
		},
		{
			name:   "move a keyed provider and clear its key",
			values: map[string]string{config.AITextProviderSettingKey: "box"},
			mutate: func(h *AdminHandler) error {
				_, err := h.UpdateAIProvider(t.Context(), "box", AdminAIProviderInput{BaseURL: &newHost, APIKey: &empty})
				return err
			},
			wantText: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := aiProviderFixture(t, tc.values)
			before := h.SettingsRepo.(*fakeServerSettingsStore).values[config.AIProvidersSettingKey]
			err := tc.mutate(h)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			stored := h.SettingsRepo.(*fakeServerSettingsStore).values
			if tc.wantErr != nil && stored[config.AIProvidersSettingKey] != before {
				t.Fatal("a refused change was stored")
			}
			if err := config.ValidateAIProviderBindings(stored); err != nil {
				t.Fatalf("stored bindings no longer resolve: %v", err)
			}
			if got := stored[config.AITextProviderSettingKey] != ""; got != tc.wantText {
				t.Fatalf("text binding kept = %v, want %v", got, tc.wantText)
			}
		})
	}
}

func TestAIProviderCheckSendsStoredKeyOnlyToItsAddress(t *testing.T) {
	var gotAuth string
	provider := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":[{"id":"chat-model"},{"id":"text-embedding-3-small"}]}`))
		}))
	}
	saved, other := provider(), provider()
	defer saved.Close()
	defer other.Close()
	h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
		config.AIProvidersSettingKey: testAIProviders(t, config.AIProvider{ID: "box", Type: config.AIProviderCustom, Name: "Box",
			BaseURL: saved.URL, APIKey: "box-secret", Capabilities: []config.AICapability{config.AICapabilityChat}}),
	}}}
	typed := "typed-secret"
	for _, tc := range []struct {
		name     string
		input    AdminAIProviderCheckInput
		wantAuth string
	}{
		{name: "same address reuses the stored key", input: AdminAIProviderCheckInput{ID: "box", Type: "custom", BaseURL: saved.URL}, wantAuth: "Bearer box-secret"},
		{name: "new address gets no stored key", input: AdminAIProviderCheckInput{ID: "box", Type: "custom", BaseURL: other.URL}},
		{name: "new address with a typed key", input: AdminAIProviderCheckInput{ID: "box", Type: "custom", BaseURL: other.URL, APIKey: &typed}, wantAuth: "Bearer typed-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotAuth = ""
			result, err := h.CheckAIProvider(t.Context(), tc.input)
			if err != nil || !result.Success || len(result.Models) != 2 {
				t.Fatalf("result = %+v, %v", result, err)
			}
			if gotAuth != tc.wantAuth {
				t.Fatalf("Authorization = %q, want %q", gotAuth, tc.wantAuth)
			}
		})
	}
}

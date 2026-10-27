package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

func TestAdminSettingsCheckServiceValidationAndSafeFailure(t *testing.T) {
	h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}}}
	if _, err := h.CheckAdminSettingsConnection(t.Context(), "unknown", nil, nil); !errors.Is(err, ErrAdminSettingsCheckKind) {
		t.Fatal(err)
	}
	// Missing credentials fail locally without a real provider call.
	result, err := h.CheckAdminSettingsConnection(t.Context(), "mdblist", nil, nil)
	if err != nil || result.Success || result.Message != "Connection check failed. Verify the submitted settings and provider availability." {
		t.Fatal(result, err)
	}
	if _, err := (&AdminHandler{}).CheckAdminSettingsConnection(t.Context(), "redis", nil, nil); !errors.Is(err, ErrAdminSettingsUnavailable) {
		t.Fatal(err)
	}
}

func TestTextCheckReportsSafeProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Error("saved text key was not sent")
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"desc = api_key=private-test-key endpoint=https://private.example"}}`))
	}))
	defer server.Close()
	h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
		config.AIProvidersSettingKey: testAIProviders(t, config.AIProvider{
			ID: "text", Type: config.AIProviderCustom, Name: "Text", BaseURL: server.URL, APIKey: "private-test-key",
			Capabilities: []config.AICapability{config.AICapabilityChat},
		}),
		config.AITextProviderSettingKey: "text",
		"ai.chat_model":                 "test-model",
	}}}
	result, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil)
	const want = "Text AI connection failed. Check the model, reasoning level, account connection, and provider usage limits."
	if err != nil || result.Success || result.Message != want {
		t.Fatalf("result = %+v, err = %v; want %q", result, err, want)
	}
}

func TestTranscriptionCheckRetryFailureMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{llm.ErrQuotaExhausted, "Speech-to-text provider quota is exhausted. Check your provider balance and limits."},
		{context.DeadlineExceeded, "Speech-to-text connection check timed out. Check provider availability and try again."},
		{errors.Join(&llm.HTTPError{StatusCode: 429}, context.DeadlineExceeded), "Speech-to-text provider is rate limiting requests. Try again later."},
		{&llm.HTTPError{StatusCode: 503}, "Speech-to-text provider is temporarily unavailable. Try again later."},
		{errors.New("private provider text"), ""},
	} {
		if got := transcriptionCheckFailureMessage(fmt.Errorf("wrapped: %w", tc.err)); got != tc.want {
			t.Errorf("message = %q; want %q", got, tc.want)
		}
	}
}

// This store exercises the real credential service without a provider call.
type settingsCheckChatGPTStore struct{ *fakeServerSettingsStore }

func (s settingsCheckChatGPTStore) GetMany(_ context.Context, keys ...string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = s.values[key]
	}
	return values, nil
}

func (s settingsCheckChatGPTStore) UpdateValueAtomic(ctx context.Context, key string, update func(string) (string, error)) error {
	value, err := update(s.values[key])
	if err != nil {
		return err
	}
	s.values[key] = value
	return nil
}

func TestTranscriptionCheckReportsSafeProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Speech-to-text authentication failed. Replace the provider's API key in AI Services."},
		{http.StatusForbidden, "Speech-to-text access was denied. Check your API key permissions and allowed providers."},
		{http.StatusPaymentRequired, "Speech-to-text provider requires payment. Check your provider balance and spending limit."},
		{http.StatusNotFound, "Speech-to-text endpoint or model is unavailable. Check the provider's base URL, the model, and allowed providers."},
		{http.StatusBadRequest, "Connection check failed. Verify the submitted settings and provider availability."},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-test-key" {
					t.Error("saved ASR key was not sent")
				}
				w.WriteHeader(tc.status)
				// Provider text must neither leak nor control the diagnosis.
				_, _ = w.Write([]byte(`{"error":{"message":"returned 401: private-test-key https://private.example"}}`))
			}))
			defer server.Close()
			h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
				config.AIProvidersSettingKey: testAIProviders(t, config.AIProvider{
					ID: "speech", Type: config.AIProviderCustom, Name: "Speech", BaseURL: server.URL, APIKey: "private-test-key",
					Capabilities: []config.AICapability{config.AICapabilitySpeech},
				}),
				config.AISpeechProviderSettingKey: "speech",
				"ai.asr_model":                    "test-model",
			}}}
			result, err := h.CheckAdminSettingsConnection(t.Context(), "ai_transcription", nil, nil)
			if err != nil || result.Success || result.Message != tc.want {
				t.Fatalf("result = %+v, err = %v; want %q", result, err, tc.want)
			}
		})
	}
}

// The check pins the account it started with: another node activating a
// different account, or this check's own rejection, never moves it onto
// another account's token.
func TestChatGPTSettingsCheckUsesPinnedCredentialSource(t *testing.T) {
	original := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = original })
	credentials, err := json.Marshal(map[string]any{"active": "a", "accounts": []map[string]any{
		{"client_id": "a", "subject": "a-user", "access_token": "a-token", "refresh_token": "a-refresh", "expires_at": time.Now().Add(time.Hour)},
		{"client_id": "b", "subject": "b-user", "access_token": "b-token", "refresh_token": "b-refresh", "expires_at": time.Now().Add(time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeServerSettingsStore{values: map[string]string{"ai.auth_mode": llm.AuthModeChatGPT, "ai.chat_model": "gpt-6.1-sol", chatgpt.CredentialsKey: string(credentials)}}
	service := chatgpt.NewService(settingsCheckChatGPTStore{store})
	newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
		return &fakeAISettingsCheckClient{chat: func(ctx context.Context, _ []llm.Message, _ bool) (string, error) {
			if cfg.ChatGPTTokenSource == nil || cfg.ChatGPTTokenRejected == nil || cfg.ChatGPTPlanRejected == nil || cfg.ChatGPTUsageLimited == nil || cfg.ChatGPTUsageVerified == nil {
				t.Fatal("admin check omitted credential source or rejection callback")
			}
			source, err := cfg.ChatGPTTokenSource(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate an account activation completed by another node.
			if err := (settingsCheckChatGPTStore{store}).UpdateValueAtomic(ctx, chatgpt.CredentialsKey, func(raw string) (string, error) {
				var next map[string]any
				if err := json.Unmarshal([]byte(raw), &next); err != nil {
					return "", err
				}
				next["active"] = "b"
				data, err := json.Marshal(next)
				return string(data), err
			}); err != nil {
				t.Fatal(err)
			}
			if token, err := source(ctx); err != nil || token != "a-token" {
				t.Fatalf("admin source switched account: %q, %v", token, err)
			}
			if err := cfg.ChatGPTTokenRejected(ctx, "a-token", true); err != nil {
				t.Fatal(err)
			}
			if _, err := source(ctx); !errors.Is(err, chatgpt.ErrSignInRequired) {
				t.Fatalf("admin source ignored final rejection: %v", err)
			}
			nextConfig, err := service.CheckTextConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next, err := nextConfig.ChatGPTTokenSource(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if token, err := next(ctx); err != nil || token != "b-token" {
				t.Fatalf("admin rejection changed new active account: %q, %v", token, err)
			}
			return `{"status":"ok"}`, nil
		}}
	}
	h := &AdminHandler{SettingsRepo: store, ChatGPT: service}
	if result, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil); err != nil || !result.Success {
		t.Fatalf("admin check=%+v, %v", result, err)
	}
}

// A check is how an administrator asks whether a usage pause has lifted: it
// uses the paused account and records what the provider answered.
func TestChatGPTSettingsCheckDeliberatelyRechecksPausedUsage(t *testing.T) {
	original := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = original })
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprintf("success=%t", success), func(t *testing.T) {
			credentials, err := json.Marshal(map[string]any{"active": "a", "accounts": []map[string]any{
				{"client_id": "a", "access_token": "a-token", "refresh_token": "a-refresh", "expires_at": time.Now().Add(time.Hour), "usage_limited": true},
			}})
			if err != nil {
				t.Fatal(err)
			}
			store := &fakeServerSettingsStore{values: map[string]string{"ai.auth_mode": llm.AuthModeChatGPT, "ai.chat_model": "gpt-6-luna", chatgpt.CredentialsKey: string(credentials)}}
			service := chatgpt.NewService(settingsCheckChatGPTStore{store})
			newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
				return &fakeAISettingsCheckClient{chat: func(ctx context.Context, _ []llm.Message, _ bool) (string, error) {
					if cfg.ChatGPTUsageLimited == nil || cfg.ChatGPTUsageVerified == nil {
						t.Fatal("usage callbacks missing")
					}
					source, err := cfg.ChatGPTTokenSource(ctx)
					if err != nil {
						t.Fatal(err)
					}
					token, err := source(ctx)
					if err != nil || token != "a-token" {
						t.Fatalf("check cannot bypass usage pause: %q, %v", token, err)
					}
					if !success {
						if err := cfg.ChatGPTUsageLimited(ctx, token); err != nil {
							t.Fatal(err)
						}
						return "", llm.ErrQuotaExhausted
					}
					if err := cfg.ChatGPTUsageVerified(ctx, token); err != nil {
						t.Fatal(err)
					}
					return `{"status":"ok"}`, nil
				}}
			}
			h := &AdminHandler{SettingsRepo: store, ChatGPT: service}
			checked, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			state, err := service.Status(t.Context())
			if err != nil || checked.Success != success || state.Accounts[0].Connected != success || state.Accounts[0].UsageLimited == success {
				t.Fatalf("check=%+v, state=%+v, err=%v", checked, state, err)
			}
		})
	}
}

// The check reaches a Meilisearch key only at the authority it was saved for,
// keeps the saved timeout, and reports provider failures in authored words.
func TestMeilisearchCheckUsesOnlyBoundDraftCredentials(t *testing.T) {
	originalCheck := checkAdminMeilisearchSettingsConnection
	t.Cleanup(func() { checkAdminMeilisearchSettingsConnection = originalCheck })
	for _, tc := range []struct {
		name    string
		draft   map[string]string
		wantKey string
	}{
		{name: "changed authority", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://other.example.test/base"}},
		{name: "same authority path", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://SEARCH.example.test:443/other"}, wantKey: "SYNTHETIC_STORED"},
		{name: "explicit replacement", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://other.example.test/base", catalog.SearchSettingMeilisearchAPIKey: "SYNTHETIC_DRAFT"}, wantKey: "SYNTHETIC_DRAFT"},
		{name: "explicit clear", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://other.example.test/base", catalog.SearchSettingMeilisearchAPIKey: ""}},
		{name: "no authentication", draft: map[string]string{catalog.SearchSettingMeilisearchAPIKey: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
				catalog.SearchSettingMeilisearchURL:       "https://search.example.test/base",
				catalog.SearchSettingMeilisearchAPIKey:    "SYNTHETIC_STORED",
				catalog.SearchSettingMeilisearchTimeoutMS: "1234",
			}}}
			calls := 0
			checkAdminMeilisearchSettingsConnection = func(ctx context.Context, cfg catalog.CatalogSearchSettings) error {
				calls++
				if cfg.MeilisearchAPIKey != tc.wantKey || cfg.Timeout != 1234*time.Millisecond {
					t.Fatal("wrong credential or changed deadline")
				}
				if wantURL, changed := tc.draft[catalog.SearchSettingMeilisearchURL]; changed && cfg.MeilisearchURL != wantURL {
					t.Fatal("draft URL was changed")
				}
				return ctx.Err()
			}
			result, err := h.CheckAdminSettingsConnection(t.Context(), "meilisearch", tc.draft, slices.Sorted(maps.Keys(tc.draft)))
			if err != nil || !result.Success || calls != 1 {
				t.Fatalf("check = %+v, %v after %d calls", result, err, calls)
			}
		})
	}
	redirected := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{catalog.SearchSettingMeilisearchURL: "http://search.example.test"}}}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{catalog.ErrMeilisearchCheckRedirected, adminMeilisearchRedirectMessage},
		{fmt.Errorf("%w: SYNTHETIC_PROVIDER_DETAIL", catalog.ErrMeilisearchCheckKeyRejected), adminMeilisearchKeyRejectedMessage},
	} {
		checkAdminMeilisearchSettingsConnection = func(context.Context, catalog.CatalogSearchSettings) error { return tc.err }
		if result, err := redirected.CheckAdminSettingsConnection(t.Context(), "meilisearch", nil, nil); err != nil || result.Success || result.Message != tc.want {
			t.Fatalf("check after %v = %+v, %v; want %q", tc.err, result, err, tc.want)
		}
	}
	checkAdminMeilisearchSettingsConnection = func(context.Context, catalog.CatalogSearchSettings) error {
		return errors.New("SYNTHETIC_PROVIDER_DETAIL")
	}
	result, err := redirected.CheckAdminSettingsConnection(t.Context(), "meilisearch", nil, nil)
	if err != nil || result.Success || strings.Contains(result.Message, "SYNTHETIC_PROVIDER_DETAIL") {
		t.Fatalf("check exposed provider detail: %+v, %v", result, err)
	}
}

// The embedding check fails when Silo could not store what the model returns
// or the stored embedding lock rejects the configuration, and names the
// model's dimensions when it passes.
func TestCheckRecommendationsEmbeddingConnection(t *testing.T) {
	originalFactory := newAdminEmbeddingsSettingsCheckClient
	t.Cleanup(func() { newAdminEmbeddingsSettingsCheckClient = originalFactory })

	const baseURL, model = "http://ollama:11434", "qwen3-embedding:0.6b"
	vectorOf := func(n int) [][]float32 { return [][]float32{make([]float32, n)} }
	for _, tc := range []struct {
		name    string
		vectors [][]float32
		err     error
		lock    string
		dirty   map[string]string
		success bool
		want    string
	}{
		{name: "fits", vectors: vectorOf(1024), success: true, want: "returns 1024 dimensions"},
		{name: "too many dimensions", vectors: vectorOf(4096), want: "The model returns 4096 dimensions; Silo stores at most 3072."},
		{name: "exactly the stored size", vectors: vectorOf(3072), success: true, want: "returns 3072 dimensions"},
		{name: "no vectors", vectors: [][]float32{}, want: "returned no vector"},
		{name: "nil vector", vectors: [][]float32{nil}, want: "returned no vector"},
		{name: "empty vector", vectors: [][]float32{{}}, want: "returned no vector"},
		{
			name: "matching lock", vectors: vectorOf(1024), success: true, want: "returns 1024 dimensions",
			lock: embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: baseURL, Model: model, SourceDimensions: 1024}),
		},
		{
			name: "lock on another model", vectors: vectorOf(1024),
			lock: embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: baseURL, Model: "all-minilm", SourceDimensions: 384}),
			want: `Embeddings were created with model "all-minilm", not "qwen3-embedding:0.6b". Reset embeddings before switching models.`,
		},
		{
			name: "same model through another provider", vectors: vectorOf(1024), success: true, want: "returns 1024 dimensions",
			lock: embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: "https://user:secret@other.example", Model: "vendor/" + model, SourceDimensions: 1024}),
		},
		{
			name: "lock with other dimensions", vectors: vectorOf(1024),
			lock: embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: baseURL, Model: model, SourceDimensions: 768}),
			want: "created with 768-dimension vectors, but the model now returns 1024",
		},
		{
			name: "lock pinned to an unstorable model", vectors: vectorOf(1024),
			lock: embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: baseURL, Model: model, SourceDimensions: 4096}),
			want: "created with 4096-dimension vectors, but the model now returns 1024",
		},
		{
			name: "a draft cannot replace the lock", vectors: vectorOf(1024),
			lock:  embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: baseURL, Model: "all-minilm", SourceDimensions: 384}),
			dirty: map[string]string{recommendations.EmbeddingLockSettingKey: ""},
			want:  `model "all-minilm"`,
		},
		{name: "unreadable lock", vectors: vectorOf(1024), lock: "{not json", want: "cannot be read"},
		{name: "provider error", err: errors.New(`Post "http://ollama:11434/v1/embeddings": connection refused`), want: "Connection check failed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newAdminEmbeddingsSettingsCheckClient = func(embeddings.ClientConfig) embeddingsSettingsCheckClient {
				return &fakeEmbeddingsSettingsCheckClient{embed: func(context.Context, []string) ([][]float32, error) {
					return tc.vectors, tc.err
				}}
			}
			stored := map[string]string{
				config.AIProvidersSettingKey: testAIProviders(t, config.AIProvider{
					ID: "box", Type: config.AIProviderOllama, Name: "Ollama", BaseURL: baseURL,
					Capabilities: []config.AICapability{config.AICapabilityChat, config.AICapabilityEmbeddings},
				}),
				config.EmbeddingProviderSettingKey: "box",
				"recommendations.embedding_model":  model,
			}
			if tc.lock != "" {
				stored[recommendations.EmbeddingLockSettingKey] = tc.lock
			}
			handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: stored}}

			result, err := handler.CheckAdminSettingsConnection(t.Context(), "recommendations_embedding", tc.dirty, slices.Collect(maps.Keys(tc.dirty)))
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.success || !strings.Contains(result.Message, tc.want) {
				t.Fatalf("check = %+v, want success=%v and %q", result, tc.success, tc.want)
			}
			if strings.Contains(result.Message, "secret") || strings.Contains(result.Message, "ollama:11434") {
				t.Fatalf("check quotes a base URL: %q", result.Message)
			}
		})
	}
}

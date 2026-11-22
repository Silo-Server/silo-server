package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

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

// A failed text model check names what went wrong in authored words, with the
// status of a request the provider rejected, and never the provider's text.
func TestTextAICheckNamesProviderFailure(t *testing.T) {
	original := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = original })
	registry := `{"providers":[{"id":"custom-1","type":"custom","name":"Local","base_url":"http://llm.example.test/v1","api_key":"SYNTHETIC_KEY","capabilities":["chat"]}]}`
	store := &fakeServerSettingsStore{values: map[string]string{"ai.providers": registry, "ai.text_provider_id": "custom-1", "ai.chat_model": "local-chat"}}
	h := &AdminHandler{SettingsRepo: store}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		name     string
		err      error
		wantKey  string
		wantArgs map[string]string
	}{
		{name: "rejected key", err: &llm.HTTPError{StatusCode: 401}, wantKey: "textAIUnauthorized"},
		{name: "rejected request", err: &llm.HTTPError{StatusCode: 400, Code: "SYNTHETIC_PROVIDER_DETAIL"}, wantKey: "textAIRejected", wantArgs: map[string]string{"status": "400"}},
		{name: "unknown model", err: &llm.HTTPError{StatusCode: 404}, wantKey: "textAINotFound"},
		{name: "key echoed in error fields", err: &llm.HTTPError{StatusCode: 400, Code: "bad_key_SYNTHETIC_KEY", Param: "SYNTHETIC_KEY"}, wantKey: "textAIRejected", wantArgs: map[string]string{"status": "400"}},
		{name: "request too large", err: &llm.HTTPError{StatusCode: 413}, wantKey: "textAIRejected", wantArgs: map[string]string{"status": "413"}},
		{name: "ChatGPT unavailable", err: chatgpt.ErrProvider, wantKey: "textAIUnavailable"},
		{name: "unclassified", err: errors.New("SYNTHETIC_PROVIDER_DETAIL"), wantKey: "textAIFailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newAdminAISettingsCheckClient = func(llm.Config) aiSettingsCheckClient {
				return &fakeAISettingsCheckClient{chat: func(context.Context, []llm.Message, bool) (string, error) {
					return "", fmt.Errorf("SYNTHETIC_PROVIDER_DETAIL echoing SYNTHETIC_KEY: %w", tc.err)
				}}
			}
			result, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil)
			if err != nil || result.Success {
				t.Fatalf("check=%+v, %v", result, err)
			}
			if result.MessageKey != checkMessageKeyPrefix+tc.wantKey || !maps.Equal(result.MessageArgs, tc.wantArgs) {
				t.Fatalf("message key=%q args=%v, want %q %v", result.MessageKey, result.MessageArgs, tc.wantKey, tc.wantArgs)
			}
			if strings.Contains(result.Message, "SYNTHETIC") {
				t.Fatalf("check exposed provider detail: %q", result.Message)
			}
		})
	}
	if !strings.Contains(logs.String(), "SYNTHETIC_PROVIDER_DETAIL") || strings.Contains(logs.String(), "SYNTHETIC_KEY") {
		t.Fatalf("server log must keep the provider's error without the API key:\n%s", logs.String())
	}
}

// A failure the provider reports inside a successful response has no HTTP
// status, so the check must not show one.
func TestTextAICheckDoesNotInventStatusForFailureInResponse(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"code":"SYNTHETIC_CODE","message":"SYNTHETIC_PROVIDER_DETAIL"}}`))
	}))
	t.Cleanup(provider.Close)
	registry := `{"providers":[{"id":"openai-1","type":"openai","name":"OpenAI","base_url":"` + provider.URL + `/v1","api_key":"SYNTHETIC_KEY","capabilities":["chat"]}]}`
	store := &fakeServerSettingsStore{values: map[string]string{"ai.providers": registry, "ai.text_provider_id": "openai-1", "ai.chat_model": "local-chat", "ai.service_tier": "ultrafast"}}
	h := &AdminHandler{SettingsRepo: store}
	result, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil)
	if err != nil || result.Success {
		t.Fatalf("check=%+v, %v", result, err)
	}
	if result.MessageKey != checkMessageKeyPrefix+"textAIRefused" || len(result.MessageArgs) != 0 {
		t.Fatalf("message key=%q args=%v, want textAIRefused without a status", result.MessageKey, result.MessageArgs)
	}
}

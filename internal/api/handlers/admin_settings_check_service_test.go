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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/config"
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
	const want = "Text AI provider rejected the check request (status 400). Check the model, reasoning level, and service tier. The server log has the provider's error."
	if err != nil || result.Success || result.Message != want {
		t.Fatalf("result = %+v, err = %v; want %q", result, err, want)
	}
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
			rec := performSettingsCheckRequest(t, h, "/admin/settings/check/ai_transcription", map[string]any{"values": map[string]string{}, "dirty_keys": []string{}})
			var legacy connectionCheckResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &legacy); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || legacy.Success || legacy.Message != tc.want {
				t.Fatalf("legacy response = %d %s; want safe diagnostic %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestVirtualLibrarySettingsCheckService(t *testing.T) {
	h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}}}
	res, err := h.CheckAdminSettingsConnection(t.Context(), "virtual_library", nil, nil)
	if err != nil || res.Success || res.Message != "Manifest URL is required." {
		t.Fatalf("expected missing manifest error, got res=%+v, err=%v", res, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"org.test","name":"Test","version":"1.0.0","resources":["stream"],"types":["movie","series"]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	res, err = h.CheckAdminSettingsConnection(t.Context(), "virtual_library", map[string]string{
		"virtual_library.manifest_url":        srv.URL + "/manifest.json",
		"virtual_library.allow_insecure_http": "true",
	}, []string{"virtual_library.manifest_url", "virtual_library.allow_insecure_http"})
	if err != nil || !res.Success || res.Message != "Streaming provider manifest verified successfully." {
		t.Fatalf("expected verified manifest, got res=%+v, err=%v", res, err)
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
			// Simulate an account activation completed by another node. Selection
			// catalog validation is covered by the credential service tests.
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
	store.values[chatgpt.CredentialsKey] = string(credentials)
	router := chi.NewRouter()
	router.Post("/api/v1/admin/settings/check/{kind}", h.HandleCheckSettingsConnection)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/check/ai_chat", strings.NewReader(`{}`)))
	var legacy connectionCheckResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &legacy); err != nil || recorder.Code != http.StatusOK || !legacy.Success {
		t.Fatalf("legacy check=%d %s, %v", recorder.Code, recorder.Body.String(), err)
	}
}

func TestChatGPTSettingsCheckDeliberatelyRechecksPausedUsage(t *testing.T) {
	original := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = original })
	for _, legacy := range []bool{false, true} {
		for _, success := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%t success=%t", legacy, success), func(t *testing.T) {
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
				var result connectionCheckResponse
				if legacy {
					router := chi.NewRouter()
					router.Post("/api/v1/admin/settings/check/{kind}", h.HandleCheckSettingsConnection)
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/check/ai_chat", strings.NewReader(`{}`)))
					if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || recorder.Code != http.StatusOK {
						t.Fatalf("legacy check: %s, %v", recorder.Body.String(), err)
					}
				} else {
					checked, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					result.Success = checked.Success
				}
				state, err := service.Status(t.Context())
				if err != nil || result.Success != success || state.Accounts[0].Connected != success || state.Accounts[0].UsageLimited == success {
					t.Fatalf("check=%+v, state=%+v, err=%v", result, state, err)
				}
			})
		}
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

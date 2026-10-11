package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
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
		"ai.base_url":   server.URL,
		"ai.chat_model": "test-model",
		"ai.api_key":    "private-test-key",
	}}}
	result, err := h.CheckAdminSettingsConnection(t.Context(), "ai_chat", nil, nil)
	const want = "Text AI connection failed. Check the model, reasoning level, account connection, and provider usage limits."
	if err != nil || result.Success || result.Message != want {
		t.Fatalf("result = %+v, err = %v; want %q", result, err, want)
	}
}

func TestTranscriptionCheckReportsSafeProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Speech-to-text authentication failed. Replace the speech-to-text API key, or clear it to use the Text AI key."},
		{http.StatusForbidden, "Speech-to-text access was denied. Check your API key permissions and allowed providers."},
		{http.StatusPaymentRequired, "Speech-to-text provider requires payment. Check your provider balance and spending limit."},
		{http.StatusNotFound, "Speech-to-text endpoint or model is unavailable. Check the base URL, model, and allowed providers."},
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
				"ai.asr_base_url": server.URL,
				"ai.asr_model":    "test-model",
				"ai.asr_api_key":  "private-test-key",
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

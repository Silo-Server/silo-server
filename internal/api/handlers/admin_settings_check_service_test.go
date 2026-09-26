package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"testing"
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

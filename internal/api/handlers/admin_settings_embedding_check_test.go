package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

func embeddingLockJSON(t *testing.T, lock recommendations.EmbeddingLock) string {
	t.Helper()
	raw, err := lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The embedding check fails when Silo could not store what the model returns
// or the stored embedding lock rejects the configuration, and names the
// model's dimensions when it passes. Its failure messages reach v2 callers.
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
			name: "lock on another provider", vectors: vectorOf(1024),
			lock: embeddingLockJSON(t, recommendations.EmbeddingLock{BaseURL: "https://user:secret@other.example", Model: model, SourceDimensions: 1024}),
			want: "Embeddings were created with a different provider base URL. Reset embeddings before switching providers.",
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
				"recommendations.embedding_base_url": baseURL,
				"recommendations.embedding_model":    model,
			}
			if tc.lock != "" {
				stored[recommendations.EmbeddingLockSettingKey] = tc.lock
			}
			handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: stored}}
			var dirtyKeys []string
			for key := range tc.dirty {
				dirtyKeys = append(dirtyKeys, key)
			}

			// The v2 service returns authored messages and hides provider text.
			result, err := handler.CheckAdminSettingsConnection(t.Context(), "recommendations_embedding", tc.dirty, dirtyKeys)
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.success || !strings.Contains(result.Message, tc.want) {
				t.Fatalf("v2 check = %+v, want success=%v and %q", result, tc.success, tc.want)
			}
			if strings.Contains(result.Message, "secret") || strings.Contains(result.Message, "ollama:11434") {
				t.Fatalf("v2 check quotes a base URL: %q", result.Message)
			}

			// The v1 route keeps its wire shape.
			rec := performSettingsCheckRequest(t, handler, "/admin/settings/check/recommendations_embedding", map[string]any{
				"values": tc.dirty, "dirty_keys": dirtyKeys,
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("v1 status = %d; body=%s", rec.Code, rec.Body.String())
			}
			var response connectionCheckResponse
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Success != tc.success || (tc.err == nil && !strings.Contains(response.Message, tc.want)) {
				t.Fatalf("v1 check = %+v, want success=%v and %q", response, tc.success, tc.want)
			}
		})
	}
}

// The check sends the saved embedding token only to the base URL it was saved
// for, as the AI checks do. A draft that points the check at another scheme,
// host or port, directly or through the legacy provider setting, is checked
// with the token it carries itself, or none; the legacy OpenAI key and an
// environment-managed token are held back the same way. A draft on the saved
// host, such as a new model or path, still uses the saved token.
func TestEmbeddingCheckSendsTheSavedTokenOnlyToItsBaseURL(t *testing.T) {
	originalFactory := newAdminEmbeddingsSettingsCheckClient
	t.Cleanup(func() { newAdminEmbeddingsSettingsCheckClient = originalFactory })
	var captured *embeddings.ClientConfig
	newAdminEmbeddingsSettingsCheckClient = func(cfg embeddings.ClientConfig) embeddingsSettingsCheckClient {
		captured = &cfg
		return &fakeEmbeddingsSettingsCheckClient{}
	}

	const (
		baseKey, modelKey, tokenKey = "recommendations.embedding_base_url", "recommendations.embedding_model", "recommendations.embedding_auth_token"
		saved, other                = "https://api.example.test/v1", "https://other.example.test/v1"
	)
	savedSettings := map[string]string{baseKey: saved, tokenKey: "saved-token"}
	for _, tc := range []struct {
		name      string
		stored    map[string]string
		bootstrap map[string]string
		draft     map[string]string
		wantURL   string
		wantToken string
	}{
		{name: "new model", stored: savedSettings, draft: map[string]string{modelKey: "other-model"}, wantURL: saved, wantToken: "saved-token"},
		{name: "same host, other path", stored: savedSettings, draft: map[string]string{baseKey: "https://API.example.test/v2"}, wantURL: "https://API.example.test/v2", wantToken: "saved-token"},
		{name: "other host", stored: savedSettings, draft: map[string]string{baseKey: other}, wantURL: other},
		{name: "other scheme", stored: savedSettings, draft: map[string]string{baseKey: "http://api.example.test/v1"}, wantURL: "http://api.example.test/v1"},
		{name: "other port", stored: savedSettings, draft: map[string]string{baseKey: "https://api.example.test:8443/v1"}, wantURL: "https://api.example.test:8443/v1"},
		{name: "other host, cleared token", stored: savedSettings, draft: map[string]string{baseKey: other, tokenKey: ""}, wantURL: other},
		{name: "other host with its own token", stored: savedSettings, draft: map[string]string{baseKey: other, tokenKey: "draft-token"}, wantURL: other, wantToken: "draft-token"},
		{
			name:   "legacy OpenAI key",
			stored: map[string]string{baseKey: saved, "recommendations.openai_api_key": "legacy-key"},
			draft:  map[string]string{baseKey: other}, wantURL: other,
		},
		{
			name:   "legacy provider resolving elsewhere",
			stored: map[string]string{"recommendations.embedding_provider": "openai", tokenKey: "saved-token"},
			draft:  map[string]string{"recommendations.embedding_provider": "ollama"}, wantURL: "http://ollama:11434",
		},
		{
			name: "environment-managed token", stored: map[string]string{baseKey: saved}, bootstrap: map[string]string{tokenKey: "env-token"},
			draft: map[string]string{baseKey: other}, wantURL: other,
		},
		{
			name: "environment-managed token, saved base URL", stored: map[string]string{baseKey: saved}, bootstrap: map[string]string{tokenKey: "env-token"},
			draft: map[string]string{modelKey: "other-model"}, wantURL: saved, wantToken: "env-token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: maps.Clone(tc.stored)}, BootstrapSensitiveValues: tc.bootstrap}
			dirtyKeys := slices.Sorted(maps.Keys(tc.draft))

			captured = nil
			if _, err := handler.CheckAdminSettingsConnection(t.Context(), "recommendations_embedding", tc.draft, dirtyKeys); err != nil {
				t.Fatal(err)
			}
			if captured == nil || captured.BaseURL != tc.wantURL || captured.APIKey != tc.wantToken {
				t.Fatalf("v2 check called %+v, want base URL %q with token %q", captured, tc.wantURL, tc.wantToken)
			}

			captured = nil
			rec := performSettingsCheckRequest(t, handler, "/admin/settings/check/recommendations_embedding", map[string]any{
				"values": tc.draft, "dirty_keys": dirtyKeys,
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("v1 status = %d; body=%s", rec.Code, rec.Body.String())
			}
			if captured == nil || captured.BaseURL != tc.wantURL || captured.APIKey != tc.wantToken {
				t.Fatalf("v1 check called %+v, want base URL %q with token %q", captured, tc.wantURL, tc.wantToken)
			}
		})
	}
}

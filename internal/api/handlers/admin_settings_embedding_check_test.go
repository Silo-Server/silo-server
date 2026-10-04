package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

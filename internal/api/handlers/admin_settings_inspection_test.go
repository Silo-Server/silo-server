package handlers

import (
	"context"
	"maps"
	"testing"
)

func TestAdminReasoningChangesUseSelectedModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stored  map[string]string
		changes map[string]string
		wantErr bool
	}{
		{"effort only", map[string]string{"ai.chat_model": "gpt-6-luna"}, map[string]string{"ai.reasoning_effort": "high"}, false},
		{"legacy model", map[string]string{"subtitle_ai.chat_model": "gpt-6-luna"}, map[string]string{"ai.reasoning_effort": "high"}, false},
		{"model only retains effort", map[string]string{"ai.chat_model": "gpt-6-luna", "ai.reasoning_effort": "high"}, map[string]string{"ai.chat_model": "gpt-4o-mini"}, true},
		{"clear effort with model change", map[string]string{"ai.chat_model": "gpt-6-luna", "ai.reasoning_effort": "high"}, map[string]string{"ai.chat_model": "gpt-4o-mini", "ai.reasoning_effort": ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeServerSettingsStore{values: maps.Clone(tc.stored)}
			h := &AdminHandler{SettingsRepo: store}
			_, err := h.UpdateAdminSettings(t.Context(), tc.changes, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("UpdateAdminSettings error = %v, want error %t", err, tc.wantErr)
			}
			if tc.wantErr {
				if !maps.Equal(store.values, tc.stored) {
					t.Fatal("rejected update changed stored settings")
				}
				return
			}
			for key, value := range tc.changes {
				if store.values[key] != value {
					t.Fatalf("stored %s = %q, want %q", key, store.values[key], value)
				}
			}
		})
	}
}

// This store deliberately returns the same map to detect mutation by readers.
type sharedAdminInspectionStore struct{ fakeServerSettingsStore }

func (s *sharedAdminInspectionStore) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}
func TestAdminSettingsInspectionRedactionDoesNotMutateStore(t *testing.T) {
	s := &sharedAdminInspectionStore{fakeServerSettingsStore{values: map[string]string{"tmdb.api_key": "synthetic-secret", "ai.chatgpt.credentials": `{"refresh_token":"synthetic"}`, "server.log_level": "debug"}}}
	h := &AdminHandler{SettingsRepo: s}
	for _, effective := range []bool{false, true} {
		values, err := h.InspectAdminSettings(t.Context(), effective)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := values["tmdb.api_key"]; ok {
			t.Fatal("secret returned")
		}
		if _, ok := values["ai.chatgpt.credentials"]; ok {
			t.Fatal("ChatGPT credentials returned")
		}
		if s.values["tmdb.api_key"] != "synthetic-secret" {
			t.Fatal("shared store mutated")
		}
		if effective && values["database.max_connections"] != "20" {
			t.Fatal("runtime default missing")
		}
	}
	status, err := h.InspectAdminSensitiveSettings(t.Context())
	if err != nil || len(status.Configured) != 1 || status.Configured[0] != "tmdb.api_key" {
		t.Fatal(status, err)
	}
}

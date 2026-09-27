package handlers

import (
	"context"
	"testing"
)

// This store deliberately returns the same map to detect mutation by readers.
type sharedAdminInspectionStore struct{ fakeServerSettingsStore }

func (s *sharedAdminInspectionStore) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}
func TestAdminSettingsInspectionRedactionDoesNotMutateStore(t *testing.T) {
	s := &sharedAdminInspectionStore{fakeServerSettingsStore{values: map[string]string{"tmdb.api_key": "synthetic-secret", "server.log_level": "debug"}}}
	h := &AdminHandler{SettingsRepo: s}
	for _, effective := range []bool{false, true} {
		values, err := h.InspectAdminSettings(t.Context(), effective)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := values["tmdb.api_key"]; ok {
			t.Fatal("secret returned")
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

package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestUpdateAdminAutoscanConnectionView(t *testing.T) {
	var calls []autoscan.Connection
	store := &fakeAutoscanStore{updateConnectionFn: func(c autoscan.Connection) (autoscan.Connection, error) {
		calls = append(calls, c)
		if c.APIKeyRef == "" {
			c.APIKeyRef = "retained-key"
		}
		return c, nil
	}}
	h := NewAutoscanHandler(store, new(fakeAutoscanTriggerer))
	for _, in := range []AdminAutoscanConnectionUpdateInput{{Name: " "}, {Name: "name", RequestIntegrationID: new(" ")}} {
		_, err := h.UpdateAdminAutoscanConnection(t.Context(), "connection-a", in)
		if !errors.Is(err, ErrAdminAutoscanConnectionUpdateInvalid) {
			t.Fatal(err)
		}
	}
	if len(calls) != 0 {
		t.Fatal("invalid update wrote store")
	}
	out, err := h.UpdateAdminAutoscanConnection(t.Context(), " connection-a ", AdminAutoscanConnectionUpdateInput{Name: " name ", Kind: " sonarr ", BaseURL: " https://example.invalid ", APIKeyRef: " secret ", RequestIntegrationID: new(" ")})
	if err != nil || out.ID != "connection-a" || !out.HasAPIKey || out.Name != "name" || out.Kind != "sonarr" || out.RequestIntegrationID != nil || len(calls) != 1 || calls[0].ID != "connection-a" || calls[0].APIKeyRef != "secret" {
		t.Fatal(err, out, calls)
	}
	out, err = h.UpdateAdminAutoscanConnection(t.Context(), " connection-a ", AdminAutoscanConnectionUpdateInput{Name: "linked", RequestIntegrationID: new(" integration ")})
	if err != nil || !out.HasAPIKey || calls[1].APIKeyRef != "" || *calls[1].RequestIntegrationID != "integration" || calls[1].BaseURL != "" {
		t.Fatal(err, calls)
	}
	_, err = (&AutoscanHandler{}).UpdateAdminAutoscanConnection(t.Context(), " connection-a ", AdminAutoscanConnectionUpdateInput{})
	if !errors.Is(err, ErrAdminAutoscanConnectionUpdateUnavailable) {
		t.Fatal(err)
	}
}

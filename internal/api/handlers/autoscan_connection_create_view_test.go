package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestCreateAdminAutoscanConnectionView(t *testing.T) {
	var calls []autoscan.Connection
	store := &fakeAutoscanStore{createConnectionFn: func(c autoscan.Connection) (autoscan.Connection, error) {
		calls = append(calls, c)
		c.ID = "created"
		return c, nil
	}}
	h := NewAutoscanHandler(store, new(fakeAutoscanTriggerer))
	for _, in := range []AdminAutoscanConnectionCreateInput{{Name: " "}, {Name: "name", RequestIntegrationID: new(" ")}} {
		_, err := h.CreateAdminAutoscanConnection(t.Context(), in)
		if !errors.Is(err, ErrAdminAutoscanConnectionCreateInvalid) {
			t.Fatal(err)
		}
	}
	if len(calls) != 0 {
		t.Fatal("invalid create wrote store")
	}
	out, err := h.CreateAdminAutoscanConnection(t.Context(), AdminAutoscanConnectionCreateInput{Name: " name ", Kind: " sonarr ", BaseURL: " https://example.invalid ", APIKeyRef: " secret ", RequestIntegrationID: new(" ")})
	if err != nil || out.ID != "created" || !out.HasAPIKey || out.Name != "name" || out.Kind != "sonarr" || out.RequestIntegrationID != nil || len(calls) != 1 || calls[0].ID != "" || calls[0].APIKeyRef != "secret" {
		t.Fatal(err, out, calls)
	}
	_, err = h.CreateAdminAutoscanConnection(t.Context(), AdminAutoscanConnectionCreateInput{Name: "linked", RequestIntegrationID: new(" integration ")})
	if err != nil || *calls[1].RequestIntegrationID != "integration" || calls[1].BaseURL != "" {
		t.Fatal(err, calls)
	}
	_, err = (&AutoscanHandler{}).CreateAdminAutoscanConnection(t.Context(), AdminAutoscanConnectionCreateInput{})
	if !errors.Is(err, ErrAdminAutoscanConnectionCreateUnavailable) {
		t.Fatal(err)
	}
}

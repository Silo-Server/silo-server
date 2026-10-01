package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type recordingAutoscanConnectionTester struct {
	*fakeAutoscanTriggerer
	id         string
	connection *autoscan.Connection
}

func (f *recordingAutoscanConnectionTester) TestConnectionByID(_ context.Context, id string) (autoscan.ConnectionTestResult, error) {
	f.id = id
	return autoscan.ConnectionTestResult{OK: true}, nil
}
func (f *recordingAutoscanConnectionTester) TestConnection(_ context.Context, c autoscan.Connection) (autoscan.ConnectionTestResult, error) {
	f.connection = &c
	return autoscan.ConnectionTestResult{OK: true}, nil
}
func TestAdminAutoscanConnectionTestDispatch(t *testing.T) {
	f := &recordingAutoscanConnectionTester{fakeAutoscanTriggerer: new(fakeAutoscanTriggerer)}
	h := NewAutoscanHandler(&fakeAutoscanStore{}, f)
	_, err := h.TestAdminAutoscanConnection(t.Context(), AdminAutoscanConnectionTestInput{ConnectionID: new(" stored "), BaseURL: "ignored", APIKeyRef: "ignored", RequestIntegrationID: new("ignored")})
	if err != nil || f.id != "stored" || f.connection != nil {
		t.Fatal(err, f)
	}
	_, err = h.TestAdminAutoscanConnection(t.Context(), AdminAutoscanConnectionTestInput{BaseURL: " https://example.invalid ", APIKeyRef: " reference ", RequestIntegrationID: new("integration")})
	if err != nil || f.connection.BaseURL != "https://example.invalid" || f.connection.APIKeyRef != "reference" || *f.connection.RequestIntegrationID != "integration" {
		t.Fatal(err, f.connection)
	}
	_, err = (&AutoscanHandler{}).TestAdminAutoscanConnection(t.Context(), AdminAutoscanConnectionTestInput{})
	if !errors.Is(err, ErrAdminAutoscanConnectionTestUnavailable) {
		t.Fatal(err)
	}
}

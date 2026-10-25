package apiv2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/apiusage"
)

type fakeAPIClients struct{}

func (fakeAPIClients) TopClients(_ context.Context, _ time.Time, hours, _ int) (*apiusage.Clients, error) {
	hour := time.Date(2031, 3, 1, 12, 0, 0, 0, time.UTC)
	return &apiusage.Clients{Hours: hours, From: hour, TotalRequests: 40, Clients: []apiusage.Client{
		{SessionID: "s1", UserID: 1, Username: "laura", UserAgent: "poller", Requests: 30, Refused: 4, PeakHour: 30, FirstHour: hour, LastHour: hour, SessionActive: true},
	}}, nil
}

func (fakeAPIClients) ClientEndpoints(_ context.Context, _ time.Time, sessionID string, _ int) ([]apiusage.Endpoint, error) {
	if sessionID != "s1" {
		return nil, apiusage.ErrSessionNotFound
	}
	return []apiusage.Endpoint{{Method: "GET", Route: "/api/v2/catalog/items/{id}", Requests: 30, Refused: 4}}, nil
}

// Per-session request counts name accounts and devices, so only admins read them.
func TestAdminAPIClients(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminAPIClients = fakeAPIClients{}
	h := NewHandler(deps)
	list := Prefix + "/admin/stats/api-clients?hours=6"
	endpoints := Prefix + "/admin/stats/api-clients/s1/endpoints"
	for _, path := range []string{list, endpoints} {
		requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
		requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	}

	rec := do(t, h, "GET", list, "", bearer(adminToken))
	var clients AdminAPIClients
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &clients) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if clients.Hours != 6 || clients.TotalRequests != 40 || len(clients.Clients) != 1 ||
		clients.Clients[0].SessionID != "s1" || clients.Clients[0].Refused != 4 || !clients.Clients[0].SessionActive {
		t.Fatalf("clients = %+v", clients)
	}

	rec = do(t, h, "GET", endpoints, "", bearer(adminToken))
	var routes AdminAPIClientEndpoints
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &routes) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if len(routes.Endpoints) != 1 || routes.Endpoints[0].Route != "/api/v2/catalog/items/{id}" {
		t.Fatalf("endpoints = %+v", routes)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/stats/api-clients/other/endpoints", "", bearer(adminToken)), TypeNotFound)
}

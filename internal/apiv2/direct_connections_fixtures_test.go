package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

func directConnectionsFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_direct_connections_status_ok", operationID: "getAdminDirectConnectionsStatus", scenario: "An acting administrator reads the API host's direct-connection readiness while the hidden toggle is off.", method: http.MethodGet, path: Prefix + "/admin/direct-connections/status", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/DirectConnectionsStatus"}}
}

type fakeDirectConnections struct{}

func (fakeDirectConnections) Status(context.Context, bool) (directconn.Status, error) {
	return directconn.Status{CheckedAt: fixedTime(), Features: []directconn.Feature{{Feature: "direct", Kind: "local", State: "off", Reason: "disabled"}}, Knobs: []directconn.Knob{}}, nil
}

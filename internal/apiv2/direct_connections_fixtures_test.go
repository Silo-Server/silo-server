package apiv2

import (
	"context"
	"net/http"
	"net/netip"
	"testing"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

func directConnectionsFixtureCases() []fixtureCase {
	capability := func(name, scenario, token string, enabled bool) fixtureCase {
		return fixtureCase{name: name, operationID: "getDirectConnectionsCapability", scenario: scenario, method: http.MethodGet, path: Prefix + "/capabilities/direct-connections",
			headers: directViewer(token), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/DirectConnectionsCapability",
			deps: func(t *testing.T, deps *Dependencies) {
				e := newDirectTestEnv(t)
				e.host.host.Enabled = enabled
				deps.DirectCoordination = e.coord
			}}
	}
	return []fixtureCase{
		{name: "admin_direct_connections_status_ok", operationID: "getAdminDirectConnectionsStatus", scenario: "An acting administrator reads the API host's direct-connection readiness while the hidden toggle is off.", method: http.MethodGet, path: Prefix + "/admin/direct-connections/status", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/DirectConnectionsStatus"},
		{name: "admin_direct_connections_peers_ok", operationID: "listAdminDirectConnectionPeers", scenario: "An acting administrator lists the devices that have a direct path to the API host: one device with its observed address and ports.", method: http.MethodGet, path: Prefix + "/admin/direct-connections/peers", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/DirectConnectionPeerCollection"},
		capability("get_direct_connections_capability_available", "A signed-in session reads the direct-connections capability on a server whose administrator turned them on, with ACK decimation off and a 32 MiB stream window.", directLoginToken, true),
		capability("get_direct_connections_capability_disabled", "The same read while the administrator has direct connections turned off: no hosts, and not allowed.", directLoginToken, false),
		capability("get_direct_connections_capability_api_key", "An API key reads the capability: available on the server, but not allowed, since only a login session may enroll a device.", apiKeyToken, true),
	}
}

type fakeDirectConnections struct{}

func (fakeDirectConnections) Status(context.Context, bool) (directconn.Status, error) {
	return directconn.Status{CheckedAt: fixedTime(), Features: []directconn.Feature{{Feature: "direct", Kind: "local", State: "off", Reason: "disabled"}}, Knobs: []directconn.Knob{}}, nil
}

func (fakeDirectConnections) Peers(context.Context) []directconn.Peer {
	return []directconn.Peer{{UserID: 7, DeviceID: "living-room-tv", Endpoint: netip.MustParseAddrPort("127.0.0.1:41641"), LocalPort: 8094, Since: fixedTime()}}
}

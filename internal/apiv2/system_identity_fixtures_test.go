package apiv2

import "net/http"

func serverIdentityFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "server_identity_ok", operationID: "getServerIdentity", scenario: "Discovery before login: the deployment's stable server identity, the same at every address.", method: http.MethodGet, path: Prefix + "/system/identity", status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ServerIdentity"},
		{name: "server_connections_ok", operationID: "getServerConnections", scenario: "A signed-in client reads the addresses the deployment offers: the public URL, a connected provider with its overlay origin, and an installed provider whose plugin is not running.", method: http.MethodGet, path: Prefix + "/system/connections", headers: bearer(memberToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/ServerConnectionsDocument"},
	}
}

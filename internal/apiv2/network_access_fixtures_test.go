package apiv2

import "net/http"

func networkAccessFixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	status := "#/components/schemas/NetworkAccessStatus"
	return []fixtureCase{
		{name: "network_access_capabilities_ok", operationID: "getNetworkAccessCapabilities", scenario: "An authenticated account lists the installed overlay-network providers read from plugin manifests.", method: http.MethodGet, path: Prefix + "/network-access/capabilities", headers: bearer(memberToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/NetworkAccessCapabilities"},
		{name: "admin_network_access_status_ok", operationID: "getAdminNetworkAccessStatus", scenario: "The provider's live state on the API host, with overlay origin and addresses.", method: http.MethodGet, path: Prefix + "/admin/network-access/stub/status", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type"}, schema: status},
		{name: "admin_network_access_status_unavailable_host", operationID: "getAdminNetworkAccessStatus", scenario: "A host whose provider plugin is not running answers state unavailable with the supervisor's reason and no updated_at.", method: http.MethodGet, path: Prefix + "/admin/network-access/down/status", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type"}, schema: status},
		{name: "admin_network_access_status_not_found", operationID: "getAdminNetworkAccessStatus", scenario: "A provider slug no enabled installation declares.", method: http.MethodGet, path: Prefix + "/admin/network-access/netbird/status", headers: actingRequestAdmin, status: 404, assertHeaders: []string{"Content-Type"}, schema: problem},
		{name: "admin_network_access_connect_accepted", operationID: "connectNetworkAccess", scenario: "Connect on the named host is acknowledged with the state reached so far; enrollment continues and the auth URL is admin-only.", method: http.MethodPost, path: Prefix + "/admin/network-access/stub/connect", headers: actingRequestAdmin, body: `{"hosts":["api"]}`, status: 202, assertHeaders: []string{"Content-Type"}, schema: status},
		{name: "admin_network_access_disconnect_accepted", operationID: "disconnectNetworkAccess", scenario: "Disconnect without a body acts on every host and is acknowledged with the state reached.", method: http.MethodPost, path: Prefix + "/admin/network-access/stub/disconnect", headers: actingRequestAdmin, status: 202, assertHeaders: []string{"Content-Type"}, schema: status},
		{name: "admin_network_access_unknown_host", operationID: "connectNetworkAccess", scenario: "A host id this deployment does not run the provider on is a validation failure at body.hosts; nothing is applied.", method: http.MethodPost, path: Prefix + "/admin/network-access/stub/connect", headers: actingRequestAdmin, body: `{"hosts":["node:99"]}`, status: 422, assertHeaders: []string{"Content-Type"}, schema: problem},
	}
}

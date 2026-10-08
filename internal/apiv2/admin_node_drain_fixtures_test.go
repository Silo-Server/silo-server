package apiv2

import "net/http"

func adminNodeDrainFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_node_capabilities_unavailable", operationID: "getAdminNodeCapabilities", method: http.MethodGet, path: Prefix + "/admin/nodes/capabilities", headers: bearer(adminToken), status: http.StatusOK, schema: "#/components/schemas/AdminNodeDrainCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, scenario: "The administrator discovers unavailable drain and disabled commissioning services without guessing a build version."},
		{name: "admin_node_drain_unavailable", operationID: "getAdminNodeDrain", method: http.MethodGet, path: Prefix + "/admin/nodes/17/drain", headers: bearer(adminToken), status: http.StatusServiceUnavailable, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A missing drain service cannot return a retirement receipt."},
		{name: "admin_node_drain_requires_authentication", operationID: "getAdminNodeDrain", method: http.MethodGet, path: Prefix + "/admin/nodes/17/drain", status: http.StatusUnauthorized, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A worker retirement observation requires acting administrator authority."},
	}
}

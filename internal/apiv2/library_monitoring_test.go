package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/librarymonitor"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeLibraryMonitoring struct {
	snap librarymonitor.StatusSnapshot
	err  error
}

func (f *fakeLibraryMonitoring) RealtimeMonitoringStatus(context.Context) (librarymonitor.StatusSnapshot, error) {
	return f.snap, f.err
}

type monitoringEntry struct {
	LibraryID   string  `json:"library_id"`
	Enabled     bool    `json:"enabled"`
	State       string  `json:"state"`
	Backend     string  `json:"backend"`
	Detail      string  `json:"detail"`
	Directories int     `json:"directories"`
	NodeID      *string `json:"node_id"`
	UpdatedAt   *string `json:"updated_at"`
}

type monitoringBody struct {
	ServerEnabled bool              `json:"server_enabled"`
	Libraries     []monitoringEntry `json:"libraries"`
}

const monitoringPath = Prefix + "/libraries/realtime-monitoring"

func monitoredFolder(id, sortOrder int) *models.MediaFolder {
	return &models.MediaFolder{ID: id, Name: "lib", Enabled: true, RealtimeMonitoring: true, SortOrder: sortOrder}
}

// libraryMonitoringFixtureCases run against fixtureDeps' monitoring fake.
func libraryMonitoringFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "get_library_realtime_monitoring_ok", operationID: "getLibraryRealtimeMonitoring",
			scenario: "One library monitored by a node, one with no fresh node report.",
			method:   http.MethodGet, path: monitoringPath, headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryRealtimeMonitoring"},
		{name: "get_library_capabilities_ok", operationID: "getLibraryCapabilities",
			scenario: "Administrator discovery of the library features this build supports.",
			method:   http.MethodGet, path: Prefix + "/libraries/capabilities", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryCapabilities"},
	}
}

package handlers

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/contracts/complexv22"
)

// SystemCapabilitiesHandler reports the Silo Complex API features whose
// production guarantees are implemented by this server.
type SystemCapabilitiesHandler struct{}

type systemCapabilitiesResponse = complexv22.SystemCapabilitiesResponse

// NewSystemCapabilitiesHandler constructs a capability discovery handler.
func NewSystemCapabilitiesHandler() *SystemCapabilitiesHandler {
	return &SystemCapabilitiesHandler{}
}

// HandleGet handles GET /api/v1/system/capabilities.
func (*SystemCapabilitiesHandler) HandleGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, systemCapabilitiesResponse{
		APIVersion:   "2.2",
		Capabilities: complexv22.Capabilities,
	})
}

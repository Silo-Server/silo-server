package handlers

import "net/http"

// SystemCapabilitiesHandler reports the Silo Complex API features whose
// production guarantees are implemented by this server.
type SystemCapabilitiesHandler struct{}

// NewSystemCapabilitiesHandler constructs a capability discovery handler.
func NewSystemCapabilitiesHandler() *SystemCapabilitiesHandler {
	return &SystemCapabilitiesHandler{}
}

type systemCapabilitiesResponse struct {
	APIVersion   string   `json:"api_version"`
	Capabilities []string `json:"capabilities"`
}

// HandleGet handles GET /api/v1/system/capabilities.
func (*SystemCapabilitiesHandler) HandleGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, systemCapabilitiesResponse{
		APIVersion:   "2.2",
		Capabilities: []string{"branding.v1"},
	})
}

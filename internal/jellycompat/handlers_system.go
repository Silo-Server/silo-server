package jellycompat

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/config"
)

type publicSystemInfoResponse struct {
	LocalAddress           string `json:"LocalAddress"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	ID                     string `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

type brandingConfigurationResponse struct {
	LoginDisclaimer string `json:"LoginDisclaimer"`
}

type endpointInfoResponse struct {
	IsLocal     bool   `json:"IsLocal"`
	IsInNetwork bool   `json:"IsInNetwork"`
	Address     string `json:"Address,omitempty"`
}

// SystemHandler serves Jellyfin setup/system endpoints.
type SystemHandler struct {
	cfg func() *config.Config
}

// NewSystemHandler creates a new system handler. The config provider is
// invoked per request so public URL / server name / emulated version
// changes apply without restart.
func NewSystemHandler(cfg func() *config.Config) *SystemHandler {
	return &SystemHandler{cfg: cfg}
}

// HandlePublicInfo serves GET /System/Info/Public.
func (h *SystemHandler) HandlePublicInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.systemInfo())
}

// HandleInfo serves GET /System/Info.
func (h *SystemHandler) HandleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.systemInfo())
}

// HandleBrandingConfiguration serves GET /Branding/Configuration.
func (h *SystemHandler) HandleBrandingConfiguration(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, brandingConfigurationResponse{
		LoginDisclaimer: "Silo provides Jellyfin-compatible app support. Silo is not affiliated with or endorsed by the Jellyfin project.",
	})
}

// HandleQuickConnectEnabled serves GET /QuickConnect/Enabled.
func (h *SystemHandler) HandleQuickConnectEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, false)
}

// HandleEndpoint serves GET /System/Endpoint.
func (h *SystemHandler) HandleEndpoint(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, endpointInfoResponse{
		IsLocal:     true,
		IsInNetwork: true,
		Address:     h.cfg().JellyfinCompat.PublicURL,
	})
}

// HandlePing serves GET /System/Ping.
func (h *SystemHandler) HandlePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *SystemHandler) systemInfo() publicSystemInfoResponse {
	return publicSystemInfoResponse{
		LocalAddress:           h.cfg().JellyfinCompat.PublicURL,
		ServerName:             h.cfg().JellyfinCompat.ServerName,
		Version:                h.cfg().JellyfinCompat.EmulatedServerVersion,
		ProductName:            "Jellyfin Server",
		ID:                     h.cfg().JellyfinCompat.ServerID,
		StartupWizardCompleted: true,
	}
}

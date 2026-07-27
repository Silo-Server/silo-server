package handlers

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

// CompatConnectInfoHandler serves the account-facing view of the compatibility
// listeners: where to point a third-party client, and whether it is even on.
//
// The admin status endpoint covers the same ground for operators, but it also
// reports install paths and version provenance, so it stays admin-only. This
// handler exposes only the address a client would learn by connecting anyway.
type CompatConnectInfoHandler struct {
	Config       *config.Config
	SettingsRepo ServerSettingsStore
}

func NewCompatConnectInfoHandler(cfg *config.Config, settings ServerSettingsStore) *CompatConnectInfoHandler {
	return &CompatConnectInfoHandler{Config: cfg, SettingsRepo: settings}
}

type compatConnectInfoResponse struct {
	Jellyfin jellycompat.ConnectInfo `json:"jellyfin"`
}

// HandleGetConnectInfo handles GET /compat/connect-info.
func (h *CompatConnectInfoHandler) HandleGetConnectInfo(w http.ResponseWriter, r *http.Request) {
	// A missing settings store is not fatal here: the bootstrap config alone
	// still describes a usable listener, so fall back to it rather than
	// failing a purely informational read.
	var settings map[string]string
	if h.SettingsRepo != nil {
		loaded, err := h.SettingsRepo.GetAll(r.Context())
		if err == nil {
			settings = loaded
		}
	}

	writeJSON(w, http.StatusOK, compatConnectInfoResponse{
		Jellyfin: jellycompat.ConnectInfoForConfig(h.Config, settings),
	})
}

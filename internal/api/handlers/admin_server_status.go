package handlers

import (
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

type adminServerStatusResponse struct {
	StartedAt             time.Time  `json:"started_at"`
	RestartRequired       bool       `json:"restart_required"`
	RestartRequiredAt     *time.Time `json:"restart_required_at,omitempty"`
	RestartRequiredReason string     `json:"restart_required_reason,omitempty"`
	RestartRequested      bool       `json:"restart_requested"`
	RestartRequestedAt    *time.Time `json:"restart_requested_at,omitempty"`
}

// HandleGetServerStatus handles GET /admin/server/status.
func (h *AdminHandler) HandleGetServerStatus(w http.ResponseWriter, r *http.Request) {
	snapshot := h.RestartStatus.Snapshot()
	resp := adminServerStatusResponse{
		StartedAt:             snapshot.StartedAt,
		RestartRequired:       snapshot.RestartRequired,
		RestartRequiredAt:     snapshot.RestartRequiredAt,
		RestartRequiredReason: snapshot.RestartRequiredReason,
		RestartRequested:      snapshot.RestartRequested,
		RestartRequestedAt:    snapshot.RestartRequestedAt,
	}

	if h.SettingsRepo != nil {
		settings, err := h.SettingsRepo.GetAll(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load settings")
			return
		}
		if jellycompat.WebComponentStatusForConfig(h.Config, settings).RestartRequired {
			resp.RestartRequired = true
			if resp.RestartRequiredReason == "" {
				resp.RestartRequiredReason = "jellyfin_compat"
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *AdminHandler) markServerRestartRequired(reason string) {
	if h == nil {
		return
	}
	h.RestartStatus.MarkRequired(reason)
}

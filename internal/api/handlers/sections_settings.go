package handlers

import (
	"encoding/json"
	"net/http"
)

// SectionSettingsHandler serves the frozen /api/v1 routes of the retired
// "let profiles add rule rows" setting. Profiles may always add rule rows
// now, so every read reports true and a write changes nothing. A stored
// sections.allow_profile_custom_sections row is left in place and ignored.
type SectionSettingsHandler struct{}

type sectionsSettingResponse struct {
	AllowProfileCustomSections bool `json:"allow_profile_custom_sections"`
}

var alwaysAllowed = sectionsSettingResponse{AllowProfileCustomSections: true}

// HandleGet handles GET /api/v1/admin/settings/sections.
func (h *SectionSettingsHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, alwaysAllowed)
}

// HandleGetProfileFlag handles GET /api/v1/profile/sections/flags.
func (h *SectionSettingsHandler) HandleGetProfileFlag(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, alwaysAllowed)
}

// HandlePut handles PUT /api/v1/admin/settings/sections. The body is still
// checked as JSON, then ignored: the answer is the value in effect.
func (h *SectionSettingsHandler) HandlePut(w http.ResponseWriter, r *http.Request) {
	var req sectionsSettingResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, alwaysAllowed)
}

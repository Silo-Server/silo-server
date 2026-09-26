package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// SectionsAllowProfileCustomSettingKey is the server_settings key controlling
// whether non-admin profiles may add user-added sections of admin-only recipes.
const SectionsAllowProfileCustomSettingKey = "sections.allow_profile_custom_sections"

// SectionSettingsHandler exposes GET/PUT for the sections-related server setting.
type SectionSettingsHandler struct {
	Settings catalog.SettingsStore
}

type sectionsSettingResponse struct {
	AllowProfileCustomSections bool `json:"allow_profile_custom_sections"`
}

// HandleGet handles GET /api/admin/settings/sections.
func (h *SectionSettingsHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	var resp sectionsSettingResponse
	if h.Settings != nil {
		v, _ := h.Settings.Get(r.Context(), SectionsAllowProfileCustomSettingKey)
		resp.AllowProfileCustomSections = v == "true"
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleGetProfileFlag exposes the allow_profile_custom_sections setting to any
// authenticated profile (no admin required). Read-only.
func (h *SectionSettingsHandler) HandleGetProfileFlag(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sectionsSettingResponse{AllowProfileCustomSections: h.AllowProfileCustomSections(r.Context())})
}

// AllowProfileCustomSections reads the allow_profile_custom_sections setting;
// false when no settings store is wired. v1 GET /profile/sections/flags and
// v2 getProfileSectionFlags both call it.
func (h *SectionSettingsHandler) AllowProfileCustomSections(ctx context.Context) bool {
	if h.Settings == nil {
		return false
	}
	v, _ := h.Settings.Get(ctx, SectionsAllowProfileCustomSettingKey)
	return v == "true"
}

// HandlePut handles PUT /api/admin/settings/sections.
func (h *SectionSettingsHandler) HandlePut(w http.ResponseWriter, r *http.Request) {
	var req sectionsSettingResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if h.Settings != nil {
		value := "false"
		if req.AllowProfileCustomSections {
			value = "true"
		}
		if err := h.Settings.Set(r.Context(), SectionsAllowProfileCustomSettingKey, value); err != nil {
			writeError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, req)
}

// UpdateAdminSectionSettings evaluates a guard against the stored flag while
// holding the existing settings transaction. The profile read remains unchanged.
func (h *SectionSettingsHandler) UpdateAdminSectionSettings(ctx context.Context, enabled bool, guard func(bool) error) error {
	updater, ok := h.Settings.(serverSettingsAtomicUpdater)
	if !ok {
		return &APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Atomic settings store not configured"}
	}
	return updater.UpdateAtomic(ctx, func(current map[string]string) (map[string]string, error) {
		before := current[SectionsAllowProfileCustomSettingKey] == "true"
		if err := guard(before); err != nil {
			return nil, err
		}
		if before == enabled {
			return nil, nil
		}
		value := "false"
		if enabled {
			value = "true"
		}
		return map[string]string{SectionsAllowProfileCustomSettingKey: value}, nil
	})
}

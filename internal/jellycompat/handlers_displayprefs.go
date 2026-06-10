package jellycompat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// displayPreferencesDTO mirrors Jellyfin's DisplayPreferences response.
type displayPreferencesDTO struct {
	ID                 string            `json:"Id"`
	SortBy             string            `json:"SortBy"`
	SortOrder          string            `json:"SortOrder"`
	RememberIndexing   bool              `json:"RememberIndexing"`
	RememberSorting    bool              `json:"RememberSorting"`
	ScrollDirection    string            `json:"ScrollDirection"`
	ShowBackdrop       bool              `json:"ShowBackdrop"`
	ShowSidebar        bool              `json:"ShowSidebar"`
	PrimaryImageHeight int               `json:"PrimaryImageHeight"`
	PrimaryImageWidth  int               `json:"PrimaryImageWidth"`
	Client             string            `json:"Client"`
	CustomPrefs        map[string]string `json:"CustomPrefs"`
}

// DisplayPreferencesHandler serves Jellyfin display preferences endpoints,
// persisting them via the user settings key-value store and seeding defaults
// from the user's profile.
type DisplayPreferencesHandler struct {
	storeProvider userstore.UserStoreProvider
}

// NewDisplayPreferencesHandler creates a new display preferences handler.
func NewDisplayPreferencesHandler(storeProvider userstore.UserStoreProvider) *DisplayPreferencesHandler {
	return &DisplayPreferencesHandler{storeProvider: storeProvider}
}

func displayPrefsSettingKey(id, client string) string {
	return fmt.Sprintf("jellycompat:displayprefs:%s:%s", id, client)
}

// HandleGetDisplayPreferences serves GET /DisplayPreferences/{displayPreferencesId}.
func (h *DisplayPreferencesHandler) HandleGetDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	id := chi.URLParam(r, "displayPreferencesId")
	client := r.URL.Query().Get("client")

	// Try to load persisted preferences from user settings.
	if h.storeProvider != nil {
		store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
		if err == nil {
			val, err := store.GetSetting(r.Context(), displayPrefsSettingKey(id, client))
			if err == nil && val != "" {
				var dto displayPreferencesDTO
				if json.Unmarshal([]byte(val), &dto) == nil {
					writeJSON(w, http.StatusOK, dto)
					return
				}
			}
		}
	}

	// No persisted prefs — build defaults, seeding from profile settings.
	dto := defaultDisplayPreferences(id, client)
	if h.storeProvider != nil {
		h.seedFromProfile(r, session, &dto)
	}
	writeJSON(w, http.StatusOK, dto)
}

// HandleUpdateDisplayPreferences serves POST /DisplayPreferences/{displayPreferencesId}.
func (h *DisplayPreferencesHandler) HandleUpdateDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	id := chi.URLParam(r, "displayPreferencesId")
	client := r.URL.Query().Get("client")

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Failed to read request body")
		return
	}

	// Validate it's valid JSON and normalize.
	var dto displayPreferencesDTO
	if json.Unmarshal(body, &dto) != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid JSON")
		return
	}
	dto.ID = id
	dto.Client = client

	if h.storeProvider != nil {
		store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
		if err == nil {
			encoded, _ := json.Marshal(dto)
			_ = store.SetSetting(r.Context(), displayPrefsSettingKey(id, client), string(encoded))
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func defaultDisplayPreferences(id, client string) displayPreferencesDTO {
	return displayPreferencesDTO{
		ID:              id,
		SortBy:          "SortName",
		SortOrder:       "Ascending",
		ScrollDirection: "Horizontal",
		ShowBackdrop:    true,
		Client:          client,
		CustomPrefs:     map[string]string{},
	}
}

func (h *DisplayPreferencesHandler) seedFromProfile(r *http.Request, session *Session, dto *displayPreferencesDTO) {
	store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
	if err != nil {
		return
	}
	profile, err := store.GetProfile(r.Context(), session.ProfileID)
	if err != nil || profile == nil {
		return
	}
	if profile.SubtitleLanguage != "" {
		dto.CustomPrefs["subtitleLanguage"] = profile.SubtitleLanguage
	}
	if profile.SubtitleMode != "" {
		dto.CustomPrefs["subtitleMode"] = profile.SubtitleMode
	}
	dto.CustomPrefs["enableNextVideoInfoOverlay"] = strconv.FormatBool(!profile.AutoSkipCredits)
}

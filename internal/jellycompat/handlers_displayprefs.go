package jellycompat

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
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
// persisting the blobs verbatim in the dedicated jellycompat_displayprefs
// table and seeding defaults from the user's profile.
type DisplayPreferencesHandler struct {
	storeProvider userstore.UserStoreProvider
}

// NewDisplayPreferencesHandler creates a new display preferences handler.
func NewDisplayPreferencesHandler(storeProvider userstore.UserStoreProvider) *DisplayPreferencesHandler {
	return &DisplayPreferencesHandler{storeProvider: storeProvider}
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

	// Try to load persisted preferences.
	if h.storeProvider != nil {
		store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
		if err == nil {
			val, err := store.GetJellycompatDisplayPrefs(r.Context(), id, client)
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
			_ = store.SetJellycompatDisplayPrefs(r.Context(), id, client, string(encoded))
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

// seedFromProfile fills a fresh DisplayPreferences document from the user's
// real settings, so a Jellyfin client's first read reflects choices made in
// Silo rather than empty defaults.
//
// Resolved at profile scope with no device: this seeds what a Jellyfin client
// sees, and those clients do not carry Silo's device identity. A device
// override leaking in here would hand one device's settings to every Jellyfin
// client on the account.
func (h *DisplayPreferencesHandler) seedFromProfile(r *http.Request, session *Session, dto *displayPreferencesDTO) {
	store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
	if err != nil {
		return
	}

	contract, err := settingscontract.Load()
	if err != nil {
		return
	}
	resolved, err := settingsresolve.New(contract).Resolve(r.Context(), store,
		settingsresolve.Context{ProfileID: session.ProfileID},
		[]string{
			settingskeys.PlaybackSubtitleLanguage,
			settingskeys.PlaybackSubtitleMode,
			settingskeys.PlaybackAutoSkipCredits,
		}, nil)
	if err != nil {
		return
	}

	for _, eff := range resolved {
		switch eff.Key {
		case settingskeys.PlaybackSubtitleLanguage:
			var language string
			if json.Unmarshal(eff.Value, &language) == nil && language != "" {
				dto.CustomPrefs["subtitleLanguage"] = language
			}
		case settingskeys.PlaybackSubtitleMode:
			var mode string
			if json.Unmarshal(eff.Value, &mode) == nil && mode != "" {
				dto.CustomPrefs["subtitleMode"] = mode
			}
		case settingskeys.PlaybackAutoSkipCredits:
			// Jellyfin spells this as the inverse: the overlay is what plays
			// instead of skipping.
			var skip bool
			if json.Unmarshal(eff.Value, &skip) == nil {
				dto.CustomPrefs["enableNextVideoInfoOverlay"] = strconv.FormatBool(!skip)
			}
		}
	}
}

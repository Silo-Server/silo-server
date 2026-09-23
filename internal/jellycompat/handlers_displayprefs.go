package jellycompat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// displayPreferencesDTO mirrors Jellyfin's DisplayPreferences response.
type displayPreferencesDTO struct {
	ID                 string            `json:"Id"`
	ViewType           string            `json:"ViewType"`
	IndexBy            string            `json:"IndexBy"`
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

	if !validateOptionalUser(w, r, session) {
		return
	}
	if h.storeProvider == nil {
		writeCompatUpstreamError(w, fmt.Errorf("user store unavailable"))
		return
	}
	store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	val, err := store.GetJellycompatDisplayPrefs(r.Context(), profilePreferencesID(session.ProfileID, id), client)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if val == "" {
		profile, err := store.GetProfile(r.Context(), session.ProfileID)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		// Older servers stored one document for the account. Keep that
		// customization available to its primary profile after scoping reads.
		if profile != nil && profile.IsPrimary {
			val, err = store.GetJellycompatDisplayPrefs(r.Context(), id, client)
			if err != nil {
				writeCompatUpstreamError(w, err)
				return
			}
		}
	}
	dto := defaultDisplayPreferences(id, client)
	if val != "" {
		if err := json.Unmarshal([]byte(val), &dto); err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
	} else if err := h.seedFromProfile(r, session, &dto); err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if dto.CustomPrefs == nil {
		dto.CustomPrefs = map[string]string{}
	}
	fillReadCustomPrefs(dto.CustomPrefs)
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

	if !validateOptionalUser(w, r, session) {
		return
	}
	var dto displayPreferencesDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&dto); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid JSON")
		return
	}
	dto.ID, dto.Client = id, client
	if dto.CustomPrefs == nil {
		dto.CustomPrefs = map[string]string{}
	}
	normalizeSavedCustomPrefs(dto.CustomPrefs)
	if h.storeProvider == nil {
		writeCompatUpstreamError(w, fmt.Errorf("user store unavailable"))
		return
	}
	store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if err := store.SetJellycompatDisplayPrefs(r.Context(), profilePreferencesID(session.ProfileID, id), client, string(encoded)); err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Jellyfin client skip-interval preferences. Jellyfin always returns both on
// read, defaulting to 10 s back and 30 s forward; a write that omits either
// stores 15 s (Jellyfin 12).
const (
	customPrefSkipBackLength     = "skipBackLength"
	customPrefSkipForwardLength  = "skipForwardLength"
	jellyfinDefaultSkipBackMS    = "10000"
	jellyfinDefaultSkipForwardMS = "30000"
	jellyfinSavedSkipLengthMS    = "15000"
)

// fillReadCustomPrefs reports the skip intervals the way Jellyfin's read does,
// so clients that post the whole document back keep 10 s/30 s instead of
// triggering the save-time 15 s fallback.
func fillReadCustomPrefs(prefs map[string]string) {
	if strings.TrimSpace(prefs[customPrefSkipBackLength]) == "" {
		prefs[customPrefSkipBackLength] = jellyfinDefaultSkipBackMS
	}
	if strings.TrimSpace(prefs[customPrefSkipForwardLength]) == "" {
		prefs[customPrefSkipForwardLength] = jellyfinDefaultSkipForwardMS
	}
}

// normalizeSavedCustomPrefs applies Jellyfin 12's save-time rules: a missing
// or empty skip length is stored as 15 seconds, and an empty landing-* view
// choice is dropped rather than kept as an invalid value.
func normalizeSavedCustomPrefs(prefs map[string]string) {
	for _, key := range []string{customPrefSkipBackLength, customPrefSkipForwardLength} {
		if strings.TrimSpace(prefs[key]) == "" {
			prefs[key] = jellyfinSavedSkipLengthMS
		}
	}
	for key, value := range prefs {
		if strings.HasPrefix(strings.ToLower(key), "landing-") && strings.TrimSpace(value) == "" {
			delete(prefs, key)
		}
	}
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
func (h *DisplayPreferencesHandler) seedFromProfile(r *http.Request, session *Session, dto *displayPreferencesDTO) error {
	store, err := h.storeProvider.ForUser(r.Context(), session.StreamAppUserID)
	if err != nil {
		return err
	}

	contract, err := settingscontract.Load()
	if err != nil {
		return err
	}
	resolved, err := settingsresolve.New(contract).Resolve(r.Context(), store,
		settingsresolve.Context{ProfileID: session.ProfileID},
		[]string{
			settingskeys.PlaybackSubtitleLanguage,
			settingskeys.PlaybackSubtitleMode,
			settingskeys.PlaybackAutoSkipCredits,
		}, nil)
	if err != nil {
		return err
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
	return nil
}

func profilePreferencesID(profileID, id string) string {
	return fmt.Sprintf("profile:%d:%s:%s", len(profileID), profileID, id)
}

func validateOptionalUser(w http.ResponseWriter, r *http.Request, session *Session) bool {
	if id := newCaseInsensitiveQuery(r.URL.Query()).Get("userId"); id != "" {
		return validatePseudoUser(w, id, session)
	}
	return true
}

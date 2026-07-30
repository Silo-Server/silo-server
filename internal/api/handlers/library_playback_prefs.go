package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type libraryLookup interface {
	GetByID(context.Context, int) (*models.MediaFolder, error)
}

// LibraryPlaybackPrefHandler handles per-library playback preference endpoints.
type LibraryPlaybackPrefHandler struct {
	storeProvider userstore.UserStoreProvider
	libraryLookup libraryLookup
	EventsHub     *evt.Hub
}

// NewLibraryPlaybackPrefHandler creates a new LibraryPlaybackPrefHandler.
func NewLibraryPlaybackPrefHandler(provider userstore.UserStoreProvider) *LibraryPlaybackPrefHandler {
	return &LibraryPlaybackPrefHandler{storeProvider: provider}
}

// SetLibraryLookup wires in an optional library lookup used to reject
// nonexistent library IDs before mutating playback preferences.
func (h *LibraryPlaybackPrefHandler) SetLibraryLookup(lookup libraryLookup) {
	h.libraryLookup = lookup
}

type setLibraryPlaybackPrefRequest struct {
	AudioLanguage       *string `json:"audio_language"`
	SubtitleLanguage    *string `json:"subtitle_language"`
	SubtitleMode        *string `json:"subtitle_mode"`
	ShowForcedSubtitles *bool   `json:"show_forced_subtitles"`
}

type libraryPlaybackPrefResponse struct {
	ProfileID           string  `json:"profile_id"`
	LibraryID           int     `json:"library_id"`
	AudioLanguage       *string `json:"audio_language,omitempty"`
	SubtitleLanguage    *string `json:"subtitle_language,omitempty"`
	SubtitleMode        *string `json:"subtitle_mode,omitempty"`
	ShowForcedSubtitles *bool   `json:"show_forced_subtitles,omitempty"`
	UpdatedAt           string  `json:"updated_at,omitempty"`
}

type libraryPlaybackPrefsListResponse struct {
	Preferences []libraryPlaybackPrefResponse `json:"preferences"`
}

// HandleListLibraryPlaybackPrefs handles GET /library-playback-prefs.
func (h *LibraryPlaybackPrefHandler) HandleListLibraryPlaybackPrefs(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	prefs, err := store.ListLibraryPlaybackPreferences(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list library playback preferences")
		return
	}

	resp := libraryPlaybackPrefsListResponse{
		Preferences: make([]libraryPlaybackPrefResponse, 0, len(prefs)),
	}
	for _, pref := range prefs {
		resp.Preferences = append(resp.Preferences, toLibraryPlaybackPrefResponse(pref))
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleSetLibraryPlaybackPref handles PUT /library-playback-prefs/{library_id}.
func (h *LibraryPlaybackPrefHandler) HandleSetLibraryPlaybackPref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	libraryID, ok := parseLibraryID(w, r)
	if !ok {
		return
	}
	if !h.ensureLibraryExists(w, r, libraryID) {
		return
	}

	var req setLibraryPlaybackPrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if !isValidLibrarySubtitleMode(req.SubtitleMode) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid subtitle_mode")
		return
	}
	sync, err := planLibraryPlaybackSettingsSync(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	pref := userstore.LibraryPlaybackPreference{
		ProfileID:              profileID,
		LibraryID:              libraryID,
		HasAudioLanguage:       req.AudioLanguage != nil,
		HasSubtitleLanguage:    req.SubtitleLanguage != nil,
		HasSubtitleMode:        req.SubtitleMode != nil,
		HasShowForcedSubtitles: req.ShowForcedSubtitles != nil,
	}
	if req.AudioLanguage != nil {
		pref.AudioLanguage = *req.AudioLanguage
	}
	if req.SubtitleLanguage != nil {
		pref.SubtitleLanguage = *req.SubtitleLanguage
	}
	if req.SubtitleMode != nil {
		pref.SubtitleMode = *req.SubtitleMode
	}
	if req.ShowForcedSubtitles != nil {
		pref.ShowForcedSubtitles = *req.ShowForcedSubtitles
	}

	if !pref.HasAudioLanguage && !pref.HasSubtitleLanguage && !pref.HasSubtitleMode && !pref.HasShowForcedSubtitles {
		if err := h.applyLibraryPlaybackSettingsSync(r.Context(), store, userID,
			profileID, libraryID, sync, func(tx userstore.PreferenceSettingsWriter) error {
				return tx.DeleteLibraryPlaybackPreference(r.Context(), profileID, libraryID)
			}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete library playback preference")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.applyLibraryPlaybackSettingsSync(r.Context(), store, userID,
		profileID, libraryID, sync, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.UpsertLibraryPlaybackPreference(r.Context(), pref)
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store library playback preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteLibraryPlaybackPref handles DELETE /library-playback-prefs/{library_id}.
func (h *LibraryPlaybackPrefHandler) HandleDeleteLibraryPlaybackPref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	libraryID, ok := parseLibraryID(w, r)
	if !ok {
		return
	}
	if !h.ensureLibraryExists(w, r, libraryID) {
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := h.applyLibraryPlaybackSettingsSync(r.Context(), store, userID,
		profileID, libraryID, []profileSettingSync{
			{key: settingskeys.PlaybackAudioLanguage},
			{key: settingskeys.PlaybackSubtitleLanguage},
			{key: settingskeys.PlaybackSubtitleMode},
			{key: settingskeys.PlaybackShowForcedSubtitles},
		}, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.DeleteLibraryPlaybackPreference(r.Context(), profileID, libraryID)
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete library playback preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func planLibraryPlaybackSettingsSync(req setLibraryPlaybackPrefRequest) ([]profileSettingSync, error) {
	out := make([]profileSettingSync, 0, 4)
	for _, field := range []struct {
		key string
		raw *string
	}{
		{settingskeys.PlaybackAudioLanguage, req.AudioLanguage},
		{settingskeys.PlaybackSubtitleLanguage, req.SubtitleLanguage},
		{settingskeys.PlaybackSubtitleMode, req.SubtitleMode},
	} {
		if field.raw == nil {
			out = append(out, profileSettingSync{key: field.key})
			continue
		}
		var err error
		out, err = appendStringSync(out, field.key, field.raw)
		if err != nil {
			return nil, err
		}
	}
	forced := profileSettingSync{key: settingskeys.PlaybackShowForcedSubtitles}
	if req.ShowForcedSubtitles != nil {
		forced.value = json.RawMessage(strconv.FormatBool(*req.ShowForcedSubtitles))
	}
	return append(out, forced), nil
}

func (h *LibraryPlaybackPrefHandler) applyLibraryPlaybackSettingsSync(
	ctx context.Context,
	store userstore.UserStore,
	userID int,
	profileID string,
	libraryID int,
	writes []profileSettingSync,
	legacyMutation func(userstore.PreferenceSettingsWriter) error,
) error {
	return applyLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID, userstore.SettingIdentity{
		Scope: settingscontract.ScopeProfileLibrary, ProfileID: profileID, LibraryID: libraryID,
	}, writes, legacyMutation)
}

func parseLibraryID(w http.ResponseWriter, r *http.Request) (int, bool) {
	libraryIDStr := chi.URLParam(r, "library_id")
	if libraryIDStr == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Library ID is required")
		return 0, false
	}

	libraryID, err := strconv.Atoi(libraryIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Library ID must be a valid integer")
		return 0, false
	}
	if libraryID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Library ID must be a positive integer")
		return 0, false
	}
	return libraryID, true
}

func (h *LibraryPlaybackPrefHandler) ensureLibraryExists(w http.ResponseWriter, r *http.Request, libraryID int) bool {
	if h.libraryLookup == nil {
		return true
	}

	if _, err := h.libraryLookup.GetByID(r.Context(), libraryID); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to look up library")
		return false
	}

	return true
}

func isValidLibrarySubtitleMode(mode *string) bool {
	if mode == nil {
		return true
	}
	switch *mode {
	case "", "auto", "always", "off":
		return true
	default:
		return false
	}
}

func toLibraryPlaybackPrefResponse(p userstore.LibraryPlaybackPreference) libraryPlaybackPrefResponse {
	resp := libraryPlaybackPrefResponse{
		ProfileID: p.ProfileID,
		LibraryID: p.LibraryID,
		UpdatedAt: p.UpdatedAt,
	}
	if p.HasAudioLanguage {
		resp.AudioLanguage = stringPtr(p.AudioLanguage)
	}
	if p.HasSubtitleLanguage {
		resp.SubtitleLanguage = stringPtr(p.SubtitleLanguage)
	}
	if p.HasSubtitleMode {
		resp.SubtitleMode = stringPtr(p.SubtitleMode)
	}
	if p.HasShowForcedSubtitles {
		resp.ShowForcedSubtitles = boolPtr(p.ShowForcedSubtitles)
	}
	return resp
}

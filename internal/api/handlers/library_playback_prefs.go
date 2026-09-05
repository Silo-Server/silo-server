package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

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

	prefs, err := h.ListLibraryPlaybackPreferences(r.Context(), userID, profileID)
	if err != nil {
		writeAPIError(w, err)
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

// ListLibraryPlaybackPreferences is the listing v1 GET
// /library-playback-prefs and v2 listLibraryPlaybackPreferences share. A
// failure is an *APIError.
func (h *LibraryPlaybackPrefHandler) ListLibraryPlaybackPreferences(ctx context.Context, userID int, profileID string) ([]userstore.LibraryPlaybackPreference, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	prefs, err := store.ListLibraryPlaybackPreferences(ctx, profileID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list library playback preferences")
	}
	return prefs, nil
}

// GetLibraryPlaybackPreference is the single-library read v2
// updateLibraryPlaybackPreference merges its partial body onto. It answers
// nil when the profile has no row for the library, which is not an error: an
// unknown library surfaces from the write that follows. A failure is an
// *APIError.
func (h *LibraryPlaybackPrefHandler) GetLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int) (*userstore.LibraryPlaybackPreference, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	pref, err := store.GetLibraryPlaybackPreference(ctx, profileID, libraryID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get library playback preference")
	}
	return pref, nil
}

// LibraryPlaybackPrefUpdate is the write v1 PUT and v2
// updateLibraryPlaybackPreference hand SetLibraryPlaybackPreference: each
// member nil means "no preference" for that trait and clears it.
type LibraryPlaybackPrefUpdate struct {
	AudioLanguage       *string
	SubtitleLanguage    *string
	SubtitleMode        *string
	ShowForcedSubtitles *bool
}

// SetLibraryPlaybackPreference is the write v1 PUT
// /library-playback-prefs/{library_id} and v2 updateLibraryPlaybackPreference
// share: the legacy row and the canonical profile_library rows commit
// together, and a user_settings.changed event follows each canonical row that
// moved. A request with every member nil removes the row. A failure is an
// *APIError: an unknown library is 404 not_found, a subtitle_mode outside
// auto/always/off or a value the canonical contract refuses is a 400
// bad_request naming the member.
func (h *LibraryPlaybackPrefHandler) SetLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int, req LibraryPlaybackPrefUpdate) error {
	if err := h.libraryExists(ctx, libraryID); err != nil {
		return err
	}
	return h.setLibraryPlaybackPreference(ctx, userID, profileID, libraryID, req)
}

// setLibraryPlaybackPreference is SetLibraryPlaybackPreference after the
// library check; the v1 handler runs that check before it reads the body.
func (h *LibraryPlaybackPrefHandler) setLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int, req LibraryPlaybackPrefUpdate) error {
	if !isValidLibrarySubtitleMode(req.SubtitleMode) {
		return fieldError("subtitle_mode", "Invalid subtitle_mode")
	}
	sync, err := planLibraryPlaybackSettingsSync(setLibraryPlaybackPrefRequest(req))
	if err != nil {
		return fieldError(libraryPlaybackPrefField(err), err.Error())
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
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
		if err := h.applyLibraryPlaybackSettingsSync(ctx, store, userID,
			profileID, libraryID, sync, func(tx userstore.PreferenceSettingsWriter) error {
				return tx.DeleteLibraryPlaybackPreference(ctx, profileID, libraryID)
			}); err != nil {
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete library playback preference")
		}
		return nil
	}

	if err := h.applyLibraryPlaybackSettingsSync(ctx, store, userID,
		profileID, libraryID, sync, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.UpsertLibraryPlaybackPreference(ctx, pref)
		}); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to store library playback preference")
	}
	return nil
}

// DeleteLibraryPlaybackPreference is the delete v1 DELETE
// /library-playback-prefs/{library_id} and v2 deleteLibraryPlaybackPreference
// share. Deleting a preference that does not exist succeeds; an unknown
// library is 404 not_found. A failure is an *APIError.
func (h *LibraryPlaybackPrefHandler) DeleteLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int) error {
	if err := h.libraryExists(ctx, libraryID); err != nil {
		return err
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.applyLibraryPlaybackSettingsSync(ctx, store, userID,
		profileID, libraryID, []profileSettingSync{
			{key: settingskeys.PlaybackAudioLanguage},
			{key: settingskeys.PlaybackSubtitleLanguage},
			{key: settingskeys.PlaybackSubtitleMode},
			{key: settingskeys.PlaybackShowForcedSubtitles},
		}, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.DeleteLibraryPlaybackPreference(ctx, profileID, libraryID)
		}); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete library playback preference")
	}
	return nil
}

// HandleSetLibraryPlaybackPref handles PUT /library-playback-prefs/{library_id}.
func (h *LibraryPlaybackPrefHandler) HandleSetLibraryPlaybackPref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	libraryID, ok := parseLibraryID(w, r)
	if !ok {
		return
	}
	if err := h.libraryExists(r.Context(), libraryID); err != nil {
		writeAPIError(w, err)
		return
	}

	var req setLibraryPlaybackPrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.setLibraryPlaybackPreference(r.Context(), userID, profileID, libraryID, LibraryPlaybackPrefUpdate(req)); err != nil {
		writeAPIError(w, err)
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
	if err := h.DeleteLibraryPlaybackPreference(r.Context(), userID, profileID, libraryID); err != nil {
		writeAPIError(w, err)
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

// libraryPlaybackPrefField names the request member a canonical-contract
// rejection is about, from the "<setting key>: <reason>" message the sync
// planner returns; the v1 body is unchanged by it, the v2 listener renders it
// as a 422 naming the member. Empty when the key is not one of the four.
func libraryPlaybackPrefField(err error) string {
	for key, field := range map[string]string{
		settingskeys.PlaybackAudioLanguage:       "audio_language",
		settingskeys.PlaybackSubtitleLanguage:    "subtitle_language",
		settingskeys.PlaybackSubtitleMode:        "subtitle_mode",
		settingskeys.PlaybackShowForcedSubtitles: "show_forced_subtitles",
	} {
		if strings.HasPrefix(err.Error(), key+":") {
			return field
		}
	}
	return ""
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

// libraryExists refuses a library id no library carries; without a lookup
// every id passes. A failure is an *APIError.
func (h *LibraryPlaybackPrefHandler) libraryExists(ctx context.Context, libraryID int) error {
	if h.libraryLookup == nil {
		return nil
	}

	if _, err := h.libraryLookup.GetByID(ctx, libraryID); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to look up library")
	}

	return nil
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

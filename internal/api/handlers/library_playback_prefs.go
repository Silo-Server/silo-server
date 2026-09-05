package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

// ListLibraryPlaybackPreferences is the listing behind v1 GET
// /library-playback-prefs: the legacy user_library_playback_preferences rows
// as stored. A failure is an *APIError.
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

// libraryPlaybackSettingKeys are the four canonical profile_library keys the
// library playback preference is made of.
var libraryPlaybackSettingKeys = []string{
	settingskeys.PlaybackAudioLanguage,
	settingskeys.PlaybackSubtitleLanguage,
	settingskeys.PlaybackSubtitleMode,
	settingskeys.PlaybackShowForcedSubtitles,
}

// ListLibraryPlaybackPreferencesCanonical is the listing behind v2
// listLibraryPlaybackPreferences. It assembles one preference per library
// from the canonical profile_library setting rows — the rows playback
// resolves and PUT /settings/values, the web library editor and
// PatchLibraryPlaybackPreference write — so the list shows the current
// overrides even when the legacy composite row is stale or missing. A
// library appears when it has at least one of the four keys set; its
// UpdatedAt is the newest of its rows. A library whose overrides exist only
// in the legacy row does not appear: every legacy write since the sync
// existed mirrors into canonical rows, so a legacy-only row is data written
// before the sync and is not the current state. Libraries come back in
// ascending id order, as the legacy listing does. A failure is an *APIError.
func (h *LibraryPlaybackPrefHandler) ListLibraryPlaybackPreferencesCanonical(ctx context.Context, userID int, profileID string) ([]userstore.LibraryPlaybackPreference, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	// One query bounded to this profile's profile_library rows for the four
	// keys — the store's scope listing — rather than every value the account
	// has stored; it needs no library list up front, which a resolution
	// query would.
	values, err := store.ListSettingValuesByScope(ctx, profileID, settingscontract.ScopeProfileLibrary, libraryPlaybackSettingKeys)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list library playback preferences")
	}
	byLibrary := map[int]*userstore.LibraryPlaybackPreference{}
	for _, v := range values {
		pref := byLibrary[v.LibraryID]
		if pref == nil {
			pref = &userstore.LibraryPlaybackPreference{ProfileID: profileID, LibraryID: v.LibraryID}
			byLibrary[v.LibraryID] = pref
		}
		if err := setLibraryPlaybackMember(pref, v); err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list library playback preferences")
		}
		if v.UpdatedAt > pref.UpdatedAt {
			pref.UpdatedAt = v.UpdatedAt
		}
	}
	prefs := make([]userstore.LibraryPlaybackPreference, 0, len(byLibrary))
	for _, pref := range byLibrary {
		prefs = append(prefs, *pref)
	}
	sort.Slice(prefs, func(i, j int) bool { return prefs[i].LibraryID < prefs[j].LibraryID })
	return prefs, nil
}

// setLibraryPlaybackMember decodes one canonical row into the member of pref
// its key names.
func setLibraryPlaybackMember(pref *userstore.LibraryPlaybackPreference, v userstore.SettingValue) error {
	if v.Key == settingskeys.PlaybackShowForcedSubtitles {
		if err := json.Unmarshal(v.Value, &pref.ShowForcedSubtitles); err != nil {
			return fmt.Errorf("decoding %s: %w", v.Key, err)
		}
		pref.HasShowForcedSubtitles = true
		return nil
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err != nil {
		return fmt.Errorf("decoding %s: %w", v.Key, err)
	}
	switch v.Key {
	case settingskeys.PlaybackAudioLanguage:
		pref.AudioLanguage, pref.HasAudioLanguage = s, true
	case settingskeys.PlaybackSubtitleLanguage:
		pref.SubtitleLanguage, pref.HasSubtitleLanguage = s, true
	case settingskeys.PlaybackSubtitleMode:
		pref.SubtitleMode, pref.HasSubtitleMode = s, true
	}
	return nil
}

// LibraryPlaybackPrefUpdate is the whole-row write v1 PUT
// /library-playback-prefs/{library_id} performs: each member nil means "no
// preference" for that trait and clears it.
type LibraryPlaybackPrefUpdate struct {
	AudioLanguage       *string
	SubtitleLanguage    *string
	SubtitleMode        *string
	ShowForcedSubtitles *bool
}

// PrefPatch is one member of a partial preference update. An absent member
// leaves the stored override alone; a present one with a nil Value clears
// it, with a value sets it.
type PrefPatch[T any] struct {
	Present bool
	Value   *T
}

// LibraryPlaybackPrefPatch is the partial update v2
// updateLibraryPlaybackPreference hands PatchLibraryPlaybackPreference.
type LibraryPlaybackPrefPatch struct {
	AudioLanguage       PrefPatch[string]
	SubtitleLanguage    PrefPatch[string]
	SubtitleMode        PrefPatch[string]
	ShowForcedSubtitles PrefPatch[bool]
}

// PatchLibraryPlaybackPreference is the partial write v2
// updateLibraryPlaybackPreference performs. It runs read, merge and write as
// one store transaction — on Postgres behind the per-user advisory lock the
// transaction takes first — so two patches of different members from
// different replicas both land, and it merges onto the canonical
// profile_library rows, not the legacy composite row: PUT /settings/values
// and the web library editor write the canonical rows without mirroring them
// into the legacy row, so the legacy row is not the current state. Only the
// present members write a canonical row (and publish a user_settings.changed
// event); the legacy row is rewritten from the merged set, and is removed
// when the merge leaves no override. A failure is an *APIError: an unknown
// library is 404 not_found, a subtitle_mode outside auto/always/off or a value
// the canonical contract refuses is a 400 bad_request naming the member.
func (h *LibraryPlaybackPrefHandler) PatchLibraryPlaybackPreference(ctx context.Context, userID int, profileID string, libraryID int, patch LibraryPlaybackPrefPatch) error {
	if err := h.libraryExists(ctx, libraryID); err != nil {
		return err
	}
	if patch.SubtitleMode.Present && !isValidLibrarySubtitleMode(patch.SubtitleMode.Value) {
		return fieldError("subtitle_mode", "Invalid subtitle_mode")
	}
	// The present members are validated and planned before the transaction
	// opens, so a rejected value never takes the lock.
	present, err := planLibraryPlaybackSettingsSync(setLibraryPlaybackPrefRequest{
		AudioLanguage:       patch.AudioLanguage.Value,
		SubtitleLanguage:    patch.SubtitleLanguage.Value,
		SubtitleMode:        patch.SubtitleMode.Value,
		ShowForcedSubtitles: patch.ShowForcedSubtitles.Value,
	})
	if err != nil {
		return fieldError(libraryPlaybackPrefField(err), err.Error())
	}
	presentKeys := map[string]bool{
		settingskeys.PlaybackAudioLanguage:       patch.AudioLanguage.Present,
		settingskeys.PlaybackSubtitleLanguage:    patch.SubtitleLanguage.Present,
		settingskeys.PlaybackSubtitleMode:        patch.SubtitleMode.Present,
		settingskeys.PlaybackShowForcedSubtitles: patch.ShowForcedSubtitles.Present,
	}
	writes := make([]profileSettingSync, 0, len(present))
	for _, w := range present {
		if presentKeys[w.key] {
			writes = append(writes, w)
		}
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	base := userstore.SettingIdentity{
		Scope: settingscontract.ScopeProfileLibrary, ProfileID: profileID, LibraryID: libraryID,
	}
	err = applyPlannedLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID, base,
		func(tx userstore.PreferenceSettingsWriter) ([]profileSettingSync, error) {
			merged, err := mergeLibraryPlaybackPatch(ctx, tx, base, patch)
			if err != nil {
				return nil, err
			}
			pref, has := libraryPlaybackPreferenceOf(profileID, libraryID, merged)
			if !has {
				return writes, tx.DeleteLibraryPlaybackPreference(ctx, profileID, libraryID)
			}
			return writes, tx.UpsertLibraryPlaybackPreference(ctx, pref)
		})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to store library playback preference")
	}
	return nil
}

// mergeLibraryPlaybackPatch is the row the patch leaves: each present member
// as given, each omitted one as the canonical profile_library row currently
// holds (nil when there is none). It reads through the transaction's writer.
func mergeLibraryPlaybackPatch(ctx context.Context, tx userstore.PreferenceSettingsWriter, base userstore.SettingIdentity, patch LibraryPlaybackPrefPatch) (LibraryPlaybackPrefUpdate, error) {
	var merged LibraryPlaybackPrefUpdate
	for _, m := range []struct {
		key   string
		patch PrefPatch[string]
		into  **string
	}{
		{settingskeys.PlaybackAudioLanguage, patch.AudioLanguage, &merged.AudioLanguage},
		{settingskeys.PlaybackSubtitleLanguage, patch.SubtitleLanguage, &merged.SubtitleLanguage},
		{settingskeys.PlaybackSubtitleMode, patch.SubtitleMode, &merged.SubtitleMode},
	} {
		if m.patch.Present {
			*m.into = m.patch.Value
			continue
		}
		if err := canonicalMember(ctx, tx, base, m.key, m.into); err != nil {
			return merged, err
		}
	}
	if patch.ShowForcedSubtitles.Present {
		merged.ShowForcedSubtitles = patch.ShowForcedSubtitles.Value
	} else if err := canonicalMember(ctx, tx, base, settingskeys.PlaybackShowForcedSubtitles, &merged.ShowForcedSubtitles); err != nil {
		return merged, err
	}
	return merged, nil
}

// canonicalMember loads the canonical row for key at base into *into, leaving
// it nil when the row is unset.
func canonicalMember[T any](ctx context.Context, tx userstore.PreferenceSettingsWriter, base userstore.SettingIdentity, key string, into **T) error {
	id := base
	id.Key = key
	row, err := tx.GetSettingValue(ctx, id)
	if err != nil {
		return fmt.Errorf("reading %s: %w", key, err)
	}
	if row == nil {
		*into = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(row.Value, &v); err != nil {
		return fmt.Errorf("decoding %s: %w", key, err)
	}
	*into = &v
	return nil
}

// libraryPlaybackPreferenceOf is the legacy composite row for req, and
// whether any member is set; a request with none is the row-removal form.
func libraryPlaybackPreferenceOf(profileID string, libraryID int, req LibraryPlaybackPrefUpdate) (userstore.LibraryPlaybackPreference, bool) {
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
	return pref, pref.HasAudioLanguage || pref.HasSubtitleLanguage || pref.HasSubtitleMode || pref.HasShowForcedSubtitles
}

// setLibraryPlaybackPreference is the whole-row write behind v1 PUT, after
// the library check the v1 handler runs before it reads the body: the legacy
// row and the canonical profile_library rows commit together, and a
// user_settings.changed event follows each canonical row that moved. A
// request with every member nil removes the row.
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

	pref, has := libraryPlaybackPreferenceOf(profileID, libraryID, req)
	if !has {
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

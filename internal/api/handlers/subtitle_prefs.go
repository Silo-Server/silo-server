package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SubtitlePrefHandler handles per-series subtitle preference endpoints.
//
// These are legacy endpoints: the shipped clients still write per-series
// subtitle choices here, but the item-detail read path resolves the language,
// mode and forced flags canonically from user_setting_values (see
// catalog.DetailService.effectiveSubtitleDefaults) and only consults the
// legacy row for the track signature. Every write therefore mirrors into the
// profile_series-scoped canonical rows, the same shape the profile endpoints
// use in profiles_settings_sync.go — a legacy write that never reaches the
// canonical store simply never takes effect.
type SubtitlePrefHandler struct {
	storeProvider userstore.UserStoreProvider
	// EventsHub, when set, receives a user_settings.changed event for every
	// canonical setting row a subtitle-preference mutation syncs. Nil (as in
	// tests) simply skips publishing.
	EventsHub *evt.Hub
}

// NewSubtitlePrefHandler creates a new SubtitlePrefHandler.
func NewSubtitlePrefHandler(provider userstore.UserStoreProvider) *SubtitlePrefHandler {
	return &SubtitlePrefHandler{storeProvider: provider}
}

// --- Request/Response types ---

type setSubtitlePrefRequest struct {
	SubtitleLanguage     string                            `json:"subtitle_language"`
	SubtitleTrackIndex   int                               `json:"subtitle_track_index"`
	ExternalSubtitlePath string                            `json:"external_subtitle_path,omitempty"`
	SubtitleMode         string                            `json:"subtitle_mode"`
	TrackSignature       *userstore.SubtitleTrackSignature `json:"track_signature,omitempty"`
	ShowForcedSubtitles  *bool                             `json:"show_forced_subtitles,omitempty"`
}

type subtitlePrefResponse struct {
	ProfileID            string                            `json:"profile_id"`
	SeriesID             string                            `json:"series_id"`
	SubtitleLanguage     string                            `json:"subtitle_language"`
	SubtitleTrackIndex   int                               `json:"subtitle_track_index"`
	ExternalSubtitlePath string                            `json:"external_subtitle_path,omitempty"`
	SubtitleMode         string                            `json:"subtitle_mode"`
	TrackSignature       *userstore.SubtitleTrackSignature `json:"track_signature,omitempty"`
	ShowForcedSubtitles  *bool                             `json:"show_forced_subtitles,omitempty"`
	UpdatedAt            string                            `json:"updated_at"`
}

// --- Handler methods ---

// HandleGetSubtitlePref handles GET /subtitle-prefs/{series_id}.
func (h *SubtitlePrefHandler) HandleGetSubtitlePref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	pref, err := h.GetSubtitlePreference(r.Context(), userID, profileID, seriesID)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toSubtitlePrefResponse(pref))
}

// GetSubtitlePreference is the read v1 GET /subtitle-prefs/{series_id} and
// v2 getSubtitlePreference share. A failure is an *APIError; a profile with
// no preference for the series is 404 not_found.
func (h *SubtitlePrefHandler) GetSubtitlePreference(ctx context.Context, userID int, profileID, seriesID string) (userstore.SubtitlePreference, error) {
	var none userstore.SubtitlePreference
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	pref, err := store.GetSubtitlePreference(ctx, profileID, seriesID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to get subtitle preference")
	}
	if pref == nil {
		return none, apiError(http.StatusNotFound, "not_found", "Subtitle preference not found")
	}
	return *pref, nil
}

// HandleSetSubtitlePref handles PUT /subtitle-prefs/{series_id}.
func (h *SubtitlePrefHandler) HandleSetSubtitlePref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	var req setSubtitlePrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	pref := userstore.SubtitlePreference{
		ProfileID:            profileID,
		SeriesID:             seriesID,
		SubtitleLanguage:     req.SubtitleLanguage,
		SubtitleTrackIndex:   req.SubtitleTrackIndex,
		ExternalSubtitlePath: req.ExternalSubtitlePath,
		SubtitleMode:         req.SubtitleMode,
		TrackSignature:       req.TrackSignature,
	}
	if req.ShowForcedSubtitles != nil {
		pref.ShowForcedSubtitles = *req.ShowForcedSubtitles
		pref.HasShowForcedSubtitles = true
	}

	if err := h.SetSubtitlePreference(r.Context(), userID, pref); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SetSubtitlePreference is the write behind v1 PUT /subtitle-prefs/{series_id}:
// the legacy row and the canonical profile_series rows commit together, and
// a user_settings.changed event follows each canonical row that moved. The
// row is replaced whole, except that a preference without a forced-subtitle
// override (!HasShowForcedSubtitles) keeps the override the legacy row
// already stores, so a client updating only the track selection does not
// silently reset it. A failure is an *APIError; a value the canonical
// contract refuses is a 400 bad_request.
func (h *SubtitlePrefHandler) SetSubtitlePreference(ctx context.Context, userID int, pref userstore.SubtitlePreference) error {
	return h.setSubtitlePreference(ctx, userID, pref, false)
}

// SetSubtitlePreferenceCanonical is the write behind v2
// updateSubtitlePreference: SetSubtitlePreference, except that the forced
// override an omitted show_forced_subtitles keeps is read from the canonical
// profile_series row alone — inside the store transaction, after the Postgres
// per-user advisory lock. The legacy row is never consulted: PUT and DELETE
// /settings/values change the canonical row without mirroring it into the
// legacy row, so a stale legacy flag would resurrect an override a client
// already cleared. An absent canonical row is "no override", and the merged
// legacy row and the canonical write then carry no forced flag. v1 keeps its
// legacy-row lookup unchanged.
func (h *SubtitlePrefHandler) SetSubtitlePreferenceCanonical(ctx context.Context, userID int, pref userstore.SubtitlePreference) error {
	return h.setSubtitlePreference(ctx, userID, pref, true)
}

func (h *SubtitlePrefHandler) setSubtitlePreference(ctx context.Context, userID int, pref userstore.SubtitlePreference, mergeCanonical bool) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	// v1 keeps the override the legacy row stores; the canonical path reads
	// the canonical row inside the transaction below and consults nothing else.
	bodyHasForced := pref.HasShowForcedSubtitles
	if !bodyHasForced && !mergeCanonical {
		existing, getErr := store.GetSubtitlePreference(ctx, pref.ProfileID, pref.SeriesID)
		if getErr != nil {
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to preserve subtitle preference")
		}
		if existing != nil && existing.HasShowForcedSubtitles {
			pref.ShowForcedSubtitles = existing.ShowForcedSubtitles
			pref.HasShowForcedSubtitles = true
		}
	}

	// Planned before the legacy write: a value the canonical store would
	// refuse must fail the request while it is still a no-op, not leave the
	// legacy row and the canonical rows disagreeing.
	sync, err := planSeriesSubtitleSync(pref)
	if err != nil {
		return apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	if !mergeCanonical || bodyHasForced {
		if err := h.applySeriesSubtitleSync(ctx, store, userID, pref.ProfileID, pref.SeriesID, sync,
			func(tx userstore.PreferenceSettingsWriter) error {
				return tx.SetSubtitlePreference(ctx, pref)
			}); err != nil {
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to store subtitle preference")
		}
		return nil
	}

	// The canonical row is read inside the transaction so no writer can slip
	// between the read and the rewrite; the forced flag is a bool, so the
	// re-plan cannot fail on a value the plan above accepted. An absent row
	// is no override: the legacy row is not consulted, so a flag it still
	// holds after DELETE /settings/values cleared the canonical row cannot
	// come back.
	base := userstore.SettingIdentity{
		Scope: settingscontract.ScopeProfileSeries, ProfileID: pref.ProfileID, SeriesID: pref.SeriesID,
	}
	err = applyPlannedLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID, base,
		func(tx userstore.PreferenceSettingsWriter) ([]profileSettingSync, error) {
			var forced *bool
			if err := canonicalMember(ctx, tx, base, settingskeys.PlaybackShowForcedSubtitles, &forced); err != nil {
				return nil, err
			}
			pref.HasShowForcedSubtitles = forced != nil
			if forced != nil {
				pref.ShowForcedSubtitles = *forced
			}
			writes, err := planSeriesSubtitleSync(pref)
			if err != nil {
				return nil, err
			}
			return writes, tx.SetSubtitlePreference(ctx, pref)
		})
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to store subtitle preference")
	}
	return nil
}

// HandleDeleteSubtitlePref handles DELETE /subtitle-prefs/{series_id}.
func (h *SubtitlePrefHandler) HandleDeleteSubtitlePref(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	seriesID := chi.URLParam(r, "series_id")

	if seriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	if err := h.DeleteSubtitlePreference(r.Context(), userID, profileID, seriesID); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteSubtitlePreference is the delete v1 DELETE /subtitle-prefs/{series_id}
// and v2 deleteSubtitlePreference share. Deleting a preference that does not
// exist succeeds. A failure is an *APIError.
func (h *SubtitlePrefHandler) DeleteSubtitlePreference(ctx context.Context, userID int, profileID, seriesID string) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	// Deleting the legacy row means "no per-series preference", spelled
	// canonically as the absence of the profile_series rows.
	if err := h.applySeriesSubtitleSync(ctx, store, userID, profileID, seriesID,
		[]profileSettingSync{
			{key: settingskeys.PlaybackSubtitleLanguage},
			{key: settingskeys.PlaybackSubtitleMode},
			{key: settingskeys.PlaybackShowForcedSubtitles},
		}, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.DeleteSubtitlePreference(ctx, profileID, seriesID)
		}); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete subtitle preference")
	}
	return nil
}

// --- Canonical sync ---

// planSeriesSubtitleSync plans the profile_series-scoped canonical writes a
// legacy subtitle-preference write implies. The mapping mirrors
// settingsmigrate.planSeriesPrefs: the empty string is the legacy spelling of
// "no preference" and clears the canonical row, and a set forced flag is a
// real override in either direction. Track index, external path and signature
// identify concrete tracks rather than expressing preferences, so they stay on
// the legacy row only.
func planSeriesSubtitleSync(pref userstore.SubtitlePreference) ([]profileSettingSync, error) {
	language := pref.SubtitleLanguage
	mode := pref.SubtitleMode
	// No skip fields: a series subtitle preference carries none, and the four
	// booleans are profile-scope anyway.
	out, err := planProfileSettingsSync(nil, &language, nil, &mode, nil, profileSkipFields{})
	if err != nil {
		return nil, err
	}
	if pref.HasShowForcedSubtitles {
		out = append(out, profileSettingSync{
			key:   settingskeys.PlaybackShowForcedSubtitles,
			value: json.RawMessage(strconv.FormatBool(pref.ShowForcedSubtitles)),
		})
	} else {
		out = append(out, profileSettingSync{key: settingskeys.PlaybackShowForcedSubtitles})
	}
	return out, nil
}

// applySeriesSubtitleSync writes the planned canonical rows at profile_series
// scope and publishes a user_settings.changed event for every row that moved,
// the same signal a /settings/values write sends. It is the per-series
// counterpart of ProfileHandler.applyProfileSettingsSync.
func (h *SubtitlePrefHandler) applySeriesSubtitleSync(
	ctx context.Context,
	store userstore.UserStore,
	userID int,
	profileID, seriesID string,
	writes []profileSettingSync,
	legacyMutation func(userstore.PreferenceSettingsWriter) error,
) error {
	return applyLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID, userstore.SettingIdentity{
		Scope: settingscontract.ScopeProfileSeries, ProfileID: profileID, SeriesID: seriesID,
	}, writes, legacyMutation)
}

// --- Helpers ---

func toSubtitlePrefResponse(p userstore.SubtitlePreference) subtitlePrefResponse {
	resp := subtitlePrefResponse{
		ProfileID:            p.ProfileID,
		SeriesID:             p.SeriesID,
		SubtitleLanguage:     p.SubtitleLanguage,
		SubtitleTrackIndex:   p.SubtitleTrackIndex,
		ExternalSubtitlePath: p.ExternalSubtitlePath,
		SubtitleMode:         p.SubtitleMode,
		TrackSignature:       p.TrackSignature,
		UpdatedAt:            p.UpdatedAt,
	}
	if p.HasShowForcedSubtitles {
		resp.ShowForcedSubtitles = boolPtr(p.ShowForcedSubtitles)
	}
	return resp
}

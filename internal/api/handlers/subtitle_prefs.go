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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	pref, err := store.GetSubtitlePreference(r.Context(), profileID, seriesID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get subtitle preference")
		return
	}

	if pref == nil {
		writeError(w, http.StatusNotFound, "not_found", "Subtitle preference not found")
		return
	}

	writeJSON(w, http.StatusOK, toSubtitlePrefResponse(*pref))
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
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
	} else {
		// This endpoint replaces the combined subtitle-preference row. Preserve
		// an existing forced-subtitle override when clients update only the track
		// selection; otherwise an omitted optional field silently resets it.
		existing, getErr := store.GetSubtitlePreference(r.Context(), profileID, seriesID)
		if getErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to preserve subtitle preference")
			return
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
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	if err := h.applySeriesSubtitleSync(r.Context(), store, userID, profileID, seriesID, sync,
		func(tx userstore.PreferenceSettingsWriter) error {
			return tx.SetSubtitlePreference(r.Context(), pref)
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store subtitle preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	// Deleting the legacy row means "no per-series preference", spelled
	// canonically as the absence of the profile_series rows.
	if err := h.applySeriesSubtitleSync(r.Context(), store, userID, profileID, seriesID,
		[]profileSettingSync{
			{key: settingskeys.PlaybackSubtitleLanguage},
			{key: settingskeys.PlaybackSubtitleMode},
			{key: settingskeys.PlaybackShowForcedSubtitles},
		}, func(tx userstore.PreferenceSettingsWriter) error {
			return tx.DeleteSubtitlePreference(r.Context(), profileID, seriesID)
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete subtitle preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

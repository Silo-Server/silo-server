package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The v1 HTTP surface of the settings values API. Each handler parses the
// request, runs the shared core in settings_values_seam.go, and renders the
// core's decision with the same status, code and message v1 always wrote.

// identityRequestFrom reads the parts of a request that name a stored value.
func (h *SettingValuesHandler) identityRequestFrom(r *http.Request, key string) SettingIdentityRequest {
	query := r.URL.Query()
	return SettingIdentityRequest{
		Key:             key,
		Scope:           query.Get("scope"),
		ActiveProfileID: apimw.GetProfileID(r.Context()),
		ProfileID:       query.Get("profile_id"),
		DeviceID:        query.Get("device_id"),
		Device:          deviceMetadataFromRequest(r),
		ClientFamily:    r.Header.Get(clientFamilyHeader),
		LibraryID:       query.Get("library_id"),
		SeriesID:        query.Get("series_id"),
		VerifyProfile: func(profileID string) error {
			return verifyProfileToken(r, h.UserRepo, h.ProfileTokens, profileID)
		},
	}
}

// identityFromRequest builds and validates the scope identity a request names.
func (h *SettingValuesHandler) identityFromRequest(
	w http.ResponseWriter, r *http.Request, store userstore.UserStore,
) (userstore.SettingIdentity, bool) {
	identity, err := h.sessionIdentity(r.Context(), store, h.identityRequestFrom(r, chi.URLParam(r, "key")))
	if err != nil {
		writeAPIError(w, err)
		return userstore.SettingIdentity{}, false
	}
	return identity, true
}

// HandleGetValue returns the explicit value at one scope, or 404 when
// nothing is stored there.
func (h *SettingValuesHandler) HandleGetValue(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	identity, ok := h.identityFromRequest(w, r, store)
	if !ok {
		return
	}
	view, err := h.readSettingValue(r.Context(), store, identity)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// HandleGetValues returns the explicit values for several keys at exactly one
// scope. Missing rows remain in the response with is_set false.
func (h *SettingValuesHandler) HandleGetValues(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}

	keys := parseSettingKeys(r.URL.Query().Get("keys"))
	if len(keys) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Query parameter keys is required")
		return
	}
	identity, err := h.sessionIdentity(r.Context(), store, h.identityRequestFrom(r, keys[0]))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out, err := h.listExplicitValues(r.Context(), store, identity, keys)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		fieldValues:   out,
		fieldRevision: h.contract.Revision,
	})
}

// HandleSetValue writes an explicit value at one scope.
func (h *SettingValuesHandler) HandleSetValue(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	identity, ok := h.identityFromRequest(w, r, store)
	if !ok {
		return
	}
	if identity.Key == settingskeys.NavShortcuts {
		writeError(w, http.StatusBadRequest, "atomic_update_required",
			navigationShortcutAtomicUpdateMessage)
		return
	}
	h.setValueAt(w, r, store, apimw.GetUserID(r.Context()), identity)
}

// setValueAt decodes the {value} envelope and runs the shared write for the
// session and admin routes.
func (h *SettingValuesHandler) setValueAt(
	w http.ResponseWriter,
	r *http.Request,
	store userstore.UserStore,
	eventUserID int,
	identity userstore.SettingIdentity,
) {
	if _, ok := h.definitionFor(w, identity.Key); !ok {
		return
	}

	var body struct {
		Value json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Body must be {\"value\": …}")
		return
	}
	// The body must be exactly one JSON document: content after the envelope
	// would mean different parsers could disagree about which mutation this is.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Body must be a single JSON document")
		return
	}

	mutationID := strings.TrimSpace(r.Header.Get(mutationIDHeader))
	result, err := h.writeSettingValue(r.Context(), store, eventUserID, identity, body.Value, mutationID, deviceMetadataFromRequest(r))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if result.replay {
		w.Header().Set("X-Silo-Idempotent-Replay", "true")
	}
	if result.raw != nil {
		writeRawJSON(w, http.StatusOK, result.raw)
	} else {
		writeJSON(w, http.StatusOK, result.response)
	}
}

// HandleSetNavigationShortcut applies one desired-state edit to the shared
// profile shortcut catalog.
func (h *SettingValuesHandler) HandleSetNavigationShortcut(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}

	var body navigationShortcutMutationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request",
			"Body must be {\"item\": {…}, \"present\": true|false}")
		return
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Body must be a single JSON document")
		return
	}

	mutationID := strings.TrimSpace(r.Header.Get(mutationIDHeader))
	result, err := h.setNavigationShortcut(r.Context(), store, apimw.GetUserID(r.Context()),
		apimw.GetProfileID(r.Context()), body.Item, body.Present, mutationID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if result.replay {
		w.Header().Set("X-Silo-Idempotent-Replay", "true")
	}
	if result.raw != nil {
		writeRawJSON(w, http.StatusOK, result.raw)
	} else {
		writeJSON(w, http.StatusOK, result.response)
	}
}

// HandleDeleteValue removes the explicit value at one scope, which is how a
// client says "stop overriding here and inherit again".
func (h *SettingValuesHandler) HandleDeleteValue(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	identity, ok := h.identityFromRequest(w, r, store)
	if !ok {
		return
	}
	if identity.Key == settingskeys.NavShortcuts {
		writeError(w, http.StatusBadRequest, "atomic_update_required",
			navigationShortcutAtomicUpdateMessage)
		return
	}
	h.deleteValueAt(w, r, store, apimw.GetUserID(r.Context()), identity)
}

// deleteValueAt is the unset path shared by the session and admin routes.
func (h *SettingValuesHandler) deleteValueAt(
	w http.ResponseWriter,
	r *http.Request,
	store userstore.UserStore,
	eventUserID int,
	identity userstore.SettingIdentity,
) {
	if err := h.clearSettingValue(r.Context(), store, eventUserID, identity); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SettingValuesHandler) effectiveQueryFrom(r *http.Request, keys []string) EffectiveSettingsQuery {
	query := r.URL.Query()
	return EffectiveSettingsQuery{
		Keys:            keys,
		ActiveProfileID: apimw.GetProfileID(r.Context()),
		ProfileID:       query.Get("profile_id"),
		DeviceID:        query.Get("device_id"),
		Device:          deviceMetadataFromRequest(r),
		ClientFamily:    r.Header.Get(clientFamilyHeader),
		LibraryIDs:      parseIntCSV(query.Get("library_ids")),
		SeriesIDs:       splitCSV(query.Get("series_ids")),
		VerifyProfile: func(profileID string) error {
			return verifyProfileToken(r, h.UserRepo, h.ProfileTokens, profileID)
		},
	}
}

// HandleGetEffective resolves any number of keys in one request.
func (h *SettingValuesHandler) HandleGetEffective(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	out, err := h.resolveEffective(r.Context(), store, h.effectiveQueryFrom(r, splitCSV(r.URL.Query().Get("keys"))))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":    out,
		fieldRevision: h.contract.Revision,
	})
}

type effectiveContextRequest struct {
	ContextID string          `json:"context_id"`
	LibraryID json.RawMessage `json:"library_id,omitempty"`
	SeriesID  string          `json:"series_id,omitempty"`
}

type effectiveContextResponse struct {
	ContextID string                          `json:"context_id"`
	Settings  []effectiveSettingValueResponse `json:"settings"`
}

// HandlePostEffective resolves several content contexts in one prepared batch.
func (h *SettingValuesHandler) HandlePostEffective(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}

	var body struct {
		Keys     []string                  `json:"keys"`
		Contexts []effectiveContextRequest `json:"contexts"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Body must be a single JSON document")
		return
	}

	out, err := h.resolveEffectiveContexts(r.Context(), store, h.effectiveQueryFrom(r, body.Keys), body.Contexts)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"contexts":    out,
		fieldRevision: h.contract.Revision,
	})
}

// constraintsFor gathers the policy inputs that narrow this viewer's settings.
//
// The routes are mounted inside RequireViewerAccess, so the resolved access
// scope is already on the context. Scope.MaxPlaybackQuality holds a literal
// member of the contract's quality enum ("1080p", "2160p"), so it feeds the
// ceiling on playback.preferred_quality directly — no translation table. An
// empty value means the policy does not cap this viewer, expressed by omitting
// the key so the resolver leaves the preference alone.
//
// catalog.metadata_language deliberately gets no constraint: the manifest notes
// record that the allowlist draft was circular, because the policy input it
// would bind to is populated from the very preference it would narrow.
func (h *SettingValuesHandler) constraintsFor(ctx context.Context) settingsresolve.Constraints {
	scope, ok := access.GetScope(ctx)
	if !ok {
		return nil
	}
	quality := strings.TrimSpace(scope.MaxPlaybackQuality)
	if quality == "" {
		return nil
	}
	limit, err := json.Marshal(quality)
	if err != nil {
		return nil
	}
	return settingsresolve.Constraints{policyInputMaxPlaybackQuality: limit}
}

func (h *SettingValuesHandler) definitionFor(w http.ResponseWriter, key string) (*settingscontract.Definition, bool) {
	def, err := h.definition(key)
	if err != nil {
		writeAPIError(w, err)
		return nil, false
	}
	return def, true
}

// registerWritingDevice refreshes the device registry from the request's
// declared device after a canonical device-scope write.
func (h *SettingValuesHandler) registerWritingDevice(
	ctx context.Context, store userstore.UserStore, profileID string, device DeviceMetadata,
) {
	if profileID == "" || device.DeviceID == "" {
		return
	}
	if h.deviceSeen != nil {
		key := profileID + "\x00" + device.DeviceID
		if _, seen := h.deviceSeen.Get(key); seen {
			return
		}
		h.deviceSeen.Set(key, struct{}{}, deviceSeenThrottle)
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		return
	}
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID:      profileID,
		DeviceID:       device.DeviceID,
		DeviceName:     device.DeviceName,
		DevicePlatform: device.DevicePlatform,
	}); err != nil {
		slog.WarnContext(ctx, "failed to register device after canonical write",
			"component", "api",
			"profile_id", profileID,
			"device_id", device.DeviceID,
			"error", err,
		)
	}
}

func (h *SettingValuesHandler) effectiveResponses(
	ctx context.Context,
	resolved []settingsresolve.Effective,
) []effectiveSettingValueResponse {
	observed := h.observedLanguageSuggestions(ctx, resolved)
	return h.effectiveResponsesWithObserved(resolved, observed)
}

func (h *SettingValuesHandler) observedLanguageSuggestions(
	ctx context.Context,
	resolved []settingsresolve.Effective,
) map[string][]string {
	result := make(map[string][]string)
	if h.languageSource == nil {
		return result
	}
	found := false
	for _, eff := range resolved {
		if eff.Key == settingskeys.CatalogMetadataLanguage {
			found = true
			break
		}
	}
	if !found {
		return result
	}

	filters := catalog.BrowseFilters{}
	if scope, ok := access.GetScope(ctx); ok {
		filters.LibraryIDs = scope.AllowedLibraryIDs
		filters.DisabledLibraryIDs = scope.DisabledLibraryIDs
		filters.MaxContentRating = scope.MaxContentRating
	}
	values, err := h.languageSource.ListOriginalLanguages(ctx, filters)
	if err != nil {
		slog.WarnContext(ctx, "settings: listing metadata language suggestions",
			"component", "settings", "error", err)
		return result
	}
	result[settingskeys.CatalogMetadataLanguage] = values
	return result
}

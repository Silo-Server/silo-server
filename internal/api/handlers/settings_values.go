package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SettingValuesHandler serves the canonical settings API: the contract itself,
// explicit values at one scope, batched effective values, and idempotent
// mutations.
//
// It replaces the string-only registry in settings.go. The differences that
// matter: values are typed JSON rather than strings, a value carries the scope
// it lives at rather than being one of two hardcoded scopes, and an unknown key
// is refused rather than stored in an open extension bag.
type SettingValuesHandler struct {
	storeProvider userstore.UserStoreProvider
	contract      *settingscontract.Manifest
	resolver      *settingsresolve.Resolver

	// EventsHub, when set, receives a user_settings.changed event after every
	// successful write or delete. Nil (as in tests) simply skips publishing.
	EventsHub *evt.Hub
}

// NewSettingValuesHandler builds the handler over the embedded contract.
func NewSettingValuesHandler(
	provider userstore.UserStoreProvider,
	contract *settingscontract.Manifest,
) *SettingValuesHandler {
	return &SettingValuesHandler{
		storeProvider: provider,
		contract:      contract,
		resolver:      settingsresolve.New(contract),
	}
}

// mutationIDHeader carries the client's idempotency key.
const mutationIDHeader = "X-Silo-Mutation-Id"

// fieldRevision is the response field carrying the contract revision. Clients
// filter definitions, scopes and enum members against it, so every response
// that could be acted on names the revision it was computed at.
const fieldRevision = "revision"

// settingValueResponse is one explicit stored value.
type settingValueResponse struct {
	Key       string          `json:"key"`
	Scope     string          `json:"scope"`
	ProfileID string          `json:"profile_id,omitempty"`
	DeviceID  string          `json:"device_id,omitempty"`
	LibraryID int             `json:"library_id,omitempty"`
	SeriesID  string          `json:"series_id,omitempty"`
	Value     json.RawMessage `json:"value"`
	Revision  int64           `json:"revision"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

// effectiveSettingValueResponse is one resolved value plus where it came from.
type effectiveSettingValueResponse struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	Source string          `json:"source"`

	// StoredValue and Constrained are present only when policy narrowed the
	// answer. The authored value is reported rather than discarded so a client
	// can say "your choice is capped" instead of silently showing the cap.
	StoredValue    json.RawMessage `json:"stored_value,omitempty"`
	Constrained    bool            `json:"constrained,omitempty"`
	ConstraintKind string          `json:"constraint_kind,omitempty"`

	// Scope locates the row the value came from, so a client can offer a reset
	// against exactly that scope. Empty for a contract default.
	Scope     string `json:"scope,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	LibraryID int    `json:"library_id,omitempty"`
	SeriesID  string `json:"series_id,omitempty"`
}

// HandleGetContract serves the public manifest.
//
// ETag-gated: clients vendor a pinned copy and generate bindings from it, so
// the common request is a conditional GET that answers "still the same
// contract?" without transferring it.
func (h *SettingValuesHandler) HandleGetContract(w http.ResponseWriter, r *http.Request) {
	etag, err := settingscontract.PublicETag()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read the settings contract")
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body, err := settingscontract.PublicBytes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read the settings contract")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// HandleGetCapabilities reports what this server supports, for feature
// detection rather than version sniffing.
func (h *SettingValuesHandler) HandleGetCapabilities(w http.ResponseWriter, r *http.Request) {
	etag, err := settingscontract.PublicETag()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read the settings contract")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_version":      h.contract.APIVersion,
		fieldRevision:      h.contract.Revision,
		"contract_etag":    etag,
		"definition_count": len(h.contract.Definitions),
		"scopes": []string{
			string(settingscontract.ScopeAccount),
			string(settingscontract.ScopeProfile),
			string(settingscontract.ScopeProfileDevice),
			string(settingscontract.ScopeProfileLibrary),
			string(settingscontract.ScopeProfileSeries),
		},
		"supports_batched_effective": true,
		"supports_idempotent_writes": true,
	})
}

// HandleGetValue returns the explicit value at one scope, or 404 when the user
// has none there.
//
// Deliberately not a resolution: this answers "did I set this here", which is
// what a reset affordance needs. Use the effective endpoint for "what applies".
func (h *SettingValuesHandler) HandleGetValue(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	identity, ok := h.identityFromRequest(w, r)
	if !ok {
		return
	}

	value, err := store.GetSettingValue(r.Context(), identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read the setting")
		return
	}
	if value == nil {
		writeError(w, http.StatusNotFound, "not_found", "No value is set at this scope")
		return
	}
	writeJSON(w, http.StatusOK, settingValueToResponse(*value))
}

// HandleSetValue writes an explicit value at one scope.
//
// A value that exceeds a policy restriction is stored, not rejected: the
// restriction filters what a preference does at resolution time, and destroying
// the preference would mean a capped 4K choice never takes effect when the cap
// lifts.
func (h *SettingValuesHandler) HandleSetValue(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	identity, ok := h.identityFromRequest(w, r)
	if !ok {
		return
	}
	def, ok := h.definitionFor(w, identity.Key)
	if !ok {
		return
	}

	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Body must be {\"value\": …}")
		return
	}
	if len(body.Value) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "value is required")
		return
	}

	normalized, err := def.ValueSchema.NormalizeValue(body.Value, settingscontract.ObjectSchemas())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_value", err.Error())
		return
	}

	// Idempotency: a client that retries a write after a dropped response must
	// not double-apply it, and must be able to tell "already done" from "that
	// id means something else".
	mutationID := strings.TrimSpace(r.Header.Get(mutationIDHeader))
	if mutationID != "" {
		requestHash := hashMutationRequest(identity, normalized)
		prior, err := store.GetSettingMutation(r.Context(), mutationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check the mutation id")
			return
		}
		if prior != nil {
			if prior.RequestHash != requestHash {
				writeError(w, http.StatusConflict, "mutation_id_conflict",
					"This mutation id was used for a different write")
				return
			}
			w.Header().Set("X-Silo-Idempotent-Replay", "true")
			writeRawJSON(w, http.StatusOK, prior.Result)
			return
		}
		defer h.recordMutation(r, store, mutationID, requestHash, identity, normalized)
	}

	stored, err := store.UpsertSettingValue(r.Context(), identity, normalized)
	if err != nil {
		if errors.Is(err, userstore.ErrInvalidSettingIdentity) ||
			errors.Is(err, userstore.ErrInvalidSettingValue) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store the setting")
		return
	}
	publishUserSettingsEvent(r.Context(), h.EventsHub,
		apimw.GetUserID(r.Context()), identity.ProfileID, identity.Key, string(identity.Scope))
	writeJSON(w, http.StatusOK, settingValueToResponse(*stored))
}

// HandleDeleteValue removes the explicit value at one scope, which is how a
// client says "stop overriding here and inherit again".
func (h *SettingValuesHandler) HandleDeleteValue(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	identity, ok := h.identityFromRequest(w, r)
	if !ok {
		return
	}

	removed, err := store.DeleteSettingValue(r.Context(), identity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear the setting")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "not_found", "No value is set at this scope")
		return
	}
	publishUserSettingsEvent(r.Context(), h.EventsHub,
		apimw.GetUserID(r.Context()), identity.ProfileID, identity.Key, string(identity.Scope))
	w.WriteHeader(http.StatusNoContent)
}

// HandleGetEffective resolves any number of keys in one request.
//
// Batched deliberately: a client opening a settings screen needs every key at
// once, and a season view needs several keys across many series. One store read
// serves all of it.
func (h *SettingValuesHandler) HandleGetEffective(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}

	keys := splitCSV(r.URL.Query().Get("keys"))
	if len(keys) == 0 {
		// No keys named means every remote definition, which is what a settings
		// screen wants and saves clients enumerating the manifest themselves.
		for i := range h.contract.Definitions {
			def := &h.contract.Definitions[i]
			if def.IsRemote() {
				keys = append(keys, def.Key)
			}
		}
	}

	rc := settingsresolve.Context{
		ProfileID:  strings.TrimSpace(apimw.GetProfileID(r.Context())),
		DeviceID:   deviceMetadataFromRequest(r).DeviceID,
		LibraryIDs: parseIntCSV(r.URL.Query().Get("library_ids")),
		SeriesIDs:  splitCSV(r.URL.Query().Get("series_ids")),
	}

	resolved, err := h.resolver.Resolve(r.Context(), store, rc, keys, h.constraintsFor(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve settings")
		return
	}

	out := make([]effectiveSettingValueResponse, 0, len(resolved))
	for _, eff := range resolved {
		out = append(out, effectiveToResponse(eff))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":    out,
		fieldRevision: h.contract.Revision,
	})
}

// policyInputMaxPlaybackQuality is the policy_input name the contract binds
// playback.preferred_quality's ceiling to. It must match the manifest's
// constrained_by.policy_input, which is how the resolver looks the limit up.
const policyInputMaxPlaybackQuality = "max_playback_quality"

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
func (h *SettingValuesHandler) constraintsFor(r *http.Request) settingsresolve.Constraints {
	scope, ok := access.GetScope(r.Context())
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

// recordMutation stores the idempotency receipt after a successful write.
func (h *SettingValuesHandler) recordMutation(
	r *http.Request,
	store userstore.UserStore,
	mutationID, requestHash string,
	identity userstore.SettingIdentity,
	value json.RawMessage,
) {
	receipt, _ := json.Marshal(settingValueResponse{
		Key:       identity.Key,
		Scope:     string(identity.Scope),
		ProfileID: identity.ProfileID,
		DeviceID:  identity.DeviceID,
		LibraryID: identity.LibraryID,
		SeriesID:  identity.SeriesID,
		Value:     value,
	})
	// Best effort: the write already succeeded, and failing the request now
	// would tell the client the opposite of the truth. A missing receipt costs
	// at most a duplicate write on retry, which upsert makes harmless.
	_, _, _ = store.PutSettingMutation(r.Context(), userstore.SettingMutationRecord{
		MutationID:  mutationID,
		RequestHash: requestHash,
		Result:      receipt,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(30 * 24 * time.Hour),
	})
}

func (h *SettingValuesHandler) storeFor(w http.ResponseWriter, r *http.Request) (userstore.UserStore, bool) {
	store, err := h.storeProvider.ForUser(r.Context(), apimw.GetUserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return nil, false
	}
	return store, true
}

func (h *SettingValuesHandler) definitionFor(w http.ResponseWriter, key string) (*settingscontract.Definition, bool) {
	def, ok := h.contract.Lookup(key)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_setting",
			"No setting named "+key+" exists in this server's contract")
		return nil, false
	}
	if !def.IsRemote() {
		writeError(w, http.StatusBadRequest, "client_local_setting",
			key+" is a device-local setting and is never stored by the server")
		return nil, false
	}
	return def, true
}

// identityFromRequest builds and validates the scope identity a request names.
//
// Scope comes from the query string rather than the path so one route serves
// every scope; the store's own Validate then enforces that the identity fields
// match the scope, which is the same check the database CHECK constraint makes.
func (h *SettingValuesHandler) identityFromRequest(
	w http.ResponseWriter, r *http.Request,
) (userstore.SettingIdentity, bool) {
	key := chi.URLParam(r, "key")
	if strings.TrimSpace(key) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A setting key is required")
		return userstore.SettingIdentity{}, false
	}
	if _, ok := h.definitionFor(w, key); !ok {
		return userstore.SettingIdentity{}, false
	}

	query := r.URL.Query()
	scope := settingscontract.Scope(strings.TrimSpace(query.Get("scope")))
	if scope == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"A scope is required: account, profile, profile_device, profile_library or profile_series")
		return userstore.SettingIdentity{}, false
	}

	identity := userstore.SettingIdentity{Key: key, Scope: scope}

	// Profile and device come from the session headers rather than the query,
	// so one profile cannot write another's settings by naming it.
	if scope != settingscontract.ScopeAccount {
		identity.ProfileID = strings.TrimSpace(apimw.GetProfileID(r.Context()))
		if identity.ProfileID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"X-Profile-Id header is required for this scope")
			return userstore.SettingIdentity{}, false
		}
	}
	if scope == settingscontract.ScopeProfileDevice {
		identity.DeviceID = deviceMetadataFromRequest(r).DeviceID
		if identity.DeviceID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"X-Silo-Device-Id header is required for a device override")
			return userstore.SettingIdentity{}, false
		}
	}
	if scope == settingscontract.ScopeProfileLibrary {
		libraryID, err := strconv.Atoi(strings.TrimSpace(query.Get("library_id")))
		if err != nil || libraryID <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request",
				"library_id is required for a library override")
			return userstore.SettingIdentity{}, false
		}
		identity.LibraryID = libraryID
	}
	if scope == settingscontract.ScopeProfileSeries {
		identity.SeriesID = strings.TrimSpace(query.Get("series_id"))
		if identity.SeriesID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"series_id is required for a series override")
			return userstore.SettingIdentity{}, false
		}
	}

	if err := identity.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return userstore.SettingIdentity{}, false
	}

	// The contract decides where a setting may be written, independently of
	// whether the identity is well formed.
	def, _ := h.contract.Lookup(key)
	if !def.AllowsScope(scope) {
		writeError(w, http.StatusBadRequest, "scope_not_allowed",
			key+" cannot be set at "+string(scope))
		return userstore.SettingIdentity{}, false
	}

	return identity, true
}

func settingValueToResponse(value userstore.SettingValue) settingValueResponse {
	return settingValueResponse{
		Key:       value.Key,
		Scope:     string(value.Scope),
		ProfileID: value.ProfileID,
		DeviceID:  value.DeviceID,
		LibraryID: value.LibraryID,
		SeriesID:  value.SeriesID,
		Value:     value.Value,
		Revision:  value.Revision,
		UpdatedAt: value.UpdatedAt,
	}
}

func effectiveToResponse(eff settingsresolve.Effective) effectiveSettingValueResponse {
	out := effectiveSettingValueResponse{
		Key:         eff.Key,
		Value:       eff.Value,
		Source:      string(eff.Source),
		StoredValue: eff.StoredValue,
		Constrained: eff.Constrained,
	}
	if eff.ConstraintKind != "" {
		out.ConstraintKind = string(eff.ConstraintKind)
	}
	if eff.Identity != nil {
		out.Scope = string(eff.Identity.Scope)
		out.ProfileID = eff.Identity.ProfileID
		out.DeviceID = eff.Identity.DeviceID
		out.LibraryID = eff.Identity.LibraryID
		out.SeriesID = eff.Identity.SeriesID
	}
	return out
}

// hashMutationRequest fingerprints what a mutation id was used for, so a reused
// id carrying different content is a conflict rather than a silent replay of
// the wrong write.
func hashMutationRequest(identity userstore.SettingIdentity, value json.RawMessage) string {
	sum := sha256.New()
	sum.Write([]byte(identity.Key))
	sum.Write([]byte{0})
	sum.Write([]byte(identity.Scope))
	sum.Write([]byte{0})
	sum.Write([]byte(identity.ProfileID))
	sum.Write([]byte{0})
	sum.Write([]byte(identity.DeviceID))
	sum.Write([]byte{0})
	sum.Write([]byte(strconv.Itoa(identity.LibraryID)))
	sum.Write([]byte{0})
	sum.Write([]byte(identity.SeriesID))
	sum.Write([]byte{0})
	sum.Write(value)
	return hex.EncodeToString(sum.Sum(nil))
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// etagMatches handles the comma-separated If-None-Match list, including "*".
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseIntCSV(raw string) []int {
	parts := splitCSV(raw)
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		if value, err := strconv.Atoi(part); err == nil && value > 0 {
			out = append(out, value)
		}
	}
	return out
}

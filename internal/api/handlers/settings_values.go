package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
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
	libraryLookup libraryLookup

	// deviceSeen throttles device-registry refreshes, one upsert per
	// deviceSeenThrottle window per (profile, device) — the same shape the
	// legacy SettingsHandler uses.
	deviceSeen *cache.TTLCache[struct{}]

	// EventsHub, when set, receives a user_settings.changed event after every
	// successful write or delete. Nil (as in tests) simply skips publishing.
	EventsHub *evt.Hub
}

// SetLibraryLookup wires the catalog lookup used to reject profile_library
// identities that do not name a real library. The same lookup serves session
// and admin mutations so the two routes cannot create different orphan rows.
func (h *SettingValuesHandler) SetLibraryLookup(lookup libraryLookup) {
	h.libraryLookup = lookup
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
		deviceSeen:    cache.NewTTLCache[struct{}](),
	}
}

// mutationIDHeader carries the client's idempotency key.
const mutationIDHeader = "X-Silo-Mutation-Id"

// fieldRevision is the response field carrying the contract revision. Clients
// filter definitions, scopes and enum members against it, so every response
// that could be acted on names the revision it was computed at.
const fieldRevision = "revision"

// fieldValues is the response key wrapping a list of stored values.
const fieldValues = "values"

// maxEffectiveContentIDs bounds the combined library_ids and series_ids of one
// effective-values request. SQLite expands each id into a bound parameter, and
// its host-parameter budget is shared with the keys — 999 on older builds — so
// the request boundary keeps a crafted batch from failing resolution outright.
const maxEffectiveContentIDs = 200

const jsonNullLiteral = "null"

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

// explicitSettingValueResponse is one entry from the collection GET. Unset is
// represented explicitly rather than as a 404 so a settings screen can fetch
// several profile defaults or device overrides in one request.
type explicitSettingValueResponse struct {
	Key       string          `json:"key"`
	Scope     string          `json:"scope"`
	ProfileID string          `json:"profile_id,omitempty"`
	DeviceID  string          `json:"device_id,omitempty"`
	LibraryID int             `json:"library_id,omitempty"`
	SeriesID  string          `json:"series_id,omitempty"`
	IsSet     bool            `json:"is_set"`
	Value     json.RawMessage `json:"value,omitempty"`
	Revision  int64           `json:"revision,omitempty"`
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

	RequestedValue  json.RawMessage              `json:"requested_value,omitempty"`
	ConstrainedBy   *settingscontract.Constraint `json:"constrained_by,omitempty"`
	PermittedValues []json.RawMessage            `json:"permitted_values,omitempty"`

	DefinitionRevision int                             `json:"definition_revision"`
	UpdatedAt          string                          `json:"updated_at,omitempty"`
	SourceContext      *effectiveSourceContextResponse `json:"source_context,omitempty"`

	// Scope locates the row the value came from, so a client can offer a reset
	// against exactly that scope. Empty for a contract default.
	Scope     string `json:"scope,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	LibraryID int    `json:"library_id,omitempty"`
	SeriesID  string `json:"series_id,omitempty"`
}

type effectiveSourceContextResponse struct {
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

// HandleGetValues returns the explicit values for several keys at exactly one
// scope. Missing rows remain in the response with is_set false; this is the
// contract's read shape for independently presenting profile defaults and
// device/content overrides.
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
	identity, ok := h.identityForSessionKey(w, r, keys[0])
	if !ok {
		return
	}
	for _, key := range keys[1:] {
		def, exists := h.definitionFor(w, key)
		if !exists {
			return
		}
		if !def.AllowsScope(identity.Scope) {
			writeError(w, http.StatusBadRequest, "scope_not_allowed",
				key+" cannot be set at "+string(identity.Scope))
			return
		}
	}

	query := userstore.SettingResolutionQuery{Keys: keys}
	switch identity.Scope {
	case settingscontract.ScopeProfile:
		query.ProfileIDs = []string{identity.ProfileID}
	case settingscontract.ScopeProfileDevice:
		query.ProfileIDs = []string{identity.ProfileID}
		query.DeviceID = identity.DeviceID
	case settingscontract.ScopeProfileLibrary:
		query.ProfileIDs = []string{identity.ProfileID}
		query.LibraryIDs = []int{identity.LibraryID}
	case settingscontract.ScopeProfileSeries:
		query.ProfileIDs = []string{identity.ProfileID}
		query.SeriesIDs = []string{identity.SeriesID}
	}
	stored, err := store.ListSettingValuesForResolution(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read settings")
		return
	}
	byKey := make(map[string]userstore.SettingValue, len(stored))
	for _, value := range stored {
		if sameSettingContext(value.SettingIdentity, identity) {
			byKey[value.Key] = value
		}
	}

	out := make([]explicitSettingValueResponse, 0, len(keys))
	for _, key := range keys {
		entry := explicitSettingValueResponse{
			Key: key, Scope: string(identity.Scope), ProfileID: identity.ProfileID,
			DeviceID: identity.DeviceID, LibraryID: identity.LibraryID, SeriesID: identity.SeriesID,
		}
		if value, exists := byKey[key]; exists {
			entry.IsSet = true
			entry.Value = value.Value
			entry.Revision = value.Revision
			entry.UpdatedAt = value.UpdatedAt
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		fieldValues:   out,
		fieldRevision: h.contract.Revision,
	})
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
	h.setValueAt(w, r, store, apimw.GetUserID(r.Context()), identity)
}

// setValueAt is the write path shared by the session route and the admin
// route: validation, normalization, idempotency and the change event are one
// implementation regardless of who addresses the store. eventUserID names the
// account whose settings changed — the session owner on the self-service
// route, the target user on the admin route — so change events always reach
// the clients whose settings moved.
func (h *SettingValuesHandler) setValueAt(
	w http.ResponseWriter,
	r *http.Request,
	store userstore.UserStore,
	eventUserID int,
	identity userstore.SettingIdentity,
) {
	def, ok := h.definitionFor(w, identity.Key)
	if !ok {
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
	var requestHash string
	if mutationID != "" {
		requestHash = hashMutationRequest(identity, normalized)
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

	response := settingValueToResponse(*stored)
	if mutationID != "" {
		// Recorded only now, after the write landed: a receipt written for a
		// failed upsert would turn the client's retry of a 500 into a silent
		// 200 replay of a write that never happened. Storing the actual
		// response also makes the replay byte-identical — revision and
		// updated_at included — rather than a reconstruction of the input.
		h.recordMutation(r, store, mutationID, requestHash, response)
	}
	if identity.Scope == settingscontract.ScopeProfileDevice {
		// A device that only ever writes canonically must still appear in
		// ListDevices and the device-management surfaces, or it can never be
		// discovered and forgotten. The legacy device route registers on every
		// touch; the canonical route matches it on device writes.
		h.registerWritingDevice(r, store, identity.ProfileID)
	}
	publishUserSettingsEvent(r.Context(), h.EventsHub,
		eventUserID, identity.ProfileID, identity.Key, string(identity.Scope))
	writeJSON(w, http.StatusOK, response)
}

// registerWritingDevice refreshes the device registry from the request's
// device headers after a successful profile_device write. Best effort and
// throttled: the value write already succeeded, and the registry entry is
// discoverability metadata, not the setting itself.
func (h *SettingValuesHandler) registerWritingDevice(
	r *http.Request, store userstore.UserStore, profileID string,
) {
	device := deviceMetadataFromRequest(r)
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
	if err := registry.RegisterDevice(r.Context(), userstore.DeviceEntry{
		ProfileID:      profileID,
		DeviceID:       device.DeviceID,
		DeviceName:     device.DeviceName,
		DevicePlatform: device.DevicePlatform,
	}); err != nil {
		slog.WarnContext(r.Context(), "failed to register device after canonical write",
			"component", "api",
			"profile_id", profileID,
			"device_id", device.DeviceID,
			"error", err,
		)
	}
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
	h.deleteValueAt(w, r, store, apimw.GetUserID(r.Context()), identity)
}

// deleteValueAt is the unset path shared by the session and admin routes. See
// setValueAt for what eventUserID means.
func (h *SettingValuesHandler) deleteValueAt(
	w http.ResponseWriter,
	r *http.Request,
	store userstore.UserStore,
	eventUserID int,
	identity userstore.SettingIdentity,
) {
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
		eventUserID, identity.ProfileID, identity.Key, string(identity.Scope))
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
	} else {
		// A key this server's contract does not define is an error, not an
		// omission. Dropping it silently lets a client fill the gap with its
		// own vendored default and present a value this server would refuse to
		// store — the same drift the contract exists to remove. The capability
		// endpoint's revision is how a newer client learns to stop asking.
		for _, key := range keys {
			if _, ok := h.contract.Lookup(key); !ok {
				writeError(w, http.StatusNotFound, "unknown_setting",
					"No setting named "+key+" exists in this server's contract")
				return
			}
		}
	}

	rc := settingsresolve.Context{
		ProfileID:  strings.TrimSpace(apimw.GetProfileID(r.Context())),
		DeviceID:   deviceMetadataFromRequest(r).DeviceID,
		LibraryIDs: parseIntCSV(r.URL.Query().Get("library_ids")),
		SeriesIDs:  splitCSV(r.URL.Query().Get("series_ids")),
	}

	// The SQLite backend expands these into IN lists, whose host-parameter
	// budget is finite; an unbounded request could fail the whole resolution.
	// The bound is far above any real batch — a season view resolves a
	// handful of series, not hundreds.
	if len(rc.LibraryIDs)+len(rc.SeriesIDs) > maxEffectiveContentIDs {
		writeError(w, http.StatusBadRequest, "bad_request",
			"Too many library_ids/series_ids in one request; resolve in smaller batches")
		return
	}

	// A device-aware key resolved without a device identity would silently
	// skip every stored device override and pass the profile fallback off as
	// the effective value — a plausible wrong answer. Fail closed instead:
	// the write path already requires the header for device overrides.
	if rc.DeviceID == "" {
		for _, key := range keys {
			if def, ok := h.contract.Lookup(key); ok &&
				def.AllowsScope(settingscontract.ScopeProfileDevice) {
				writeError(w, http.StatusBadRequest, "bad_request",
					"X-Silo-Device-Id header is required to resolve "+key)
				return
			}
		}
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

type effectiveContextRequest struct {
	ContextID string          `json:"context_id"`
	LibraryID json.RawMessage `json:"library_id,omitempty"`
	SeriesID  string          `json:"series_id,omitempty"`
}

// HandlePostEffective resolves several content contexts in one prepared
// candidate read. Each response entry preserves the caller's context_id so a
// client can join results back to an ordered or virtualized content list.
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

	keys := uniqueTrimmed(body.Keys)
	if len(keys) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "keys must contain at least one setting")
		return
	}
	for _, key := range keys {
		if _, ok := h.definitionFor(w, key); !ok {
			return
		}
	}
	if len(body.Contexts) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "contexts must contain at least one content context")
		return
	}
	if len(body.Contexts) > maxEffectiveContentIDs {
		writeError(w, http.StatusBadRequest, "bad_request", "Too many contexts in one request; resolve in smaller batches")
		return
	}

	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	deviceID := deviceMetadataFromRequest(r).DeviceID
	if deviceID == "" {
		for _, key := range keys {
			if def, exists := h.contract.Lookup(key); exists && def.AllowsScope(settingscontract.ScopeProfileDevice) {
				writeError(w, http.StatusBadRequest, "bad_request", "X-Silo-Device-Id header is required to resolve "+key)
				return
			}
		}
	}

	seen := make(map[string]struct{}, len(body.Contexts))
	contexts := make([]settingsresolve.Context, 0, len(body.Contexts))
	contentIDs := 0
	for _, requested := range body.Contexts {
		contextID := strings.TrimSpace(requested.ContextID)
		if contextID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "Every context requires a non-empty context_id")
			return
		}
		if _, exists := seen[contextID]; exists {
			writeError(w, http.StatusBadRequest, "bad_request", "context_id values must be unique")
			return
		}
		seen[contextID] = struct{}{}

		libraryID, err := parseOptionalPositiveJSONInt(requested.LibraryID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "library_id must be a positive integer or numeric string")
			return
		}
		seriesID := strings.TrimSpace(requested.SeriesID)
		if libraryID == 0 && seriesID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "Every context requires library_id or series_id")
			return
		}
		rc := settingsresolve.Context{ProfileID: profileID, DeviceID: deviceID}
		if libraryID > 0 {
			rc.LibraryIDs = []int{libraryID}
			contentIDs++
		}
		if seriesID != "" {
			rc.SeriesIDs = []string{seriesID}
			contentIDs++
		}
		if contentIDs > maxEffectiveContentIDs {
			writeError(w, http.StatusBadRequest, "bad_request", "Too many content ids in one request; resolve in smaller batches")
			return
		}
		contexts = append(contexts, rc)
	}

	resolved, err := h.resolver.ResolveContexts(r.Context(), store, contexts, keys, h.constraintsFor(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve settings")
		return
	}
	type contextResponse struct {
		ContextID string                          `json:"context_id"`
		Settings  []effectiveSettingValueResponse `json:"settings"`
	}
	out := make([]contextResponse, len(resolved))
	for i, values := range resolved {
		out[i].ContextID = strings.TrimSpace(body.Contexts[i].ContextID)
		out[i].Settings = make([]effectiveSettingValueResponse, 0, len(values))
		for _, eff := range values {
			out[i].Settings = append(out[i].Settings, effectiveToResponse(eff))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"contexts":    out,
		fieldRevision: h.contract.Revision,
	})
}

func uniqueTrimmed(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func parseOptionalPositiveJSONInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == jsonNullLiteral {
		return 0, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		value, err := strconv.Atoi(number.String())
		if err == nil && value > 0 {
			return value, nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err == nil && value > 0 {
			return value, nil
		}
	}
	return 0, errors.New("not a positive integer")
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

// recordMutation stores the idempotency receipt after a successful write. The
// receipt is the response the original request returned, so a replay serves
// exactly what the client would have received.
func (h *SettingValuesHandler) recordMutation(
	r *http.Request,
	store userstore.UserStore,
	mutationID, requestHash string,
	response settingValueResponse,
) {
	receipt, _ := json.Marshal(response)
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
	return h.identityForSessionKey(w, r, chi.URLParam(r, "key"))
}

func (h *SettingValuesHandler) identityForSessionKey(
	w http.ResponseWriter, r *http.Request, requestedKey string,
) (userstore.SettingIdentity, bool) {
	key, scope, ok := h.keyedScope(w, requestedKey, r.URL.Query())
	if !ok {
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

	return h.completeIdentity(w, r.Context(), r.URL.Query(), identity)
}

// keyedScopeFromRequest parses the parts every scoped request names: a key
// that exists in the contract and is remote, plus an explicit scope.
func (h *SettingValuesHandler) keyedScopeFromRequest(
	w http.ResponseWriter, r *http.Request,
) (string, settingscontract.Scope, bool) {
	return h.keyedScope(w, chi.URLParam(r, "key"), r.URL.Query())
}

func (h *SettingValuesHandler) keyedScope(
	w http.ResponseWriter, requestedKey string, query url.Values,
) (string, settingscontract.Scope, bool) {
	key := strings.TrimSpace(requestedKey)
	if strings.TrimSpace(key) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A setting key is required")
		return "", "", false
	}
	if _, ok := h.definitionFor(w, key); !ok {
		return "", "", false
	}

	scope := settingscontract.Scope(strings.TrimSpace(query.Get("scope")))
	if scope == "" {
		writeError(w, http.StatusBadRequest, "bad_request",
			"A scope is required: account, profile, profile_device, profile_library or profile_series")
		return "", "", false
	}
	return key, scope, true
}

// completeIdentity fills the content-scope ids from the query, then runs the
// checks the session and admin routes share: the identity matches its scope's
// columns and the contract allows the key at that scope.
func (h *SettingValuesHandler) completeIdentity(
	w http.ResponseWriter, ctx context.Context, query url.Values, identity userstore.SettingIdentity,
) (userstore.SettingIdentity, bool) {
	if identity.Scope == settingscontract.ScopeProfileLibrary {
		libraryID, err := strconv.Atoi(strings.TrimSpace(query.Get("library_id")))
		if err != nil || libraryID <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request",
				"library_id is required for a library override")
			return userstore.SettingIdentity{}, false
		}
		identity.LibraryID = libraryID
		if !h.libraryContextExists(w, ctx, libraryID) {
			return userstore.SettingIdentity{}, false
		}
	}
	if identity.Scope == settingscontract.ScopeProfileSeries {
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
	def, _ := h.contract.Lookup(identity.Key)
	if !def.AllowsScope(identity.Scope) {
		writeError(w, http.StatusBadRequest, "scope_not_allowed",
			identity.Key+" cannot be set at "+string(identity.Scope))
		return userstore.SettingIdentity{}, false
	}

	return identity, true
}

func (h *SettingValuesHandler) libraryContextExists(
	w http.ResponseWriter, ctx context.Context, libraryID int,
) bool {
	if h.libraryLookup == nil {
		return true
	}
	if _, err := h.libraryLookup.GetByID(ctx, libraryID); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to look up library")
		return false
	}
	return true
}

func sameSettingContext(a, b userstore.SettingIdentity) bool {
	return a.Scope == b.Scope && a.ProfileID == b.ProfileID &&
		a.DeviceID == b.DeviceID && a.LibraryID == b.LibraryID && a.SeriesID == b.SeriesID
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
		Key:                eff.Key,
		Value:              eff.Value,
		Source:             string(eff.Source),
		StoredValue:        eff.StoredValue,
		Constrained:        eff.Constrained,
		RequestedValue:     eff.RequestedValue,
		ConstrainedBy:      eff.ConstrainedBy,
		PermittedValues:    eff.PermittedValues,
		DefinitionRevision: eff.DefinitionRevision,
		UpdatedAt:          eff.UpdatedAt,
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
		out.SourceContext = &effectiveSourceContextResponse{
			ProfileID: eff.Identity.ProfileID,
			DeviceID:  eff.Identity.DeviceID,
			LibraryID: eff.Identity.LibraryID,
			SeriesID:  eff.Identity.SeriesID,
		}
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

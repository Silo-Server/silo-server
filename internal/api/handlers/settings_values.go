package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
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
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"golang.org/x/text/language"
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
	storeProvider  userstore.UserStoreProvider
	contract       *settingscontract.Manifest
	resolver       *settingsresolve.Resolver
	libraryLookup  libraryLookup
	languageSource languageSuggestionSource

	// deviceSeen throttles device-registry refreshes, one upsert per
	// deviceSeenThrottle window per (profile, device) — the same shape the
	// legacy SettingsHandler uses.
	deviceSeen *cache.TTLCache[struct{}]

	// EventsHub, when set, receives a user_settings.changed event after every
	// successful write or delete. Nil (as in tests) simply skips publishing.
	EventsHub *evt.Hub

	// UserRepo and ProfileTokens enable household management: a primary profile
	// naming another profile on its own account. Both nil means the widening is
	// simply unavailable — never that it is unguarded.
	UserRepo      userLookup
	ProfileTokens *access.ProfileTokenService
}

// languageSuggestionSource supplies the distinct original_language values the
// accessible catalog actually contains. It decorates catalog.metadata_language
// suggestions only: original_language is a plain indexed scalar column, so the
// listing is a cheap DISTINCT scan. The audio and subtitle pickers deliberately
// do NOT get observed values — their track-derived listings walk every media
// file (tens of seconds on large catalogs), and since those settings are open
// language_tag values, clients offer free entry beyond the contract floor
// instead.
type languageSuggestionSource interface {
	ListOriginalLanguages(context.Context, catalog.BrowseFilters) ([]string, error)
}

// SetLibraryLookup wires the catalog lookup used to reject profile_library
// identities that do not name a real library. The same lookup serves session
// and admin mutations so the two routes cannot create different orphan rows.
func (h *SettingValuesHandler) SetLibraryLookup(lookup libraryLookup) {
	h.libraryLookup = lookup
}

// SetLanguageSuggestionSource wires deployment-observed original languages
// into effective metadata-language responses. The contract option set remains
// the stable floor; a missing source or failed catalog lookup simply returns
// that floor.
func (h *SettingValuesHandler) SetLanguageSuggestionSource(source languageSuggestionSource) {
	h.languageSource = source
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

// maxShortcutMutationRetries bounds contention retries while still making a
// normal burst of edits effectively wait-free for clients. Each failed CAS
// means another writer made progress; exhausting this limit is therefore a
// retryable conflict, never permission to overwrite the newer document.
const maxShortcutMutationRetries = 32

const jsonNullLiteral = "null"

const navigationShortcutAtomicUpdateMessage = "Use PUT /settings/values/nav.shortcuts/item to change navigation shortcuts"

var (
	errMutationIDConflict           = errors.New("setting mutation id conflict")
	errMutationReplayRollback       = errors.New("setting mutation replay requires rollback")
	errMutationTransactionRequired  = errors.New("settings store does not support atomic canonical mutations")
	errShortcutMutationContention   = errors.New("navigation shortcuts changed too quickly")
	errShortcutMutationInvalidValue = errors.New("invalid navigation shortcut value")
)

type idempotentSettingMutationOutcome struct {
	result  json.RawMessage
	stored  *userstore.SettingValue
	replay  bool
	changed bool
}

type shortcutMutationStore interface {
	GetSettingValue(context.Context, userstore.SettingIdentity) (*userstore.SettingValue, error)
	CompareAndSetSettingValue(
		context.Context,
		userstore.SettingIdentity,
		json.RawMessage,
		int64,
	) (*userstore.SettingValue, error)
}

// settingValueResponse is one explicit stored value.
type settingValueResponse struct {
	Key          string          `json:"key"`
	Scope        string          `json:"scope"`
	ProfileID    string          `json:"profile_id,omitempty"`
	ClientFamily string          `json:"client_family,omitempty"`
	DeviceID     string          `json:"device_id,omitempty"`
	LibraryID    int             `json:"library_id,omitempty"`
	SeriesID     string          `json:"series_id,omitempty"`
	Value        json.RawMessage `json:"value"`
	Revision     int64           `json:"revision"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

type navigationShortcutMutationRequest struct {
	Item    json.RawMessage `json:"item"`
	Present *bool           `json:"present"`
}

type navigationShortcutDocument struct {
	Items []navigationShortcutItem `json:"items"`
}

// navigationShortcutItem mirrors navigation-shortcuts.json. LibraryID is a
// pointer because its presence is part of collection identity: a global
// collection and a library-specific collection with the same collection_id
// are different destinations.
type navigationShortcutItem struct {
	Type         string `json:"type"`
	LibraryID    *int   `json:"library_id,omitempty"`
	SectionID    string `json:"section_id,omitempty"`
	CollectionID string `json:"collection_id,omitempty"`
	Label        string `json:"label"`
}

type navigationShortcutIdentity struct {
	Type         string
	LibraryID    int
	HasLibraryID bool
	SectionID    string
	CollectionID string
}

// explicitSettingValueResponse is one entry from the collection GET. Unset is
// represented explicitly rather than as a 404 so a settings screen can fetch
// several profile defaults or device overrides in one request.
type explicitSettingValueResponse struct {
	Key          string          `json:"key"`
	Scope        string          `json:"scope"`
	ProfileID    string          `json:"profile_id,omitempty"`
	ClientFamily string          `json:"client_family,omitempty"`
	DeviceID     string          `json:"device_id,omitempty"`
	LibraryID    int             `json:"library_id,omitempty"`
	SeriesID     string          `json:"series_id,omitempty"`
	IsSet        bool            `json:"is_set"`
	Value        json.RawMessage `json:"value,omitempty"`
	Revision     int64           `json:"revision,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
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
	// SuggestedValues is advisory presentation data for an open setting. It is
	// the stable contract floor plus values observed in this viewer's catalog
	// and the current effective value. It never acts as a write allowlist.
	SuggestedValues []string `json:"suggested_values,omitempty"`

	DefinitionRevision int                             `json:"definition_revision"`
	UpdatedAt          string                          `json:"updated_at,omitempty"`
	SourceContext      *effectiveSourceContextResponse `json:"source_context,omitempty"`

	// Scope locates the row the value came from, so a client can offer a reset
	// against exactly that scope. Empty for a contract default.
	Scope        string `json:"scope,omitempty"`
	ProfileID    string `json:"profile_id,omitempty"`
	ClientFamily string `json:"client_family,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	LibraryID    int    `json:"library_id,omitempty"`
	SeriesID     string `json:"series_id,omitempty"`
}

type effectiveSourceContextResponse struct {
	ProfileID    string `json:"profile_id,omitempty"`
	ClientFamily string `json:"client_family,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	LibraryID    int    `json:"library_id,omitempty"`
	SeriesID     string `json:"series_id,omitempty"`
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
			string(settingscontract.ScopeProfileClient),
			string(settingscontract.ScopeProfileDevice),
			string(settingscontract.ScopeProfileLibrary),
			string(settingscontract.ScopeProfileSeries),
		},
		"client_families": []string{
			string(settingscontract.ClientFamilyTV),
			string(settingscontract.ClientFamilyMobile),
			string(settingscontract.ClientFamilyTablet),
			string(settingscontract.ClientFamilyDesktop),
			string(settingscontract.ClientFamilyWeb),
		},
		"supports_batched_effective": true,
		"supports_idempotent_writes": true,
		"supports_atomic_shortcuts":  true,
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
	case settingscontract.ScopeProfileClient:
		query.ProfileIDs = []string{identity.ProfileID}
		query.ClientFamily = identity.ClientFamily
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
			ClientFamily: string(identity.ClientFamily), DeviceID: identity.DeviceID,
			LibraryID: identity.LibraryID, SeriesID: identity.SeriesID,
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
	if identity.Key == settingskeys.NavShortcuts {
		writeError(w, http.StatusBadRequest, "atomic_update_required",
			navigationShortcutAtomicUpdateMessage)
		return
	}
	h.setValueAt(w, r, store, apimw.GetUserID(r.Context()), identity)
}

// HandleSetNavigationShortcut applies one desired-state edit to the shared
// profile shortcut catalog. Unlike the generic whole-document PUT, two clients
// adding different destinations cannot overwrite one another: the handler
// rebases after an internal compare-and-set conflict until its semantic edit
// lands on the newest document.
func (h *SettingValuesHandler) HandleSetNavigationShortcut(w http.ResponseWriter, r *http.Request) {
	store, ok := h.storeFor(w, r)
	if !ok {
		return
	}
	shortcutStore, ok := store.(shortcutMutationStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error",
			"This settings store does not support atomic shortcut updates")
		return
	}

	profileID := strings.TrimSpace(apimw.GetProfileID(r.Context()))
	identity := userstore.SettingIdentity{
		Key: settingskeys.NavShortcuts, Scope: settingscontract.ScopeProfile, ProfileID: profileID,
	}
	if err := identity.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}
	def, ok := h.definitionFor(w, identity.Key)
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
	if body.Present == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "present is required")
		return
	}

	item, err := normalizeNavigationShortcutItem(def, body.Item)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_value", err.Error())
		return
	}
	requestHash := hashNavigationShortcutMutation(identity, item, *body.Present)
	mutationID := strings.TrimSpace(r.Header.Get(mutationIDHeader))

	var stored *userstore.SettingValue
	var idempotentResult json.RawMessage
	var changed bool
	if mutationID == "" {
		stored, changed, err = mutateNavigationShortcut(
			r.Context(), shortcutStore, def, identity, item, *body.Present)
	} else {
		var outcome idempotentSettingMutationOutcome
		outcome, err = runSettingMutation(
			r.Context(), store, mutationID, requestHash,
			func(writer userstore.SettingMutationWriter) (*userstore.SettingValue, bool, error) {
				return mutateNavigationShortcut(r.Context(), writer, def, identity, item, *body.Present)
			},
		)
		if err == nil && outcome.replay {
			w.Header().Set("X-Silo-Idempotent-Replay", "true")
			writeRawJSON(w, http.StatusOK, outcome.result)
			return
		}
		stored, changed = outcome.stored, outcome.changed
		idempotentResult = outcome.result
	}
	if err != nil {
		switch {
		case errors.Is(err, errMutationIDConflict):
			writeError(w, http.StatusConflict, "mutation_id_conflict",
				"This mutation id was used for a different write")
		case errors.Is(err, errShortcutMutationContention):
			writeError(w, http.StatusConflict, "setting_update_conflict",
				"Navigation shortcuts changed too quickly; retry this mutation")
		case errors.Is(err, errShortcutMutationInvalidValue):
			writeError(w, http.StatusBadRequest, "invalid_value", err.Error())
		case errors.Is(err, userstore.ErrInvalidSettingIdentity),
			errors.Is(err, userstore.ErrInvalidSettingValue):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store navigation shortcuts")
		}
		return
	}

	response := settingValueToResponse(*stored)
	if changed {
		publishUserSettingsEvent(r.Context(), h.EventsHub,
			apimw.GetUserID(r.Context()), identity.ProfileID, identity.Key, string(identity.Scope))
		auditSettingsForOther(r.Context(), settingsAuditRecord{
			Action:          settingsAuditActionSet,
			ActorProfileID:  actingProfileID(r.Context()),
			TargetProfileID: identity.ProfileID,
			TargetUserID:    apimw.GetUserID(r.Context()),
			Key:             identity.Key,
			Scope:           string(identity.Scope),
		})
	}
	if idempotentResult != nil {
		writeRawJSON(w, http.StatusOK, idempotentResult)
	} else {
		writeJSON(w, http.StatusOK, response)
	}
}

func normalizeNavigationShortcutItem(
	def *settingscontract.Definition,
	item json.RawMessage,
) (navigationShortcutItem, error) {
	if len(item) == 0 {
		return navigationShortcutItem{}, errors.New("item is required")
	}
	raw, err := json.Marshal(struct {
		Items []json.RawMessage `json:"items"`
	}{Items: []json.RawMessage{item}})
	if err != nil {
		return navigationShortcutItem{}, fmt.Errorf("encoding shortcut: %w", err)
	}
	normalized, err := def.ValueSchema.NormalizeValue(raw, settingscontract.ObjectSchemas())
	if err != nil {
		return navigationShortcutItem{}, err
	}
	var document navigationShortcutDocument
	if err := json.Unmarshal(normalized, &document); err != nil || len(document.Items) != 1 {
		return navigationShortcutItem{}, errors.New("shortcut did not normalize to one item")
	}
	return document.Items[0], nil
}

func applyNavigationShortcutMutation(
	def *settingscontract.Definition,
	current *userstore.SettingValue,
	item navigationShortcutItem,
	present bool,
) (json.RawMessage, bool, error) {
	document := navigationShortcutDocument{Items: []navigationShortcutItem{}}
	if current != nil {
		if err := json.Unmarshal(current.Value, &document); err != nil {
			return nil, false, fmt.Errorf("decoding stored navigation shortcuts: %w", err)
		}
	}

	target := item.identity()
	match := -1
	for index, candidate := range document.Items {
		if candidate.identity() == target {
			match = index
			break
		}
	}

	if present {
		if match >= 0 {
			if document.Items[match].equal(item) {
				return current.Value, false, nil
			}
			document.Items[match] = item
		} else {
			document.Items = append(document.Items, item)
		}
	} else {
		if match < 0 {
			if current == nil {
				return json.RawMessage(`{"items":[]}`), false, nil
			}
			return current.Value, false, nil
		}
		document.Items = append(document.Items[:match], document.Items[match+1:]...)
	}

	raw, err := json.Marshal(document)
	if err != nil {
		return nil, false, fmt.Errorf("encoding navigation shortcuts: %w", err)
	}
	normalized, err := def.ValueSchema.NormalizeValue(raw, settingscontract.ObjectSchemas())
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", errShortcutMutationInvalidValue, err)
	}
	return normalized, true, nil
}

func (item navigationShortcutItem) identity() navigationShortcutIdentity {
	identity := navigationShortcutIdentity{
		Type: item.Type, SectionID: item.SectionID, CollectionID: item.CollectionID,
	}
	if item.LibraryID != nil {
		identity.LibraryID = *item.LibraryID
		identity.HasLibraryID = true
	}
	return identity
}

func (item navigationShortcutItem) equal(other navigationShortcutItem) bool {
	return item.identity() == other.identity() && item.Label == other.Label
}

func mutateNavigationShortcut(
	ctx context.Context,
	store shortcutMutationStore,
	def *settingscontract.Definition,
	identity userstore.SettingIdentity,
	item navigationShortcutItem,
	present bool,
) (*userstore.SettingValue, bool, error) {
	for attempt := 0; attempt < maxShortcutMutationRetries; attempt++ {
		current, err := store.GetSettingValue(ctx, identity)
		if err != nil {
			return nil, false, fmt.Errorf("reading navigation shortcuts: %w", err)
		}

		next, changed, err := applyNavigationShortcutMutation(def, current, item, present)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			if current != nil {
				return current, false, nil
			}
			return &userstore.SettingValue{
				SettingIdentity: identity,
				Value:           json.RawMessage(`{"items":[]}`),
			}, false, nil
		}

		expectedRevision := int64(0)
		if current != nil {
			expectedRevision = current.Revision
		}
		stored, err := store.CompareAndSetSettingValue(ctx, identity, next, expectedRevision)
		if errors.Is(err, userstore.ErrSettingValueRevisionConflict) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		return stored, true, nil
	}
	return nil, false, errShortcutMutationContention
}

// runSettingMutation applies one canonical mutation inside a store
// transaction, and records its idempotency receipt in that same transaction
// when the caller supplied a mutation id.
//
// Every canonical write goes through here, keyed or not. A mutation is rarely
// one row any more — a mirrored pair writes two, and a profile-scope intro-skip
// choice also moves the legacy column GET /profiles serves — and a caller
// without an idempotency key has exactly the same claim to those landing
// together as one with. Worse, it has less recourse: a keyed write that half
// fails is repaired by the retry, while two unkeyed writes to one preference
// can interleave and leave the pair permanently disagreeing.
func runSettingMutation(
	ctx context.Context,
	store userstore.UserStore,
	mutationID string,
	requestHash string,
	mutate func(userstore.SettingMutationWriter) (*userstore.SettingValue, bool, error),
) (idempotentSettingMutationOutcome, error) {
	transactioner, ok := store.(userstore.SettingMutationTransactioner)
	if !ok {
		return idempotentSettingMutationOutcome{}, errMutationTransactionRequired
	}

	var outcome idempotentSettingMutationOutcome
	err := transactioner.WithSettingMutationTransaction(ctx, mutationID,
		func(writer userstore.SettingMutationWriter) error {
			if mutationID == "" {
				stored, changed, err := mutate(writer)
				if err != nil {
					return err
				}
				outcome.stored = stored
				outcome.changed = changed
				return nil
			}

			prior, err := writer.GetSettingMutation(ctx, mutationID)
			if err != nil {
				return fmt.Errorf("checking setting mutation: %w", err)
			}
			if prior != nil {
				if prior.RequestHash != requestHash {
					return errMutationIDConflict
				}
				outcome.result = slices.Clone(prior.Result)
				outcome.replay = true
				return nil
			}

			stored, changed, err := mutate(writer)
			if err != nil {
				return err
			}
			response := settingValueToResponse(*stored)
			result, err := json.Marshal(response)
			if err != nil {
				return fmt.Errorf("encoding setting mutation receipt: %w", err)
			}
			record, inserted, err := writer.PutSettingMutation(ctx, userstore.SettingMutationRecord{
				MutationID:  mutationID,
				RequestHash: requestHash,
				Result:      result,
				ExpiresAt:   time.Now().UTC().Add(30 * 24 * time.Hour),
			})
			if err != nil {
				return fmt.Errorf("recording setting mutation: %w", err)
			}
			if !inserted {
				if record.RequestHash != requestHash {
					return errMutationIDConflict
				}
				outcome.result = slices.Clone(record.Result)
				outcome.replay = true
				// A legacy writer could have inserted between the initial read and
				// this insert. Roll back our setting write before serving its receipt.
				return errMutationReplayRollback
			}

			outcome.result = slices.Clone(record.Result)
			outcome.stored = stored
			outcome.changed = changed
			return nil
		})
	if errors.Is(err, errMutationReplayRollback) {
		return outcome, nil
	}
	return outcome, err
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

	// A deprecated key and its replacement are one preference stored twice
	// while old clients are in the field, so a write to either lands on both.
	// The value is already normalized, so a conversion failure here is a defect
	// in the pairing rather than bad input — refuse the request rather than
	// store half of it.
	mirror, hasMirror, err := settingscontract.MirrorWrite(identity.Key, normalized)
	if err != nil {
		slog.ErrorContext(r.Context(), "settings mirror could not convert a normalized value",
			"component", "api", "key", identity.Key, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store the setting")
		return
	}
	mirrorIdentity := identity
	mirrorIdentity.Key = mirror.Key

	// GET /profiles still serves auto_skip_intro from the legacy column, so a
	// profile-scope choice made through either half of the pair has to reach it
	// or the profile DTO keeps reporting the preference the household abandoned.
	columnValue := legacyIntroSkipColumnWrite(identity.Key, normalized, mirror, hasMirror)

	// Idempotency: a client that retries a write after a dropped response must
	// not double-apply it, and must be able to tell "already done" from "that
	// id means something else". The mutation itself is transactional either way
	// — see runSettingMutation.
	mutationID := strings.TrimSpace(r.Header.Get(mutationIDHeader))
	outcome, err := runSettingMutation(
		r.Context(), store, mutationID, hashMutationRequest(identity, normalized),
		func(writer userstore.SettingMutationWriter) (*userstore.SettingValue, bool, error) {
			value, err := upsertMirroredPair(
				r.Context(), writer, identity, normalized, mirrorIdentity, mirror, hasMirror)
			if err != nil {
				return nil, false, err
			}
			if err := writeLegacyIntroSkipColumn(r.Context(), writer, identity, columnValue); err != nil {
				return nil, false, err
			}
			return value, true, nil
		},
	)
	if errors.Is(err, errMutationIDConflict) {
		writeError(w, http.StatusConflict, "mutation_id_conflict",
			"This mutation id was used for a different write")
		return
	}
	if err != nil {
		if errors.Is(err, userstore.ErrInvalidSettingIdentity) ||
			errors.Is(err, userstore.ErrInvalidSettingValue) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "failed to store a canonical setting",
			"component", "api", "key", identity.Key, "scope", string(identity.Scope), "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store the setting")
		return
	}
	if outcome.replay {
		w.Header().Set("X-Silo-Idempotent-Replay", "true")
		writeRawJSON(w, http.StatusOK, outcome.result)
		return
	}
	stored := outcome.stored
	idempotentResult := outcome.result

	response := settingValueToResponse(*stored)
	acting := actingProfileID(r.Context())
	if identity.Scope == settingscontract.ScopeProfileDevice {
		// A device that only ever writes canonically must still appear in
		// ListDevices and the device-management surfaces, or it can never be
		// discovered and forgotten. The legacy device route registers on every
		// touch; the canonical route matches it on device writes.
		//
		// Only when the caller is writing its own device for its own profile,
		// though. Registration asserts "this device is in use by this profile",
		// which a write aimed at another device — or made on another profile's
		// behalf — is not. Registering here would invent a device nobody holds:
		// the parent's browser filed under the child's profile.
		if identity.DeviceID == deviceMetadataFromRequest(r).DeviceID && identity.ProfileID == acting {
			h.registerWritingDevice(r, store, identity.ProfileID)
		}
	}
	publishUserSettingsEvent(r.Context(), h.EventsHub,
		eventUserID, identity.ProfileID, identity.Key, string(identity.Scope))
	auditSettingsForOther(r.Context(), settingsAuditRecord{
		Action:          settingsAuditActionSet,
		ActorProfileID:  acting,
		TargetProfileID: identity.ProfileID,
		TargetUserID:    eventUserID,
		ClientFamily:    string(identity.ClientFamily),
		DeviceID:        identity.DeviceID,
		Key:             identity.Key,
		Scope:           string(identity.Scope),
	})
	if idempotentResult != nil {
		writeRawJSON(w, http.StatusOK, idempotentResult)
	} else {
		writeJSON(w, http.StatusOK, response)
	}
}

// upsertMirroredPair writes the addressed row and, when the key has one, its
// companion, returning the row the caller addressed.
//
// The two rows are written in key order rather than caller order, and that is
// the whole point of the function. Two requests naming opposite halves of the
// pair would otherwise take each other's row locks in opposite orders and
// deadlock; in one order the second transaction simply waits for the first and
// then overwrites both rows with its own answer, which is the last-write-wins
// the contract promises rather than a pair left holding one value from each.
func upsertMirroredPair(
	ctx context.Context,
	writer userstore.SettingMutationWriter,
	identity userstore.SettingIdentity,
	value json.RawMessage,
	mirrorIdentity userstore.SettingIdentity,
	mirror settingscontract.MirroredWrite,
	hasMirror bool,
) (*userstore.SettingValue, error) {
	if !hasMirror {
		return writer.UpsertSettingValue(ctx, identity, value)
	}

	first, firstValue := identity, value
	second, secondValue := mirrorIdentity, mirror.Value
	if second.Key < first.Key {
		first, firstValue, second, secondValue = second, secondValue, first, firstValue
	}
	firstStored, err := writer.UpsertSettingValue(ctx, first, firstValue)
	if err != nil {
		return nil, err
	}
	secondStored, err := writer.UpsertSettingValue(ctx, second, secondValue)
	if err != nil {
		return nil, err
	}
	if first.Key == identity.Key {
		return firstStored, nil
	}
	return secondStored, nil
}

// legacyIntroSkipColumnWrite is the playback.auto_skip_intro value a canonical
// write implies, or nil when the write is not part of the intro-skip pair.
//
// Either half answers, because the mirror has already made them one preference:
// the boolean is its own answer, and the enum's is the companion just computed
// for it. Both have to answer, or the column would be movable in one direction
// only — an enum write could set it and the boolean write that followed could
// not correct it.
func legacyIntroSkipColumnWrite(
	key string,
	normalized json.RawMessage,
	mirror settingscontract.MirroredWrite,
	hasMirror bool,
) json.RawMessage {
	if !hasMirror {
		return nil
	}
	switch key {
	case settingskeys.PlaybackAutoSkipIntro:
		return normalized
	case settingskeys.PlaybackIntroSkipMode:
		return mirror.Value
	default:
		return nil
	}
}

// legacyIntroSkipColumnCleared is the playback.auto_skip_intro value a cleared
// profile-scope row leaves behind, or nil when the cleared key is not part of
// the intro-skip pair.
//
// Clearing the profile-scope row is how a household says "inherit again", so
// what the column must now hold is what the key resolves to with no explicit
// value: the contract default. It is read from the manifest rather than spelled
// here so a changed default cannot leave the column behind.
func (h *SettingValuesHandler) legacyIntroSkipColumnCleared(key string) json.RawMessage {
	switch key {
	case settingskeys.PlaybackAutoSkipIntro, settingskeys.PlaybackIntroSkipMode:
	default:
		return nil
	}
	def, ok := h.contract.Lookup(settingskeys.PlaybackAutoSkipIntro)
	if !ok || len(def.DefaultValue) == 0 {
		return nil
	}
	return def.DefaultValue
}

// writeLegacyIntroSkipColumn writes the intro-skip preference through to
// user_profiles.auto_skip_intro inside the caller's transaction. A nil value
// means this request has nothing to say about the column.
//
// GET /profiles still serves auto_skip_intro from its column — the profile
// DTO's shape is pinned by shipped clients — so the column is a third copy of
// one preference and has to track every canonical profile-scope change to
// either half of the pair, set or clear alike. A column that only the enum
// write could move would end up contradicting the very row the caller stored:
// an enum write sets it true, and a later boolean write, or a DELETE that goes
// back to inheriting, leaves the DTO reporting a choice nobody holds.
//
// Device scope is none of its business: the column is profile-wide, and one
// television's override is not the household's choice. The canonical write path
// touches no other legacy preference column — the cutover direction is that the
// profile DTO stops reading them, which it already has for the language and
// subtitle fields — so this stays the narrow repair for the one field still
// served from its column.
//
// It runs inside the mutation's transaction, not after it. Outside, a failure
// here would leave the rows committed and the column stale — and on the
// idempotent path unrepairable, because the receipt is already recorded and a
// retry replays it instead of trying the column again.
func writeLegacyIntroSkipColumn(
	ctx context.Context,
	writer userstore.SettingMutationWriter,
	identity userstore.SettingIdentity,
	value json.RawMessage,
) error {
	if value == nil || identity.Scope != settingscontract.ScopeProfile {
		return nil
	}

	var enabled bool
	if err := json.Unmarshal(value, &enabled); err != nil {
		return fmt.Errorf("%s produced a non-boolean auto_skip_intro (%s): %w",
			identity.Key, value, err)
	}
	if err := writer.UpdateProfile(ctx, identity.ProfileID,
		userstore.UpdateProfileInput{AutoSkipIntro: &enabled}); err != nil {
		return fmt.Errorf("updating auto_skip_intro on profile %s: %w", identity.ProfileID, err)
	}
	return nil
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
	if identity.Key == settingskeys.NavShortcuts {
		writeError(w, http.StatusBadRequest, "atomic_update_required",
			navigationShortcutAtomicUpdateMessage)
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
	// One transaction for the whole clear. Clearing one half of a mirrored pair
	// clears both — a surviving companion row would go on resolving as an
	// explicit choice at a scope the caller just said it wanted to inherit at —
	// and a profile-scope clear also returns the legacy column to the contract
	// default, so GET /profiles stops serving the value this request cleared.
	// Committing those separately is what leaves a client retrying against a 404
	// while the companion it could not see keeps resolving.
	transactioner, ok := store.(userstore.SettingMutationTransactioner)
	if !ok {
		slog.ErrorContext(r.Context(), "settings store cannot clear a value atomically",
			"component", "api", "key", identity.Key)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear the setting")
		return
	}
	mirrorKey, hasMirror := settingscontract.MirrorKey(identity.Key)
	clearedColumn := h.legacyIntroSkipColumnCleared(identity.Key)

	var removed, mirrorRemoved bool
	err := transactioner.WithSettingMutationTransaction(r.Context(), "",
		func(writer userstore.SettingMutationWriter) error {
			var err error
			if removed, err = writer.DeleteSettingValue(r.Context(), identity); err != nil {
				return err
			}
			if hasMirror {
				mirrorIdentity := identity
				mirrorIdentity.Key = mirrorKey
				// Cleared even when the addressed row was already absent. That
				// state is only reachable through a partial failure from before
				// this path was transactional, and leaving the survivor behind
				// would make it permanent: every retry answers 404 without ever
				// looking at the row that is still resolving.
				if mirrorRemoved, err = writer.DeleteSettingValue(r.Context(), mirrorIdentity); err != nil {
					return err
				}
			}
			if !removed && !mirrorRemoved {
				return nil // nothing was stored here, so nothing to reconcile
			}
			return writeLegacyIntroSkipColumn(r.Context(), writer, identity, clearedColumn)
		})
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to clear a canonical setting",
			"component", "api", "key", identity.Key, "scope", string(identity.Scope), "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear the setting")
		return
	}
	if !removed {
		// 404 stays the answer for "no value is set at this scope", which is
		// what the caller addressed. A stray companion that was swept up on the
		// way is a repair, not a result, so it is logged and published rather
		// than turned into a success the caller did not ask for.
		if mirrorRemoved {
			slog.WarnContext(r.Context(), "cleared a mirrored setting whose addressed half was already gone",
				"component", "api", "key", identity.Key, "mirror_key", mirrorKey,
				"scope", string(identity.Scope))
			publishUserSettingsEvent(r.Context(), h.EventsHub,
				eventUserID, identity.ProfileID, mirrorKey, string(identity.Scope))
		}
		writeError(w, http.StatusNotFound, "not_found", "No value is set at this scope")
		return
	}
	auditSettingsForOther(r.Context(), settingsAuditRecord{
		Action:          "clear",
		ActorProfileID:  actingProfileID(r.Context()),
		TargetProfileID: identity.ProfileID,
		TargetUserID:    eventUserID,
		ClientFamily:    string(identity.ClientFamily),
		DeviceID:        identity.DeviceID,
		Key:             identity.Key,
		Scope:           string(identity.Scope),
	})
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
	if family, needed, ok := h.clientFamilyForKeys(w, r, keys); !ok {
		return
	} else if needed {
		rc.ClientFamily = family
	}

	// A device-settings screen resolves what some *other* device sees, so this
	// read accepts the same explicit identity the write path does, under the
	// same guards: the device must belong to the profile, and naming another
	// profile requires the household parent.
	if named := strings.TrimSpace(r.URL.Query().Get("profile_id")); named != "" && named != rc.ProfileID {
		if !h.mayActForProfile(w, r, named) {
			return
		}
		rc.ProfileID = named
	}
	if named := strings.TrimSpace(r.URL.Query().Get("device_id")); named != "" {
		if !h.deviceBelongsToProfile(w, r, rc.ProfileID, named) {
			return
		}
		rc.DeviceID = named
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

	out := h.effectiveResponses(r, resolved)
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
	clientFamily, familyNeeded, ok := h.clientFamilyForKeys(w, r, keys)
	if !ok {
		return
	}
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
		if familyNeeded {
			rc.ClientFamily = clientFamily
		}
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
	allResolved := make([]settingsresolve.Effective, 0)
	for _, values := range resolved {
		allResolved = append(allResolved, values...)
	}
	observed := h.observedLanguageSuggestions(r, allResolved)
	out := make([]contextResponse, len(resolved))
	for i, values := range resolved {
		out[i].ContextID = strings.TrimSpace(body.Contexts[i].ContextID)
		out[i].Settings = h.effectiveResponsesWithObserved(values, observed)
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

// clientFamilyForKeys validates an optional family header for effective reads.
// An absent header deliberately drops the profile_client layer so pre-revision
// 5 callers keep resolving broader fallbacks; explicit profile_client reads and
// writes still require the header in identityForSessionKey. The server never
// guesses this identity from X-Silo-Device-Platform: that header is free-form
// display metadata, while client_family is a closed storage key shared by like
// clients.
func (h *SettingValuesHandler) clientFamilyForKeys(
	w http.ResponseWriter, r *http.Request, keys []string,
) (settingscontract.ClientFamily, bool, bool) {
	eligible := false
	for _, key := range keys {
		if def, ok := h.contract.Lookup(key); ok && def.AllowsScope(settingscontract.ScopeProfileClient) {
			eligible = true
			break
		}
	}

	value := strings.TrimSpace(r.Header.Get(clientFamilyHeader))
	if value == "" {
		return "", false, true
	}
	family := settingscontract.ClientFamily(value)
	if !family.Valid() {
		writeError(w, http.StatusBadRequest, "bad_request",
			"X-Silo-Client-Family header must be one of tv, mobile, tablet, desktop or web")
		return "", false, false
	}
	return family, eligible, true
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

	// The profile defaults to the session header, so an ordinary caller cannot
	// write another's settings by naming it. A household parent may name a
	// different profile on their own account — authorized below.
	if scope != settingscontract.ScopeAccount {
		identity.ProfileID = strings.TrimSpace(apimw.GetProfileID(r.Context()))
		if identity.ProfileID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"X-Profile-Id header is required for this scope")
			return userstore.SettingIdentity{}, false
		}
		if named := strings.TrimSpace(r.URL.Query().Get("profile_id")); named != "" &&
			named != identity.ProfileID {
			if !h.mayActForProfile(w, r, named) {
				return userstore.SettingIdentity{}, false
			}
			identity.ProfileID = named
		}
	}
	if scope == settingscontract.ScopeProfileClient {
		family := settingscontract.ClientFamily(strings.TrimSpace(r.Header.Get(clientFamilyHeader)))
		if !family.Valid() {
			writeError(w, http.StatusBadRequest, "bad_request",
				"X-Silo-Client-Family header must be one of tv, mobile, tablet, desktop or web")
			return userstore.SettingIdentity{}, false
		}
		identity.ClientFamily = family
	}
	if scope == settingscontract.ScopeProfileDevice {
		// A device may be named explicitly so one device can manage another's
		// settings — the screen that lists your devices edits them in place.
		// Unlike the profile above, that is safe to accept from the query only
		// because the device is then checked against this profile's registry.
		named := strings.TrimSpace(r.URL.Query().Get("device_id"))
		identity.DeviceID = named
		if identity.DeviceID == "" {
			identity.DeviceID = deviceMetadataFromRequest(r).DeviceID
		}
		if identity.DeviceID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"X-Silo-Device-Id header is required for a device override")
			return userstore.SettingIdentity{}, false
		}
		if named != "" && !h.deviceBelongsToProfile(w, r, identity.ProfileID, named) {
			return userstore.SettingIdentity{}, false
		}
	}

	return h.completeIdentity(w, r.Context(), r.URL.Query(), identity)
}

// mayActForProfile authorizes acting for a profile other than the caller's own.
//
// Two checks, in this order and for different reasons. First the household
// guard: only the primary profile (or a server admin) manages the household, so
// an ordinary member naming a sibling is 403 — the profile plainly exists, and
// pretending otherwise would be a lie the caller can already disprove through
// GET /profiles. Then existence, resolved through the caller's *own* user
// store, which is what confines this to one account: a profile id from another
// account is simply absent there, so it is 404 and the caller learns nothing.
func (h *SettingValuesHandler) mayActForProfile(
	w http.ResponseWriter, r *http.Request, profileID string,
) bool {
	store, ok := h.storeFor(w, r)
	if !ok {
		return false
	}

	allowed, err := canManageHousehold(r, store, h.UserRepo, h.ProfileTokens)
	if err != nil {
		writeProfileManagementPermissionError(w, err)
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden",
			"Managing another profile's settings requires the primary profile or admin access")
		return false
	}

	profile, err := store.GetProfile(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load profile")
		return false
	}
	if profile == nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return false
	}
	return true
}

// deviceBelongsToProfile authorizes a device id that came from the query rather
// than from this request's own header. It answers 404 rather than 403 for an
// unknown device: a 403 would confirm the id exists somewhere.
//
// The caller's own header device is deliberately not checked. Registration is
// lazy — a device's first write is what registers it — so requiring a row there
// would reject every new device's first setting.
func (h *SettingValuesHandler) deviceBelongsToProfile(
	w http.ResponseWriter, r *http.Request, profileID, deviceID string,
) bool {
	store, ok := h.storeFor(w, r)
	if !ok {
		return false
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Device not found")
		return false
	}
	exists, err := registry.DeviceExists(r.Context(), profileID, deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to look up device")
		return false
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "Device not found")
		return false
	}
	return true
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
			"A scope is required: account, profile, profile_client, profile_device, profile_library or profile_series")
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
		a.ClientFamily == b.ClientFamily && a.DeviceID == b.DeviceID &&
		a.LibraryID == b.LibraryID && a.SeriesID == b.SeriesID
}

func settingValueToResponse(value userstore.SettingValue) settingValueResponse {
	return settingValueResponse{
		Key:          value.Key,
		Scope:        string(value.Scope),
		ProfileID:    value.ProfileID,
		ClientFamily: string(value.ClientFamily),
		DeviceID:     value.DeviceID,
		LibraryID:    value.LibraryID,
		SeriesID:     value.SeriesID,
		Value:        value.Value,
		Revision:     value.Revision,
		UpdatedAt:    value.UpdatedAt,
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
		out.ClientFamily = string(eff.Identity.ClientFamily)
		out.DeviceID = eff.Identity.DeviceID
		out.LibraryID = eff.Identity.LibraryID
		out.SeriesID = eff.Identity.SeriesID
		out.SourceContext = &effectiveSourceContextResponse{
			ProfileID:    eff.Identity.ProfileID,
			ClientFamily: string(eff.Identity.ClientFamily),
			DeviceID:     eff.Identity.DeviceID,
			LibraryID:    eff.Identity.LibraryID,
			SeriesID:     eff.Identity.SeriesID,
		}
	}
	return out
}

func (h *SettingValuesHandler) effectiveResponses(
	r *http.Request,
	resolved []settingsresolve.Effective,
) []effectiveSettingValueResponse {
	observed := h.observedLanguageSuggestions(r, resolved)
	return h.effectiveResponsesWithObserved(resolved, observed)
}

func (h *SettingValuesHandler) effectiveResponsesWithObserved(
	resolved []settingsresolve.Effective,
	observed map[string][]string,
) []effectiveSettingValueResponse {
	out := make([]effectiveSettingValueResponse, 0, len(resolved))
	for _, eff := range resolved {
		response := effectiveToResponse(eff)
		def, ok := h.contract.Lookup(eff.Key)
		if ok && def.SuggestedOptions != "" {
			optionSet := h.contract.OptionSets[def.SuggestedOptions]
			floor := make([]string, 0, len(optionSet.Options))
			for _, option := range optionSet.OptionsAtRevision(h.contract.Revision) {
				floor = append(floor, option.Value)
			}
			response.SuggestedValues = mergeLanguageSuggestions(
				floor, observed[eff.Key], eff.Value,
			)
		}
		out = append(out, response)
	}
	return out
}

func (h *SettingValuesHandler) observedLanguageSuggestions(
	r *http.Request,
	resolved []settingsresolve.Effective,
) map[string][]string {
	result := make(map[string][]string)
	if h.languageSource == nil {
		return result
	}
	if !slices.ContainsFunc(resolved, func(eff settingsresolve.Effective) bool {
		return eff.Key == settingskeys.CatalogMetadataLanguage
	}) {
		return result
	}

	filters := catalog.BrowseFilters{}
	if scope, ok := access.GetScope(r.Context()); ok {
		filters.LibraryIDs = scope.AllowedLibraryIDs
		filters.DisabledLibraryIDs = scope.DisabledLibraryIDs
		filters.MaxContentRating = scope.MaxContentRating
	}
	values, err := h.languageSource.ListOriginalLanguages(r.Context(), filters)
	if err != nil {
		slog.WarnContext(r.Context(), "settings: listing metadata language suggestions",
			"component", "settings", "error", err)
		return result
	}
	result[settingskeys.CatalogMetadataLanguage] = values
	return result
}

// mergeLanguageSuggestions keeps the contract's stable authored order, then
// appends deployment-observed languages. Semantic aliases such as eng/en are
// deduplicated through the catalog's ISO canonicalizer. When the current
// stored value is one of those aliases it replaces the row's wire value so a
// picker always has an exact selectable tag for its current selection.
func mergeLanguageSuggestions(
	floor []string,
	observed []string,
	current json.RawMessage,
) []string {
	values := make([]string, 0, len(floor)+len(observed)+1)
	indexByLanguage := make(map[string]int, cap(values))
	appendValue := func(value string, replace bool) {
		normalized, ok := settingscontract.NormalizeLanguageTag(value)
		if !ok {
			return
		}
		identity := normalized
		if tag, err := language.Parse(normalized); err == nil {
			// x/text collapses true ISO aliases (eng/en) while retaining
			// meaningful script and region specificity (pt/pt-BR).
			identity = tag.String()
		}
		if index, exists := indexByLanguage[identity]; exists {
			if replace {
				values[index] = normalized
			}
			return
		}
		indexByLanguage[identity] = len(values)
		values = append(values, normalized)
	}

	for _, value := range floor {
		appendValue(value, false)
	}
	for _, value := range observed {
		appendValue(value, false)
	}
	var currentValue string
	if err := json.Unmarshal(current, &currentValue); err == nil {
		appendValue(currentValue, true)
	}
	return values
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
	// Preserve the established fingerprint for the five pre-existing scopes so
	// an in-flight retry made across this server upgrade still replays. Only the
	// new profile_client identity appends a family discriminator.
	if identity.ClientFamily != "" {
		sum.Write([]byte{0})
		sum.Write([]byte(identity.ClientFamily))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func hashNavigationShortcutMutation(
	identity userstore.SettingIdentity,
	item navigationShortcutItem,
	present bool,
) string {
	// Removing a destination ignores presentation fields, so the fingerprint
	// does too. Reusing one mutation id for the same remove with a refreshed
	// label is the same operation; adding includes the label because it can
	// update that field in place.
	if !present {
		item.Label = ""
	}
	canonical, _ := json.Marshal(struct {
		Operation string                 `json:"operation"`
		Item      navigationShortcutItem `json:"item"`
		Present   bool                   `json:"present"`
	}{Operation: "set_navigation_shortcut_presence", Item: item, Present: present})
	return hashMutationRequest(identity, canonical)
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

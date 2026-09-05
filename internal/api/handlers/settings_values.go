package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

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

	if match := r.Header.Get("If-None-Match"); match != "" && ETagMatches(match, etag) {
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

// SettingsCapabilitiesView is the settings capability document. Field order
// is alphabetical on purpose: v1 encoded a map, whose keys sort, and the v1
// bytes must not change.
type SettingsCapabilitiesView struct {
	APIVersion               int      `json:"api_version"`
	ClientFamilies           []string `json:"client_families"`
	ContractETag             string   `json:"contract_etag"`
	DefinitionCount          int      `json:"definition_count"`
	Revision                 int      `json:"revision"`
	Scopes                   []string `json:"scopes"`
	SupportsAtomicShortcuts  bool     `json:"supports_atomic_shortcuts"`
	SupportsBatchedEffective bool     `json:"supports_batched_effective"`
	SupportsIdempotentWrites bool     `json:"supports_idempotent_writes"`
}

// SettingsCapabilities builds the capability document for a manifest. v1
// GET /settings/contract/capabilities and v2 getSettingsContractCapabilities
// both answer from it.
func SettingsCapabilities(contract *settingscontract.Manifest) (SettingsCapabilitiesView, error) {
	etag, err := settingscontract.PublicETag()
	if err != nil {
		return SettingsCapabilitiesView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to read the settings contract")
	}
	return SettingsCapabilitiesView{
		APIVersion:      contract.APIVersion,
		Revision:        contract.Revision,
		ContractETag:    etag,
		DefinitionCount: len(contract.Definitions),
		Scopes: []string{
			string(settingscontract.ScopeAccount),
			string(settingscontract.ScopeProfile),
			string(settingscontract.ScopeProfileClient),
			string(settingscontract.ScopeProfileDevice),
			string(settingscontract.ScopeProfileLibrary),
			string(settingscontract.ScopeProfileSeries),
		},
		ClientFamilies: []string{
			string(settingscontract.ClientFamilyTV),
			string(settingscontract.ClientFamilyMobile),
			string(settingscontract.ClientFamilyTablet),
			string(settingscontract.ClientFamilyDesktop),
			string(settingscontract.ClientFamilyWeb),
		},
		SupportsBatchedEffective: true,
		SupportsIdempotentWrites: true,
		SupportsAtomicShortcuts:  true,
	}, nil
}

// HandleGetCapabilities reports what this server supports, for feature
// detection rather than version sniffing.
func (h *SettingValuesHandler) HandleGetCapabilities(w http.ResponseWriter, r *http.Request) {
	view, err := SettingsCapabilities(h.contract)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
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

func (h *SettingValuesHandler) storeFor(w http.ResponseWriter, r *http.Request) (userstore.UserStore, bool) {
	store, err := h.storeProvider.ForUser(r.Context(), apimw.GetUserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return nil, false
	}
	return store, true
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

// ETagMatches handles the comma-separated If-None-Match list, including "*".
func ETagMatches(header, etag string) bool {
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

// Capabilities is the settings capability document for this server's
// manifest; v2 getSettingsContractCapabilities calls it.
func (h *SettingValuesHandler) Capabilities(context.Context) (SettingsCapabilitiesView, error) {
	return SettingsCapabilities(h.contract)
}

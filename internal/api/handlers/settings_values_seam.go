package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// This file is the request-free core of the settings values API. The v1
// routes in settings_values.go parse the request, call in here, and render
// the *APIError they get back with the same status, code and message they
// always wrote; the v2 operations call the exported methods at the bottom and
// render the same decisions as Problem Details. Field on a returned error
// names the request member at fault: "key", "keys", "scope", "profile_id",
// "profile_header", "device_id", "device_header", "client_family",
// "library_id", "series_id", "value", "item", "present" or "contexts".

const (
	settingFieldKey           = "key"
	settingFieldKeys          = "keys"
	settingFieldScope         = "scope"
	settingFieldProfileID     = "profile_id"
	settingFieldProfileHeader = "profile_header"
	settingFieldDeviceID      = "device_id"
	settingFieldDeviceHeader  = "device_header"
	settingFieldClientFamily  = "client_family"
	settingFieldLibraryID     = "library_id"
	settingFieldSeriesID      = "series_id"
	settingFieldValue         = "value"
	settingFieldItem          = "item"
	settingFieldPresent       = "present"
	settingFieldContexts      = "contexts"

	settingErrorUnknown        = "unknown_setting"
	settingErrorClientLocal    = "client_local_setting"
	settingErrorScopeDenied    = "scope_not_allowed"
	settingErrorInvalidValue   = "invalid_value"
	settingErrorAtomicRequired = "atomic_update_required"
	settingErrorMutationID     = "mutation_id_conflict"
	settingErrorUpdateConflict = "setting_update_conflict"
	settingErrorForbidden      = "forbidden"
)

// SettingValueView, ExplicitSettingValueView, EffectiveSettingValueView and
// EffectiveSettingContextView are the read models the seam returns; the v1
// routes serialize them directly, so their JSON tags are v1's wire shape.
type (
	SettingValueView            = settingValueResponse
	ExplicitSettingValueView    = explicitSettingValueResponse
	EffectiveSettingValueView   = effectiveSettingValueResponse
	EffectiveSettingContextView = effectiveContextResponse
	// EffectiveSourceContextView locates the content context an effective
	// value was resolved for.
	EffectiveSourceContextView = effectiveSourceContextResponse
	// EffectiveContextRequest is one content context of a batched resolve.
	EffectiveContextRequest = effectiveContextRequest
)

// SettingIdentityRequest is everything a request says about which stored
// value it addresses: the key and scope, the session profile, the optional
// explicitly named profile and device, the declared device and client
// family, and the content ids. VerifyProfile answers whether a PIN-locked
// primary profile has been verified for this request.
type SettingIdentityRequest struct {
	Key   string
	Scope string
	// ActiveProfileID is the profile the caller is signed in as.
	ActiveProfileID string
	// ProfileID names another profile on the account to act for.
	ProfileID string
	// DeviceID names another registered device of the profile.
	DeviceID string
	// Device is the device the client declared in its headers.
	Device       DeviceMetadata
	ClientFamily string
	LibraryID    string
	SeriesID     string
	// VerifyProfile confirms a PIN-locked primary profile; nil never verifies.
	VerifyProfile func(profileID string) error
}

// EffectiveSettingsQuery is one effective resolution request. Keys empty
// means every remote definition.
type EffectiveSettingsQuery struct {
	Keys            []string
	ActiveProfileID string
	ProfileID       string
	DeviceID        string
	Device          DeviceMetadata
	ClientFamily    string
	LibraryIDs      []int
	SeriesIDs       []string
	VerifyProfile   func(profileID string) error
}

// settingMutationResult is a completed write: the stored row, and for an
// idempotent write the receipt bytes (a replay serves those unchanged).
type settingMutationResult struct {
	response settingValueResponse
	raw      json.RawMessage
	replay   bool
}

func codedFieldError(status int, code, field, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message, Field: field}
}

func (h *SettingValuesHandler) definition(key string) (*settingscontract.Definition, *APIError) {
	def, ok := h.contract.Lookup(key)
	if !ok {
		return nil, codedFieldError(http.StatusNotFound, settingErrorUnknown, settingFieldKey,
			"No setting named "+key+" exists in this server's contract")
	}
	if !def.IsRemote() {
		return nil, codedFieldError(http.StatusBadRequest, settingErrorClientLocal, settingFieldKey,
			key+" is a device-local setting and is never stored by the server")
	}
	return def, nil
}

func (h *SettingValuesHandler) keyedScopeFor(requestedKey, requestedScope string) (string, settingscontract.Scope, *APIError) {
	key := strings.TrimSpace(requestedKey)
	if key == "" {
		return "", "", fieldError(settingFieldKey, "A setting key is required")
	}
	if _, err := h.definition(key); err != nil {
		return "", "", err
	}
	scope := settingscontract.Scope(strings.TrimSpace(requestedScope))
	if scope == "" {
		return "", "", fieldError(settingFieldScope,
			"A scope is required: account, profile, profile_client, profile_device, profile_library or profile_series")
	}
	return key, scope, nil
}

// sessionIdentity builds and validates the scope identity a request names.
//
// Scope comes from the request rather than the path so one route serves
// every scope; the store's own Validate then enforces that the identity fields
// match the scope, which is the same check the database CHECK constraint makes.
func (h *SettingValuesHandler) sessionIdentity(
	ctx context.Context, store userstore.UserStore, req SettingIdentityRequest,
) (userstore.SettingIdentity, *APIError) {
	key, scope, err := h.keyedScopeFor(req.Key, req.Scope)
	if err != nil {
		return userstore.SettingIdentity{}, err
	}

	identity := userstore.SettingIdentity{Key: key, Scope: scope}

	// The profile defaults to the session, so an ordinary caller cannot write
	// another's settings by naming it. A household parent may name a
	// different profile on their own account — authorized below.
	if scope != settingscontract.ScopeAccount {
		identity.ProfileID = strings.TrimSpace(req.ActiveProfileID)
		if identity.ProfileID == "" {
			return userstore.SettingIdentity{}, fieldError(settingFieldProfileHeader,
				"X-Profile-Id header is required for this scope")
		}
		if named := strings.TrimSpace(req.ProfileID); named != "" && named != identity.ProfileID {
			if err := h.mayActFor(ctx, store, identity.ProfileID, req.VerifyProfile, named); err != nil {
				return userstore.SettingIdentity{}, err
			}
			identity.ProfileID = named
		}
	}
	if scope == settingscontract.ScopeProfileClient {
		family := settingscontract.ClientFamily(strings.TrimSpace(req.ClientFamily))
		if !family.Valid() {
			return userstore.SettingIdentity{}, fieldError(settingFieldClientFamily,
				"X-Silo-Client-Family header must be one of tv, mobile, tablet, desktop or web")
		}
		identity.ClientFamily = family
	}
	if scope == settingscontract.ScopeProfileDevice {
		// A device may be named explicitly so one device can manage another's
		// settings — the screen that lists your devices edits them in place.
		// Unlike the profile above, that is safe to accept from the request
		// only because the device is then checked against this profile's
		// registry.
		named := strings.TrimSpace(req.DeviceID)
		identity.DeviceID = named
		if identity.DeviceID == "" {
			identity.DeviceID = req.Device.DeviceID
		}
		if identity.DeviceID == "" {
			return userstore.SettingIdentity{}, fieldError(settingFieldDeviceHeader,
				"X-Silo-Device-Id header is required for a device override")
		}
		if named != "" {
			if err := h.deviceBelongs(ctx, store, identity.ProfileID, named); err != nil {
				return userstore.SettingIdentity{}, err
			}
		}
	}

	return h.completeIdentityFor(ctx, req.LibraryID, req.SeriesID, identity)
}

// mayActFor authorizes acting for a profile other than the caller's own.
//
// Two checks, in this order and for different reasons. First the household
// guard: only the primary profile (or a server admin) manages the household, so
// an ordinary member naming a sibling is 403 — the profile plainly exists, and
// pretending otherwise would be a lie the caller can already disprove through
// GET /profiles. Then existence, resolved through the caller's *own* user
// store, which is what confines this to one account: a profile id from another
// account is simply absent there, so it is 404 and the caller learns nothing.
func (h *SettingValuesHandler) mayActFor(
	ctx context.Context, store userstore.UserStore, activeProfileID string,
	verify func(profileID string) error, profileID string,
) *APIError {
	if verify == nil {
		verify = func(string) error { return access.ErrProfileUnverified }
	}
	allowed, err := canManageHouseholdAs(ctx, store, activeProfileID, verify)
	if err != nil {
		if errors.Is(err, access.ErrProfileUnverified) {
			return &APIError{Status: http.StatusForbidden, Code: settingErrorForbidden,
				Message: "Profile management requires verifying the primary profile PIN", cause: err}
		}
		return &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to check profile permissions", cause: err}
	}
	if !allowed {
		return apiError(http.StatusForbidden, settingErrorForbidden,
			"Managing another profile's settings requires the primary profile or admin access")
	}

	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to load profile", cause: err}
	}
	if profile == nil {
		return codedFieldError(http.StatusNotFound, policyErrorNotFound, settingFieldProfileID, "Profile not found")
	}
	return nil
}

// deviceBelongs authorizes a device id that came from the request rather
// than from its own device header. It answers 404 rather than 403 for an
// unknown device: a 403 would confirm the id exists somewhere.
//
// The caller's own header device is deliberately not checked. Registration is
// lazy — a device's first write is what registers it — so requiring a row there
// would reject every new device's first setting.
func (h *SettingValuesHandler) deviceBelongs(
	ctx context.Context, store userstore.UserStore, profileID, deviceID string,
) *APIError {
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		return codedFieldError(http.StatusNotFound, policyErrorNotFound, settingFieldDeviceID, "Device not found")
	}
	exists, err := registry.DeviceExists(ctx, profileID, deviceID)
	if err != nil {
		return &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to look up device", cause: err}
	}
	if !exists {
		return codedFieldError(http.StatusNotFound, policyErrorNotFound, settingFieldDeviceID, "Device not found")
	}
	return nil
}

// completeIdentityFor fills the content-scope ids, then runs the checks the
// session and admin routes share: the identity matches its scope's columns
// and the contract allows the key at that scope.
func (h *SettingValuesHandler) completeIdentityFor(
	ctx context.Context, libraryID, seriesID string, identity userstore.SettingIdentity,
) (userstore.SettingIdentity, *APIError) {
	if identity.Scope == settingscontract.ScopeProfileLibrary {
		id, err := strconv.Atoi(strings.TrimSpace(libraryID))
		if err != nil || id <= 0 {
			return userstore.SettingIdentity{}, fieldError(settingFieldLibraryID,
				"library_id is required for a library override")
		}
		identity.LibraryID = id
		if err := h.libraryExists(ctx, id); err != nil {
			return userstore.SettingIdentity{}, err
		}
	}
	if identity.Scope == settingscontract.ScopeProfileSeries {
		identity.SeriesID = strings.TrimSpace(seriesID)
		if identity.SeriesID == "" {
			return userstore.SettingIdentity{}, fieldError(settingFieldSeriesID,
				"series_id is required for a series override")
		}
	}

	if err := identity.Validate(); err != nil {
		return userstore.SettingIdentity{}, fieldError(settingFieldScope, err.Error())
	}

	// The contract decides where a setting may be written, independently of
	// whether the identity is well formed.
	def, _ := h.contract.Lookup(identity.Key)
	if !def.AllowsScope(identity.Scope) {
		return userstore.SettingIdentity{}, codedFieldError(http.StatusBadRequest, settingErrorScopeDenied,
			settingFieldScope, identity.Key+" cannot be set at "+string(identity.Scope))
	}

	return identity, nil
}

func (h *SettingValuesHandler) libraryExists(ctx context.Context, libraryID int) *APIError {
	if h.libraryLookup == nil {
		return nil
	}
	if _, err := h.libraryLookup.GetByID(ctx, libraryID); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return codedFieldError(http.StatusNotFound, policyErrorNotFound, settingFieldLibraryID, "Library not found")
		}
		return &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to look up library", cause: err}
	}
	return nil
}

// clientFamilyFor validates an optional family header for effective reads.
// An absent header deliberately drops the profile_client layer so pre-revision
// 5 callers keep resolving broader fallbacks; explicit profile_client reads and
// writes still require the header in sessionIdentity. The server never
// guesses this identity from X-Silo-Device-Platform: that header is free-form
// display metadata, while client_family is a closed storage key shared by like
// clients.
func (h *SettingValuesHandler) clientFamilyFor(
	header string, keys []string,
) (settingscontract.ClientFamily, bool, *APIError) {
	eligible := false
	for _, key := range keys {
		if def, ok := h.contract.Lookup(key); ok && def.AllowsScope(settingscontract.ScopeProfileClient) {
			eligible = true
			break
		}
	}

	value := strings.TrimSpace(header)
	if value == "" {
		return "", false, nil
	}
	family := settingscontract.ClientFamily(value)
	if !family.Valid() {
		return "", false, fieldError(settingFieldClientFamily,
			"X-Silo-Client-Family header must be one of tv, mobile, tablet, desktop or web")
	}
	return family, eligible, nil
}

func (h *SettingValuesHandler) readSettingValue(
	ctx context.Context, store userstore.UserStore, identity userstore.SettingIdentity,
) (settingValueResponse, *APIError) {
	value, err := store.GetSettingValue(ctx, identity)
	if err != nil {
		return settingValueResponse{}, &APIError{Status: http.StatusInternalServerError,
			Code: policyErrorInternal, Message: "Failed to read the setting", cause: err}
	}
	if value == nil {
		return settingValueResponse{}, apiError(http.StatusNotFound, policyErrorNotFound, "No value is set at this scope")
	}
	return settingValueToResponse(*value), nil
}

// listExplicitValues returns the explicit values for several keys at exactly
// one scope. Missing rows remain in the response with is_set false; this is
// the contract's read shape for independently presenting profile defaults and
// device/content overrides.
func (h *SettingValuesHandler) listExplicitValues(
	ctx context.Context, store userstore.UserStore, identity userstore.SettingIdentity, keys []string,
) ([]explicitSettingValueResponse, *APIError) {
	for _, key := range keys[1:] {
		def, err := h.definition(key)
		if err != nil {
			err.Field = settingFieldKeys
			return nil, err
		}
		if !def.AllowsScope(identity.Scope) {
			return nil, codedFieldError(http.StatusBadRequest, settingErrorScopeDenied, settingFieldScope,
				key+" cannot be set at "+string(identity.Scope))
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
	stored, err := store.ListSettingValuesForResolution(ctx, query)
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to read settings", cause: err}
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
	return out, nil
}

// writeSettingValue is the write path shared by the session route, the admin
// route and v2: validation, normalization, idempotency and the change event
// are one implementation regardless of who addresses the store. eventUserID
// names the account whose settings changed — the session owner on the
// self-service route, the target user on the admin route — so change events
// always reach the clients whose settings moved. device is the device the
// request declared; it is registered when it writes its own override.
//
// A value that exceeds a policy restriction is stored, not rejected: the
// restriction filters what a preference does at resolution time, and destroying
// the preference would mean a capped 4K choice never takes effect when the cap
// lifts.
func (h *SettingValuesHandler) writeSettingValue(
	ctx context.Context,
	store userstore.UserStore,
	eventUserID int,
	identity userstore.SettingIdentity,
	rawValue json.RawMessage,
	mutationID string,
	device DeviceMetadata,
) (settingMutationResult, *APIError) {
	def, apiErr := h.definition(identity.Key)
	if apiErr != nil {
		return settingMutationResult{}, apiErr
	}
	if len(rawValue) == 0 {
		return settingMutationResult{}, fieldError(settingFieldValue, "value is required")
	}

	normalized, err := def.ValueSchema.NormalizeValue(rawValue, settingscontract.ObjectSchemas())
	if err != nil {
		return settingMutationResult{}, codedFieldError(http.StatusBadRequest, settingErrorInvalidValue,
			settingFieldValue, err.Error())
	}

	// A deprecated key and its replacement are one preference stored twice
	// while old clients are in the field, so a write to either lands on both.
	// The value is already normalized, so a conversion failure here is a defect
	// in the pairing rather than bad input — refuse the request rather than
	// store half of it.
	mirror, hasMirror, err := settingscontract.MirrorWrite(identity.Key, normalized)
	if err != nil {
		slog.ErrorContext(ctx, "settings mirror could not convert a normalized value",
			"component", "api", "key", identity.Key, "error", err)
		return settingMutationResult{}, apiError(http.StatusInternalServerError, policyErrorInternal, "Failed to store the setting")
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
	outcome, err := runSettingMutation(
		ctx, store, mutationID, hashMutationRequest(identity, normalized),
		func(writer userstore.SettingMutationWriter) (*userstore.SettingValue, bool, error) {
			value, err := upsertMirroredPair(
				ctx, writer, identity, normalized, mirrorIdentity, mirror, hasMirror)
			if err != nil {
				return nil, false, err
			}
			if err := writeLegacyIntroSkipColumn(ctx, writer, identity, columnValue); err != nil {
				return nil, false, err
			}
			return value, true, nil
		},
	)
	if errors.Is(err, errMutationIDConflict) {
		return settingMutationResult{}, apiError(http.StatusConflict, settingErrorMutationID,
			"This mutation id was used for a different write")
	}
	if err != nil {
		if errors.Is(err, userstore.ErrInvalidSettingIdentity) ||
			errors.Is(err, userstore.ErrInvalidSettingValue) {
			return settingMutationResult{}, fieldError(settingFieldValue, err.Error())
		}
		slog.ErrorContext(ctx, "failed to store a canonical setting",
			"component", "api", "key", identity.Key, "scope", string(identity.Scope), "error", err)
		return settingMutationResult{}, apiError(http.StatusInternalServerError, policyErrorInternal, "Failed to store the setting")
	}
	if outcome.replay {
		return settingMutationResult{raw: outcome.result, replay: true}, nil
	}
	stored := outcome.stored

	response := settingValueToResponse(*stored)
	acting := actingProfileID(ctx)
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
		if identity.DeviceID == device.DeviceID && identity.ProfileID == acting {
			h.registerWritingDevice(ctx, store, identity.ProfileID, device)
		}
	}
	publishUserSettingsEvent(ctx, h.EventsHub,
		eventUserID, identity.ProfileID, identity.Key, string(identity.Scope))
	auditSettingsForOther(ctx, settingsAuditRecord{
		Action:          settingsAuditActionSet,
		ActorProfileID:  acting,
		TargetProfileID: identity.ProfileID,
		TargetUserID:    eventUserID,
		ClientFamily:    string(identity.ClientFamily),
		DeviceID:        identity.DeviceID,
		Key:             identity.Key,
		Scope:           string(identity.Scope),
	})
	return settingMutationResult{response: response, raw: outcome.result}, nil
}

// clearSettingValue is the unset path shared by the session route, the admin
// route and v2. See writeSettingValue for what eventUserID means.
func (h *SettingValuesHandler) clearSettingValue(
	ctx context.Context,
	store userstore.UserStore,
	eventUserID int,
	identity userstore.SettingIdentity,
) *APIError {
	// One transaction for the whole clear. Clearing one half of a mirrored pair
	// clears both — a surviving companion row would go on resolving as an
	// explicit choice at a scope the caller just said it wanted to inherit at —
	// and a profile-scope clear also returns the legacy column to the contract
	// default, so GET /profiles stops serving the value this request cleared.
	// Committing those separately is what leaves a client retrying against a 404
	// while the companion it could not see keeps resolving.
	transactioner, ok := store.(userstore.SettingMutationTransactioner)
	if !ok {
		slog.ErrorContext(ctx, "settings store cannot clear a value atomically",
			"component", "api", "key", identity.Key)
		return apiError(http.StatusInternalServerError, policyErrorInternal, "Failed to clear the setting")
	}
	mirrorKey, hasMirror := settingscontract.MirrorKey(identity.Key)
	clearedColumn := h.legacyIntroSkipColumnCleared(identity.Key)

	var removed, mirrorRemoved bool
	err := transactioner.WithSettingMutationTransaction(ctx, "",
		func(writer userstore.SettingMutationWriter) error {
			var err error
			if removed, err = writer.DeleteSettingValue(ctx, identity); err != nil {
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
				if mirrorRemoved, err = writer.DeleteSettingValue(ctx, mirrorIdentity); err != nil {
					return err
				}
			}
			if !removed && !mirrorRemoved {
				return nil // nothing was stored here, so nothing to reconcile
			}
			return writeLegacyIntroSkipColumn(ctx, writer, identity, clearedColumn)
		})
	if err != nil {
		slog.ErrorContext(ctx, "failed to clear a canonical setting",
			"component", "api", "key", identity.Key, "scope", string(identity.Scope), "error", err)
		return apiError(http.StatusInternalServerError, policyErrorInternal, "Failed to clear the setting")
	}
	if !removed {
		// 404 stays the answer for "no value is set at this scope", which is
		// what the caller addressed. A stray companion that was swept up on the
		// way is a repair, not a result, so it is logged and published rather
		// than turned into a success the caller did not ask for.
		if mirrorRemoved {
			slog.WarnContext(ctx, "cleared a mirrored setting whose addressed half was already gone",
				"component", "api", "key", identity.Key, "mirror_key", mirrorKey,
				"scope", string(identity.Scope))
			publishUserSettingsEvent(ctx, h.EventsHub,
				eventUserID, identity.ProfileID, mirrorKey, string(identity.Scope))
		}
		return apiError(http.StatusNotFound, policyErrorNotFound, "No value is set at this scope")
	}
	auditSettingsForOther(ctx, settingsAuditRecord{
		Action:          "clear",
		ActorProfileID:  actingProfileID(ctx),
		TargetProfileID: identity.ProfileID,
		TargetUserID:    eventUserID,
		ClientFamily:    string(identity.ClientFamily),
		DeviceID:        identity.DeviceID,
		Key:             identity.Key,
		Scope:           string(identity.Scope),
	})
	publishUserSettingsEvent(ctx, h.EventsHub,
		eventUserID, identity.ProfileID, identity.Key, string(identity.Scope))
	return nil
}

// setNavigationShortcut applies one desired-state edit to the shared profile
// shortcut catalog. Unlike the generic whole-document write, two clients
// adding different destinations cannot overwrite one another: the edit
// rebases after an internal compare-and-set conflict until its semantic edit
// lands on the newest document.
func (h *SettingValuesHandler) setNavigationShortcut(
	ctx context.Context,
	store userstore.UserStore,
	userID int,
	profileID string,
	rawItem json.RawMessage,
	present *bool,
	mutationID string,
) (settingMutationResult, *APIError) {
	shortcutStore, ok := store.(shortcutMutationStore)
	if !ok {
		return settingMutationResult{}, apiError(http.StatusInternalServerError, policyErrorInternal,
			"This settings store does not support atomic shortcut updates")
	}

	identity := userstore.SettingIdentity{
		Key: settingskeys.NavShortcuts, Scope: settingscontract.ScopeProfile, ProfileID: strings.TrimSpace(profileID),
	}
	if err := identity.Validate(); err != nil {
		return settingMutationResult{}, fieldError(settingFieldProfileHeader, "X-Profile-Id header is required")
	}
	def, apiErr := h.definition(identity.Key)
	if apiErr != nil {
		return settingMutationResult{}, apiErr
	}
	if present == nil {
		return settingMutationResult{}, fieldError(settingFieldPresent, "present is required")
	}

	item, err := normalizeNavigationShortcutItem(def, rawItem)
	if err != nil {
		return settingMutationResult{}, codedFieldError(http.StatusBadRequest, settingErrorInvalidValue,
			settingFieldItem, err.Error())
	}
	requestHash := hashNavigationShortcutMutation(identity, item, *present)

	var stored *userstore.SettingValue
	var idempotentResult json.RawMessage
	var changed bool
	if mutationID == "" {
		stored, changed, err = mutateNavigationShortcut(ctx, shortcutStore, def, identity, item, *present)
	} else {
		var outcome idempotentSettingMutationOutcome
		outcome, err = runSettingMutation(
			ctx, store, mutationID, requestHash,
			func(writer userstore.SettingMutationWriter) (*userstore.SettingValue, bool, error) {
				return mutateNavigationShortcut(ctx, writer, def, identity, item, *present)
			},
		)
		if err == nil && outcome.replay {
			return settingMutationResult{raw: outcome.result, replay: true}, nil
		}
		stored, changed = outcome.stored, outcome.changed
		idempotentResult = outcome.result
	}
	if err != nil {
		switch {
		case errors.Is(err, errMutationIDConflict):
			return settingMutationResult{}, apiError(http.StatusConflict, settingErrorMutationID,
				"This mutation id was used for a different write")
		case errors.Is(err, errShortcutMutationContention):
			return settingMutationResult{}, apiError(http.StatusConflict, settingErrorUpdateConflict,
				"Navigation shortcuts changed too quickly; retry this mutation")
		case errors.Is(err, errShortcutMutationInvalidValue):
			return settingMutationResult{}, codedFieldError(http.StatusBadRequest, settingErrorInvalidValue,
				settingFieldItem, err.Error())
		case errors.Is(err, userstore.ErrInvalidSettingIdentity),
			errors.Is(err, userstore.ErrInvalidSettingValue):
			return settingMutationResult{}, fieldError(settingFieldItem, err.Error())
		default:
			return settingMutationResult{}, apiError(http.StatusInternalServerError, policyErrorInternal,
				"Failed to store navigation shortcuts")
		}
	}

	if changed {
		publishUserSettingsEvent(ctx, h.EventsHub, userID, identity.ProfileID, identity.Key, string(identity.Scope))
		auditSettingsForOther(ctx, settingsAuditRecord{
			Action:          settingsAuditActionSet,
			ActorProfileID:  actingProfileID(ctx),
			TargetProfileID: identity.ProfileID,
			TargetUserID:    userID,
			Key:             identity.Key,
			Scope:           string(identity.Scope),
		})
	}
	return settingMutationResult{response: settingValueToResponse(*stored), raw: idempotentResult}, nil
}

// resolveEffective resolves any number of keys in one request.
//
// Batched deliberately: a client opening a settings screen needs every key at
// once, and a season view needs several keys across many series. One store read
// serves all of it.
func (h *SettingValuesHandler) resolveEffective(
	ctx context.Context, store userstore.UserStore, q EffectiveSettingsQuery,
) ([]effectiveSettingValueResponse, *APIError) {
	keys := q.Keys
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
				return nil, codedFieldError(http.StatusNotFound, settingErrorUnknown, settingFieldKeys,
					"No setting named "+key+" exists in this server's contract")
			}
		}
	}

	rc := settingsresolve.Context{
		ProfileID:  strings.TrimSpace(q.ActiveProfileID),
		DeviceID:   q.Device.DeviceID,
		LibraryIDs: q.LibraryIDs,
		SeriesIDs:  q.SeriesIDs,
	}
	if family, needed, err := h.clientFamilyFor(q.ClientFamily, keys); err != nil {
		return nil, err
	} else if needed {
		rc.ClientFamily = family
	}

	// A device-settings screen resolves what some *other* device sees, so this
	// read accepts the same explicit identity the write path does, under the
	// same guards: the device must belong to the profile, and naming another
	// profile requires the household parent.
	if named := strings.TrimSpace(q.ProfileID); named != "" && named != rc.ProfileID {
		if err := h.mayActFor(ctx, store, q.ActiveProfileID, q.VerifyProfile, named); err != nil {
			return nil, err
		}
		rc.ProfileID = named
	}
	if named := strings.TrimSpace(q.DeviceID); named != "" {
		if err := h.deviceBelongs(ctx, store, rc.ProfileID, named); err != nil {
			return nil, err
		}
		rc.DeviceID = named
	}

	// The SQLite backend expands these into IN lists, whose host-parameter
	// budget is finite; an unbounded request could fail the whole resolution.
	// The bound is far above any real batch — a season view resolves a
	// handful of series, not hundreds.
	if len(rc.LibraryIDs)+len(rc.SeriesIDs) > maxEffectiveContentIDs {
		return nil, fieldError(settingFieldLibraryID,
			"Too many library_ids/series_ids in one request; resolve in smaller batches")
	}

	// A device-aware key resolved without a device identity would silently
	// skip every stored device override and pass the profile fallback off as
	// the effective value — a plausible wrong answer. Fail closed instead:
	// the write path already requires the header for device overrides.
	if rc.DeviceID == "" {
		for _, key := range keys {
			if def, ok := h.contract.Lookup(key); ok &&
				def.AllowsScope(settingscontract.ScopeProfileDevice) {
				return nil, fieldError(settingFieldDeviceHeader,
					"X-Silo-Device-Id header is required to resolve "+key)
			}
		}
	}

	resolved, err := h.resolver.Resolve(ctx, store, rc, keys, h.constraintsFor(ctx))
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to resolve settings", cause: err}
	}
	return h.effectiveResponses(ctx, resolved), nil
}

// resolveEffectiveContexts resolves the same keys under several content
// contexts at once, each answered under its caller-chosen context_id.
func (h *SettingValuesHandler) resolveEffectiveContexts(
	ctx context.Context, store userstore.UserStore, q EffectiveSettingsQuery, requested []effectiveContextRequest,
) ([]effectiveContextResponse, *APIError) {
	keys := q.Keys
	if len(keys) == 0 {
		return nil, fieldError(settingFieldKeys, "keys must contain at least one setting")
	}
	for _, key := range keys {
		if _, err := h.definition(key); err != nil {
			err.Field = settingFieldKeys
			return nil, err
		}
	}
	if len(requested) == 0 {
		return nil, fieldError(settingFieldContexts, "contexts must contain at least one content context")
	}
	if len(requested) > maxEffectiveContentIDs {
		return nil, fieldError(settingFieldContexts, "Too many contexts in one request; resolve in smaller batches")
	}

	profileID := strings.TrimSpace(q.ActiveProfileID)
	deviceID := q.Device.DeviceID
	clientFamily, familyNeeded, apiErr := h.clientFamilyFor(q.ClientFamily, keys)
	if apiErr != nil {
		return nil, apiErr
	}
	if deviceID == "" {
		for _, key := range keys {
			if def, exists := h.contract.Lookup(key); exists && def.AllowsScope(settingscontract.ScopeProfileDevice) {
				return nil, fieldError(settingFieldDeviceHeader, "X-Silo-Device-Id header is required to resolve "+key)
			}
		}
	}

	seen := make(map[string]struct{}, len(requested))
	contexts := make([]settingsresolve.Context, 0, len(requested))
	contentIDs := 0
	for _, request := range requested {
		contextID := request.ContextID
		if strings.TrimSpace(contextID) == "" {
			return nil, fieldError(settingFieldContexts, "Every context requires a non-empty context_id")
		}
		if _, exists := seen[contextID]; exists {
			return nil, fieldError(settingFieldContexts, "context_id values must be unique")
		}
		seen[contextID] = struct{}{}

		libraryID, err := parseOptionalPositiveJSONInt(request.LibraryID)
		if err != nil {
			return nil, fieldError(settingFieldContexts, "library_id must be a positive integer or numeric string")
		}
		seriesID := strings.TrimSpace(request.SeriesID)
		if libraryID == 0 && seriesID == "" {
			return nil, fieldError(settingFieldContexts, "Every context requires library_id or series_id")
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
			return nil, fieldError(settingFieldContexts, "Too many content ids in one request; resolve in smaller batches")
		}
		contexts = append(contexts, rc)
	}

	resolved, err := h.resolver.ResolveContexts(ctx, store, contexts, keys, h.constraintsFor(ctx))
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to resolve settings", cause: err}
	}
	allResolved := make([]settingsresolve.Effective, 0)
	for _, values := range resolved {
		allResolved = append(allResolved, values...)
	}
	observed := h.observedLanguageSuggestions(ctx, allResolved)
	out := make([]effectiveContextResponse, len(resolved))
	for i, values := range resolved {
		out[i].ContextID = requested[i].ContextID
		// Resolution deduplicates keys to share reads. The response still has
		// one entry per requested key, including repeats, in request order.
		byKey := make(map[string]effectiveSettingValueResponse, len(values))
		for _, value := range h.effectiveResponsesWithObserved(values, observed) {
			byKey[value.Key] = value
		}
		out[i].Settings = make([]effectiveSettingValueResponse, len(keys))
		for j, key := range keys {
			out[i].Settings[j] = byKey[key]
		}
	}
	return out, nil
}

// The exported seam: what v2 calls. Each method resolves the account's store,
// runs the shared core and returns its decision as an error the v2 listener
// renders (a *APIError, or nil).

// ContractRevision is the settings manifest revision this server serves.
func (h *SettingValuesHandler) ContractRevision() int { return h.contract.Revision }

func (h *SettingValuesHandler) storeOf(ctx context.Context, userID int) (userstore.UserStore, *APIError) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal,
			Message: "Failed to access user store", cause: err}
	}
	return store, nil
}

// GetSettingValue reads the explicit value stored at one scope.
func (h *SettingValuesHandler) GetSettingValue(ctx context.Context, userID int, req SettingIdentityRequest) (SettingValueView, error) {
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	identity, apiErr := h.sessionIdentity(ctx, store, req)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	view, apiErr := h.readSettingValue(ctx, store, identity)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	return view, nil
}

// ListSettingValues reads the explicit values of several keys at one scope;
// req.Key is ignored in favor of keys, whose first member fixes the scope
// checks. Unset keys are reported with is_set false.
func (h *SettingValuesHandler) ListSettingValues(ctx context.Context, userID int, keys []string, req SettingIdentityRequest) ([]ExplicitSettingValueView, error) {
	// keys arrive one per repeated query parameter: a comma is part of a
	// key, not a separator, and a repeated key is answered once per mention
	// rather than deduplicated (the v2 contract preserves the parameter as
	// sent). v1's CSV form is parsed by its own handler before the seam.
	trimmed := make([]string, 0, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			trimmed = append(trimmed, key)
		}
	}
	keys = trimmed
	if len(keys) == 0 {
		return nil, fieldError(settingFieldKeys, "Query parameter keys is required")
	}
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return nil, apiErr
	}
	req.Key = keys[0]
	identity, apiErr := h.sessionIdentity(ctx, store, req)
	if apiErr != nil {
		if apiErr.Field == settingFieldKey {
			apiErr.Field = settingFieldKeys
		}
		return nil, apiErr
	}
	views, apiErr := h.listExplicitValues(ctx, store, identity, keys)
	if apiErr != nil {
		return nil, apiErr
	}
	return views, nil
}

// SetSettingValue writes an explicit value at one scope and returns the
// stored row. The whole-document write of nav.shortcuts is refused in favor
// of SetNavigationShortcut, as on v1.
func (h *SettingValuesHandler) SetSettingValue(ctx context.Context, userID int, req SettingIdentityRequest, value json.RawMessage) (SettingValueView, error) {
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	identity, apiErr := h.sessionIdentity(ctx, store, req)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	if identity.Key == settingskeys.NavShortcuts {
		return SettingValueView{}, codedFieldError(http.StatusBadRequest, settingErrorAtomicRequired,
			settingFieldKey, navigationShortcutAtomicUpdateMessage)
	}
	result, apiErr := h.writeSettingValue(ctx, store, userID, identity, value, "", req.Device)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	return result.response, nil
}

// DeleteSettingValue clears the explicit value at one scope.
func (h *SettingValuesHandler) DeleteSettingValue(ctx context.Context, userID int, req SettingIdentityRequest) error {
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return apiErr
	}
	identity, apiErr := h.sessionIdentity(ctx, store, req)
	if apiErr != nil {
		return apiErr
	}
	if identity.Key == settingskeys.NavShortcuts {
		return codedFieldError(http.StatusBadRequest, settingErrorAtomicRequired,
			settingFieldKey, navigationShortcutAtomicUpdateMessage)
	}
	if apiErr := h.clearSettingValue(ctx, store, userID, identity); apiErr != nil {
		return apiErr
	}
	return nil
}

// SetNavigationShortcut adds or removes one shortcut of the acting profile's
// nav.shortcuts document and returns the stored document.
func (h *SettingValuesHandler) SetNavigationShortcut(ctx context.Context, userID int, profileID string, item json.RawMessage, present bool) (SettingValueView, error) {
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	result, apiErr := h.setNavigationShortcut(ctx, store, userID, profileID, item, &present, "")
	if apiErr != nil {
		return SettingValueView{}, apiErr
	}
	return result.response, nil
}

// ResolveEffectiveSettings resolves the effective values of the query's keys
// (every remote key when none are named) for the acting or named profile.
func (h *SettingValuesHandler) ResolveEffectiveSettings(ctx context.Context, userID int, q EffectiveSettingsQuery) ([]EffectiveSettingValueView, error) {
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return nil, apiErr
	}
	views, apiErr := h.resolveEffective(ctx, store, q)
	if apiErr != nil {
		return nil, apiErr
	}
	return views, nil
}

// ResolveEffectiveSettingContexts resolves the query's keys under several
// content contexts at once.
func (h *SettingValuesHandler) ResolveEffectiveSettingContexts(ctx context.Context, userID int, q EffectiveSettingsQuery, contexts []EffectiveContextRequest) ([]EffectiveSettingContextView, error) {
	store, apiErr := h.storeOf(ctx, userID)
	if apiErr != nil {
		return nil, apiErr
	}
	views, apiErr := h.resolveEffectiveContexts(ctx, store, q, contexts)
	if apiErr != nil {
		return nil, apiErr
	}
	return views, nil
}

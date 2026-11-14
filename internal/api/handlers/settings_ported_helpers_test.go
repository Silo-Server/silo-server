package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Helpers for the settings tests that call the request-free seam v2 uses
// (GetSettingValue, SetSettingValue, ResolveEffectiveSettings, ...).

const seamUserID = 1

// seamCtx is the context v2 hands the seam: the account's claims and the
// acting profile, which the seam reads for device registration and audit.
func seamCtx(t *testing.T, profileID string) context.Context {
	t.Helper()
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: seamUserID})
	return apimw.SetProfileID(ctx, profileID)
}

// seamReq addresses key at scope as profile-1 on device-1, a web client.
func seamReq(key string, scope settingscontract.Scope) SettingIdentityRequest {
	return SettingIdentityRequest{
		Key:             key,
		Scope:           string(scope),
		ActiveProfileID: "profile-1",
		Device:          NewDeviceMetadata("device-1", "", ""),
		ClientFamily:    string(settingscontract.ClientFamilyWeb),
	}
}

// seamQuery resolves keys as profile-1 on device-1, a web client.
func seamQuery(keys ...string) EffectiveSettingsQuery {
	return EffectiveSettingsQuery{
		Keys:            keys,
		ActiveProfileID: "profile-1",
		Device:          NewDeviceMetadata("device-1", "", ""),
		ClientFamily:    string(settingscontract.ClientFamilyWeb),
	}
}

// requireSettingStatus asserts err is a seam decision with the given status.
func requireSettingStatus(t *testing.T, err error, status int, what string) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("%s: err = %v, want status %d", what, err, status)
	}
	return apiErr
}

// seamSet writes value at req and fails the test on error.
func seamSet(t *testing.T, ctx context.Context, h *SettingValuesHandler, req SettingIdentityRequest, value string) SettingValueView {
	t.Helper()
	view, err := h.SetSettingValue(ctx, seamUserID, req, json.RawMessage(value))
	if err != nil {
		t.Fatalf("set %s at %s: %v", req.Key, req.Scope, err)
	}
	return view
}

// seamEffective resolves exactly one key and fails the test on error.
func seamEffective(t *testing.T, ctx context.Context, h *SettingValuesHandler, q EffectiveSettingsQuery) EffectiveSettingValueView {
	t.Helper()
	views, err := h.ResolveEffectiveSettings(ctx, seamUserID, q)
	if err != nil {
		t.Fatalf("resolve %v: %v", q.Keys, err)
	}
	if len(views) != 1 {
		t.Fatalf("resolved %d settings, want 1", len(views))
	}
	return views[0]
}

// setShortcut applies one navigation shortcut edit for profile-1.
func setShortcut(t *testing.T, h *SettingValuesHandler, item string, present bool) (SettingValueView, navigationShortcutDocument, error) {
	t.Helper()
	view, err := h.SetNavigationShortcut(seamCtx(t, "profile-1"), seamUserID, "profile-1", json.RawMessage(item), present)
	if err != nil {
		return view, navigationShortcutDocument{}, err
	}
	var document navigationShortcutDocument
	if err := json.Unmarshal(view.Value, &document); err != nil {
		t.Fatalf("decode shortcut document: %v; value=%s", err, view.Value)
	}
	return view, document, nil
}

// mustSetShortcut is setShortcut that fails the test on error.
func mustSetShortcut(t *testing.T, h *SettingValuesHandler, item string, present bool) (SettingValueView, navigationShortcutDocument) {
	t.Helper()
	view, document, err := setShortcut(t, h, item, present)
	if err != nil {
		t.Fatalf("shortcut %s present=%v: %v", item, present, err)
	}
	return view, document
}

func navShortcutIdentity() userstore.SettingIdentity {
	return userstore.SettingIdentity{
		Key: "nav.shortcuts", Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	}
}

// adminSeamEnv gives the admin and the target account distinct stores, so an
// admin write that resolved the wrong account is caught instead of masked.
type adminSeamEnv struct {
	handler     *SettingValuesHandler
	adminStore  userstore.UserStore
	targetStore userstore.UserStore
}

func newAdminSeamEnv(t *testing.T) adminSeamEnv {
	t.Helper()
	adminStore := newIsolatedProfileTestStore(t, "admin")
	targetStore := newIsolatedProfileTestStore(t, "target")
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	handler := NewSettingValuesHandler(mappedTestUserStoreProvider{
		stores: map[int]userstore.UserStore{
			adminValuesAdminID:  adminStore,
			adminValuesTargetID: targetStore,
		},
	}, contract)
	return adminSeamEnv{handler: handler, adminStore: adminStore, targetStore: targetStore}
}

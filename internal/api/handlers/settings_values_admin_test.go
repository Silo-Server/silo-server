package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	adminValuesAdminID  = 1
	adminValuesTargetID = 7
)

// adminValuesEnv mounts the admin projection exactly as the router does: the
// canonical handler behind RequireActingAdmin, with the target user's store
// distinct from the admin's own so a route that resolved the wrong user is
// caught rather than masked by a shared store.
type adminValuesEnv struct {
	router      chi.Router
	handler     *SettingValuesHandler
	adminStore  userstore.UserStore
	targetStore userstore.UserStore
}

func newAdminValuesEnv(t *testing.T) adminValuesEnv {
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

	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(apimw.RequireActingAdmin(nil, nil))
		r.Get("/admin/users/{id}/settings/values", handler.HandleAdminListUserSettingValues)
		r.Put("/admin/users/{id}/settings/values/{key}", handler.HandleAdminSetUserSettingValue)
		r.Delete("/admin/users/{id}/settings/values/{key}", handler.HandleAdminDeleteUserSettingValue)
	})
	return adminValuesEnv{router: router, handler: handler, adminStore: adminStore, targetStore: targetStore}
}

// do sends a request through the mounted routes as a caller with the given
// role; an empty role sends no session at all.
func (env adminValuesEnv) do(t *testing.T, role, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	}
	if role != "" {
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: adminValuesAdminID, Role: role}))
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

func TestAdminNavigationShortcutRepairPreservesRevisionHistory(t *testing.T) {
	env := newAdminValuesEnv(t)
	ctx := context.Background()
	identity := userstore.SettingIdentity{
		Key: "nav.shortcuts", Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	}
	seeded, err := env.targetStore.UpsertSettingValue(ctx, identity,
		json.RawMessage(`{"items":[{"type":"library","library_id":42,"label":"Movies"}]}`))
	if err != nil {
		t.Fatalf("seed shortcuts: %v", err)
	}
	if seeded.Revision != 1 {
		t.Fatalf("seed revision = %d, want 1", seeded.Revision)
	}

	target := "/admin/users/7/settings/values/nav.shortcuts?scope=profile&profile_id=profile-1"
	if rec := env.do(t, "admin", http.MethodDelete, target, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("admin repair DELETE = %d, want 400: %s", rec.Code, rec.Body.String())
	} else if !bytes.Contains(rec.Body.Bytes(), []byte(`"error":"atomic_update_required"`)) {
		t.Fatalf("admin repair DELETE body = %s, want atomic_update_required", rec.Body.String())
	}
	if got, err := env.targetStore.GetSettingValue(ctx, identity); err != nil || got == nil || got.Revision != 1 {
		t.Fatalf("shortcut after rejected DELETE = %+v err=%v, want revision 1 intact", got, err)
	}

	rec := env.do(t, "admin", http.MethodPut, target, []byte(`{"value":{"items":[]}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin repair PUT empty = %d: %s", rec.Code, rec.Body.String())
	}
	var repaired settingValueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &repaired); err != nil {
		t.Fatalf("decode repair response: %v", err)
	}
	if repaired.Revision != 2 || string(repaired.Value) != `{"items":[]}` {
		t.Fatalf("repair response = revision %d value %s, want empty revision 2",
			repaired.Revision, repaired.Value)
	}

	cas, ok := env.targetStore.(userstore.SettingValueCompareAndSetter)
	if !ok {
		t.Fatal("target store does not support compare-and-set")
	}
	_, err = cas.CompareAndSetSettingValue(ctx, identity,
		json.RawMessage(`{"items":[{"type":"library","library_id":99,"label":"Shows"}]}`),
		seeded.Revision)
	if !errors.Is(err, userstore.ErrSettingValueRevisionConflict) {
		t.Fatalf("stale CAS after admin repair = %v, want revision conflict", err)
	}
	if got, err := env.targetStore.GetSettingValue(ctx, identity); err != nil || got == nil ||
		got.Revision != 2 || string(got.Value) != `{"items":[]}` {
		t.Fatalf("shortcut after stale CAS = %+v err=%v, want empty revision 2", got, err)
	}
}

// TestAdminMutationsAttributeEventsToTheTargetUser pins the admin-specific
// half of the change-event contract: the envelope is addressed to the user
// whose settings moved — the target named in the path — never to the acting
// admin. Addressing the admin instead would leave the target's devices stale
// on exactly the change they most need to hear about, while poking the
// admin's own devices for nothing. The acting admin (user 1) and the target
// (user 7) are distinct here precisely so the two attributions cannot alias.
func TestAdminMutationsAttributeEventsToTheTargetUser(t *testing.T) {
	env := newAdminValuesEnv(t)
	env.handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := env.handler.EventsHub.Subscribe()
	defer unsubscribe()

	target := "/admin/users/7/settings/values/playback.subtitle_mode?scope=profile&profile_id=profile-1"

	assertTargetEnvelope := func(operation string) {
		t.Helper()
		var envelope evt.Envelope
		select {
		case envelope = <-events:
		default:
			t.Fatalf("admin %s published no event", operation)
		}
		if envelope.UserID != adminValuesTargetID {
			t.Errorf("admin %s event addressed to user %d, want the target %d",
				operation, envelope.UserID, adminValuesTargetID)
		}
		if envelope.UserID == adminValuesAdminID {
			t.Errorf("admin %s event addressed to the acting admin", operation)
		}
		if envelope.ProfileID != "profile-1" {
			t.Errorf("admin %s event profile = %q, want profile-1", operation, envelope.ProfileID)
		}
		if envelope.Channel != evt.ChannelUserSettings || envelope.Event != userSettingsChangedEvent {
			t.Errorf("admin %s published %s on %s, want %s on %s",
				operation, envelope.Event, envelope.Channel,
				userSettingsChangedEvent, evt.ChannelUserSettings)
		}
	}

	if rec := env.do(t, "admin", http.MethodPut, target, []byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	assertTargetEnvelope("PUT")

	if rec := env.do(t, "admin", http.MethodDelete, target, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	assertTargetEnvelope("DELETE")
}

func TestAdminSettingServiceTargetIdentity(t *testing.T) {
	env := newAdminValuesEnv(t)
	ctx := t.Context()
	req := SettingIdentityRequest{Key: "playback.subtitle_language", Scope: "profile_device", ProfileID: "profile-1", DeviceID: "tv-1"}
	row, err := env.handler.SetAdminAccountSetting(ctx, adminValuesTargetID, req, json.RawMessage(`"de"`))
	if err != nil || string(row.Value) != `"de"` || row.ProfileID != "profile-1" {
		t.Fatalf("%+v %v", row, err)
	}
	id := userstore.SettingIdentity{Key: req.Key, Scope: settingscontract.ScopeProfileDevice, ProfileID: req.ProfileID, DeviceID: req.DeviceID}
	if got, err := env.adminStore.GetSettingValue(ctx, id); err != nil || got != nil {
		t.Fatalf("administrator store changed: %+v %v", got, err)
	}
	if err := env.handler.DeleteAdminAccountSetting(ctx, adminValuesTargetID, req); err != nil {
		t.Fatal(err)
	}
	if got, err := env.targetStore.GetSettingValue(ctx, id); err != nil || got != nil {
		t.Fatalf("target setting retained: %+v %v", got, err)
	}
	for _, profile := range []string{"", "ghost"} {
		req.ProfileID = profile
		if _, err := env.handler.SetAdminAccountSetting(ctx, adminValuesTargetID, req, json.RawMessage(`"en"`)); err == nil {
			t.Fatal("accepted nonexistent target profile")
		}
	}
	req = SettingIdentityRequest{Key: "nav.shortcuts", Scope: "profile", ProfileID: "profile-1"}
	if err := env.handler.DeleteAdminAccountSetting(ctx, adminValuesTargetID, req); err == nil {
		t.Fatal("deleted shortcuts instead of explicit empty document")
	}
}

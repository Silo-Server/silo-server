package handlers

import (
	"bytes"
	"context"
	"encoding/json"
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
		r.Use(apimw.RequireActingAdmin(nil))
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

func TestAdminSettingValuesRefuseNonAdmins(t *testing.T) {
	env := newAdminValuesEnv(t)

	for name, req := range map[string]struct {
		method, target string
		body           []byte
	}{
		"list":   {http.MethodGet, "/admin/users/7/settings/values", nil},
		"set":    {http.MethodPut, "/admin/users/7/settings/values/playback.subtitle_mode?scope=account", []byte(`{"value":"always"}`)},
		"delete": {http.MethodDelete, "/admin/users/7/settings/values/playback.subtitle_mode?scope=account", nil},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := env.do(t, "user", req.method, req.target, req.body); rec.Code != http.StatusForbidden {
				t.Errorf("non-admin %s = %d, want 403: %s", name, rec.Code, rec.Body.String())
			}
			if rec := env.do(t, "", req.method, req.target, req.body); rec.Code != http.StatusUnauthorized {
				t.Errorf("anonymous %s = %d, want 401: %s", name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdminSettingValuesRejectNonexistentLibraryContext(t *testing.T) {
	env := newAdminValuesEnv(t)
	env.handler.SetLibraryLookup(settingValuesLibraryLookup{existing: map[int]bool{7: true}})

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		var body []byte
		if method == http.MethodPut {
			body = []byte(`{"value":"de"}`)
		}
		rec := env.do(t, "admin", method,
			"/admin/users/7/settings/values/playback.subtitle_language?scope=profile_library&profile_id=profile-1&library_id=99",
			body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s nonexistent library = %d, want 404: %s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminListShowsAnotherUsersValuesAcrossScopes(t *testing.T) {
	env := newAdminValuesEnv(t)
	ctx := context.Background()

	seeded := map[settingscontract.Scope]userstore.SettingIdentity{
		settingscontract.ScopeAccount: {
			Key: "catalog.metadata_language", Scope: settingscontract.ScopeAccount,
		},
		settingscontract.ScopeProfile: {
			Key: "playback.subtitle_mode", Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
		},
		settingscontract.ScopeProfileDevice: {
			Key: "playback.subtitle_language", Scope: settingscontract.ScopeProfileDevice,
			ProfileID: "profile-1", DeviceID: "tv-1",
		},
		settingscontract.ScopeProfileLibrary: {
			Key: "playback.subtitle_language", Scope: settingscontract.ScopeProfileLibrary,
			ProfileID: "profile-1", LibraryID: 42,
		},
		settingscontract.ScopeProfileSeries: {
			Key: "playback.subtitle_language", Scope: settingscontract.ScopeProfileSeries,
			ProfileID: "profile-1", SeriesID: "s-1",
		},
	}
	values := map[settingscontract.Scope]string{
		settingscontract.ScopeAccount:        `"de"`,
		settingscontract.ScopeProfile:        `"always"`,
		settingscontract.ScopeProfileDevice:  `"en"`,
		settingscontract.ScopeProfileLibrary: `"fr"`,
		settingscontract.ScopeProfileSeries:  `"ja"`,
	}
	for scope, id := range seeded {
		if _, err := env.targetStore.UpsertSettingValue(ctx, id, json.RawMessage(values[scope])); err != nil {
			t.Fatalf("seeding %s: %v", scope, err)
		}
	}
	// A value in the admin's own store must not leak into the target's list.
	if _, err := env.adminStore.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: "playback.subtitle_mode", Scope: settingscontract.ScopeAccount,
	}, json.RawMessage(`"off"`)); err != nil {
		t.Fatalf("seeding admin store: %v", err)
	}

	rec := env.do(t, "admin", http.MethodGet, "/admin/users/7/settings/values", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Values   []settingValueResponse `json:"values"`
		Revision int                    `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Values) != len(seeded) {
		t.Fatalf("listed %d values, want %d: %s", len(body.Values), len(seeded), rec.Body.String())
	}
	contract, _ := settingscontract.Load()
	if body.Revision != contract.Revision {
		t.Errorf("revision = %d, want %d", body.Revision, contract.Revision)
	}
	for _, got := range body.Values {
		want, ok := seeded[settingscontract.Scope(got.Scope)]
		if !ok {
			t.Errorf("unexpected scope %q in list", got.Scope)
			continue
		}
		if got.Key != want.Key || got.ProfileID != want.ProfileID ||
			got.DeviceID != want.DeviceID || got.LibraryID != want.LibraryID ||
			got.SeriesID != want.SeriesID {
			t.Errorf("listed identity at %s = %+v, want %+v", got.Scope, got, want)
		}
		if string(got.Value) != values[settingscontract.Scope(got.Scope)] {
			t.Errorf("value at %s = %s, want %s", got.Scope, got.Value, values[settingscontract.Scope(got.Scope)])
		}
	}

	// A user with no store is a 404, not an empty list pretending to be truth.
	if rec := env.do(t, "admin", http.MethodGet, "/admin/users/99/settings/values", nil); rec.Code != http.StatusNotFound {
		t.Errorf("list for unknown user = %d, want 404", rec.Code)
	}
}

func TestAdminSetAndDeleteAtExplicitScopeRoundTrip(t *testing.T) {
	env := newAdminValuesEnv(t)
	ctx := context.Background()
	target := "/admin/users/7/settings/values/playback.subtitle_language" +
		"?scope=profile_device&profile_id=profile-1&device_id=tv-1"
	identity := userstore.SettingIdentity{
		Key: "playback.subtitle_language", Scope: settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "tv-1",
	}

	rec := env.do(t, "admin", http.MethodPut, target, []byte(`{"value":"de"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	var stored settingValueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decoding PUT response: %v", err)
	}
	if string(stored.Value) != `"de"` || stored.Scope != "profile_device" ||
		stored.ProfileID != "profile-1" || stored.DeviceID != "tv-1" {
		t.Errorf("PUT stored %+v, want \"de\" at profile-1/tv-1", stored)
	}

	// The write landed in the target user's store and only there.
	if got, err := env.targetStore.GetSettingValue(ctx, identity); err != nil || got == nil {
		t.Fatalf("target store value = %+v, %v; want stored", got, err)
	}
	if got, err := env.adminStore.GetSettingValue(ctx, identity); err != nil || got != nil {
		t.Errorf("admin store value = %+v, %v; want none", got, err)
	}

	if rec := env.do(t, "admin", http.MethodDelete, target, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := env.targetStore.GetSettingValue(ctx, identity); err != nil || got != nil {
		t.Errorf("value after delete = %+v, %v; want gone", got, err)
	}
	if rec := env.do(t, "admin", http.MethodDelete, target, nil); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", rec.Code)
	}
}

// TestAdminSetRejectsInvalidValueLikeTheSessionRoute pins that the admin write
// is the same validation path as /settings/values, not a second validator: an
// invalid value fails with the identical status, code and message.
func TestAdminSetRejectsInvalidValueLikeTheSessionRoute(t *testing.T) {
	env := newAdminValuesEnv(t)
	invalid := []byte(`{"value":"sideways"}`)

	adminRec := env.do(t, "admin", http.MethodPut,
		"/admin/users/7/settings/values/playback.subtitle_mode?scope=profile&profile_id=profile-1", invalid)
	if adminRec.Code != http.StatusBadRequest {
		t.Fatalf("admin PUT = %d, want 400: %s", adminRec.Code, adminRec.Body.String())
	}

	// The same write through the session route, as the target user.
	sessionReq := httptest.NewRequest(http.MethodPut,
		"/settings/values/playback.subtitle_mode?scope=profile", bytes.NewReader(invalid))
	sessionCtx := apimw.SetClaims(sessionReq.Context(), &auth.Claims{UserID: adminValuesTargetID})
	sessionReq = sessionReq.WithContext(apimw.SetProfileID(sessionCtx, "profile-1"))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("key", "playback.subtitle_mode")
	sessionReq = sessionReq.WithContext(context.WithValue(sessionReq.Context(), chi.RouteCtxKey, routeCtx))
	sessionRec := httptest.NewRecorder()
	env.handler.HandleSetValue(sessionRec, sessionReq)

	if sessionRec.Code != adminRec.Code {
		t.Errorf("status: session %d, admin %d", sessionRec.Code, adminRec.Code)
	}
	var adminErr, sessionErr errorResponse
	if err := json.Unmarshal(adminRec.Body.Bytes(), &adminErr); err != nil {
		t.Fatalf("decoding admin error: %v", err)
	}
	if err := json.Unmarshal(sessionRec.Body.Bytes(), &sessionErr); err != nil {
		t.Fatalf("decoding session error: %v", err)
	}
	if adminErr.Error != "invalid_value" {
		t.Errorf("admin error code = %q, want invalid_value", adminErr.Error)
	}
	if adminErr != sessionErr {
		t.Errorf("error bodies differ: admin %+v, session %+v", adminErr, sessionErr)
	}
}

func TestAdminSetRefusesUnknownKeysAndProfiles(t *testing.T) {
	env := newAdminValuesEnv(t)

	rec := env.do(t, "admin", http.MethodPut,
		"/admin/users/7/settings/values/totally.invented.key?scope=profile&profile_id=profile-1",
		[]byte(`{"value":"x"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown key = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	var unknownErr errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &unknownErr); err != nil {
		t.Fatalf("decoding unknown-key error: %v", err)
	}
	if unknownErr.Error != "unknown_setting" {
		t.Errorf("unknown key code = %q, want unknown_setting", unknownErr.Error)
	}

	// A client_local key is refused as server storage, same as the session route.
	rec = env.do(t, "admin", http.MethodPut,
		"/admin/users/7/settings/values/downloads.wifi_only?scope=profile&profile_id=profile-1",
		[]byte(`{"value":true}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("client_local key = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// A profile the target user does not have is a 404, which also keeps
	// Postgres's profile FK from turning the typo into a 500.
	rec = env.do(t, "admin", http.MethodPut,
		"/admin/users/7/settings/values/playback.subtitle_mode?scope=profile&profile_id=ghost",
		[]byte(`{"value":"always"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown profile = %d, want 404: %s", rec.Code, rec.Body.String())
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

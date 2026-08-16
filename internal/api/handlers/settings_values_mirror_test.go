package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The intro-skip pair is one preference stored under two keys for one release:
// revision 7 replaced the boolean playback.auto_skip_intro with the three-way
// playback.intro_skip_mode, and every shipped client still reads the boolean.
// These tests hold the canonical write path to keeping the two in step, because
// a household whose two keys disagree gets a different intro behavior on every
// device it owns.

// storedValueAt reads one explicit row, whatever scope it lives at.
func storedValueAt(
	t *testing.T, store userstore.UserStore, identity userstore.SettingIdentity,
) *userstore.SettingValue {
	t.Helper()
	value, err := store.GetSettingValue(context.Background(), identity)
	if err != nil {
		t.Fatalf("reading %s at %s: %v", identity.Key, identity.Scope, err)
	}
	return value
}

func profileIdentity(key string) userstore.SettingIdentity {
	return userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	}
}

func deviceIdentity(key string) userstore.SettingIdentity {
	return userstore.SettingIdentity{
		Key: key, Scope: settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "device-1",
	}
}

// requireStored asserts a row exists and holds want.
func requireStored(
	t *testing.T, store userstore.UserStore, identity userstore.SettingIdentity, want string,
) {
	t.Helper()
	value := storedValueAt(t, store, identity)
	if value == nil {
		t.Fatalf("no %s row at %s", identity.Key, identity.Scope)
	}
	if string(value.Value) != want {
		t.Errorf("%s at %s = %s, want %s", identity.Key, identity.Scope, value.Value, want)
	}
}

// TestWritingTheDeprecatedBooleanMirrorsTheEnum: an unmigrated client saves the
// switch and a current client must read the mode the household actually chose,
// not the contract default.
func TestWritingTheDeprecatedBooleanMirrorsTheEnum(t *testing.T) {
	for name, tc := range map[string]struct{ body, wantMode string }{
		"on":  {`{"value":true}`, `"always"`},
		"off": {`{"value":false}`, `"ask"`},
	} {
		t.Run(name, func(t *testing.T) {
			handler, store := newValuesTestHandler(t)

			rec := routeValues(t, handler, http.MethodPut,
				settingskeys.PlaybackAutoSkipIntro, "scope=profile", []byte(tc.body))
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
			}

			requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), tc.wantMode)

			// The response is the key the caller addressed, unchanged: the
			// mirror is a side effect, not a redirect.
			var response settingValueResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decoding response: %v; body=%s", err, rec.Body.String())
			}
			if response.Key != settingskeys.PlaybackAutoSkipIntro {
				t.Errorf("response key = %s, want the key that was written", response.Key)
			}
		})
	}
}

// TestWritingTheEnumMirrorsTheDeprecatedBoolean covers all three modes,
// including the one the boolean cannot express.
func TestWritingTheEnumMirrorsTheDeprecatedBoolean(t *testing.T) {
	for _, tc := range []struct{ mode, wantBool string }{
		{"never", `false`},
		{"ask", `false`},
		{"always", `true`},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			handler, store := newValuesTestHandler(t)

			rec := routeValues(t, handler, http.MethodPut,
				settingskeys.PlaybackIntroSkipMode, "scope=profile",
				[]byte(`{"value":"`+tc.mode+`"}`))
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT %q = %d: %s", tc.mode, rec.Code, rec.Body.String())
			}

			requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode),
				`"`+tc.mode+`"`)
			requireStored(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro), tc.wantBool)
		})
	}
}

// TestTheMirrorFollowsTheIdentity, not just the key: a device override must not
// silently become a profile-wide one.
func TestTheMirrorFollowsTheIdentity(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackIntroSkipMode, "scope=profile_device", []byte(`{"value":"always"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("device PUT = %d: %s", rec.Code, rec.Body.String())
	}

	requireStored(t, store, deviceIdentity(settingskeys.PlaybackAutoSkipIntro), `true`)
	if value := storedValueAt(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro)); value != nil {
		t.Errorf("a device write left a profile-scope mirror row: %s", value.Value)
	}
}

// TestDeletingEitherHalfClearsBoth. A row left behind would go on resolving as
// an explicit choice at the very scope the caller asked to inherit at.
func TestDeletingEitherHalfClearsBoth(t *testing.T) {
	for _, deleted := range []string{
		settingskeys.PlaybackIntroSkipMode,
		settingskeys.PlaybackAutoSkipIntro,
	} {
		t.Run(deleted, func(t *testing.T) {
			handler, store := newValuesTestHandler(t)

			if rec := routeValues(t, handler, http.MethodPut,
				settingskeys.PlaybackIntroSkipMode, "scope=profile",
				[]byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
				t.Fatalf("seed PUT = %d: %s", rec.Code, rec.Body.String())
			}

			rec := routeValues(t, handler, http.MethodDelete, deleted, "scope=profile", nil)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("DELETE %s = %d: %s", deleted, rec.Code, rec.Body.String())
			}

			for _, key := range []string{
				settingskeys.PlaybackIntroSkipMode,
				settingskeys.PlaybackAutoSkipIntro,
			} {
				if value := storedValueAt(t, store, profileIdentity(key)); value != nil {
					t.Errorf("%s survived a DELETE of %s: %s", key, deleted, value.Value)
				}
			}
		})
	}
}

// TestDeletingAnAbsentValueTouchesNeitherHalf: the 404 for "nothing set here"
// still has to mean nothing was written, mirror included.
func TestDeletingAnAbsentValueTouchesNeitherHalf(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackAutoSkipIntro, "scope=profile",
		[]byte(`{"value":true}`)); rec.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d: %s", rec.Code, rec.Body.String())
	}

	// Nothing was ever written at device scope, so this clears nothing —
	// including the profile-scope pair, which belongs to another identity.
	rec := routeValues(t, handler, http.MethodDelete,
		settingskeys.PlaybackIntroSkipMode, "scope=profile_device", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE of an unset device value = %d, want 404: %s", rec.Code, rec.Body.String())
	}

	requireStored(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro), `true`)
	requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), `"always"`)
}

// TestMirroredWritesReplayInsteadOfDoubleApplying. The mirror rides inside the
// mutation's transaction, so a retried write must re-serve the receipt and
// leave both rows at the revision the first write produced.
func TestMirroredWritesReplayInsteadOfDoubleApplying(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	send := func(mutationID, value string) *httptest.ResponseRecorder {
		req := valuesRequest(http.MethodPut,
			"/settings/values/"+settingskeys.PlaybackIntroSkipMode+"?scope=profile",
			[]byte(`{"value":`+value+`}`))
		req.Header.Set(mutationIDHeader, mutationID)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("key", settingskeys.PlaybackIntroSkipMode)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rec := httptest.NewRecorder()
		handler.HandleSetValue(rec, req)
		return rec
	}

	if rec := send("mut-intro", `"always"`); rec.Code != http.StatusOK {
		t.Fatalf("first write = %d: %s", rec.Code, rec.Body.String())
	}
	mirrored := storedValueAt(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro))
	if mirrored == nil {
		t.Fatal("the first write stored no mirror row")
	}

	replay := send("mut-intro", `"always"`)
	if replay.Code != http.StatusOK || replay.Header().Get("X-Silo-Idempotent-Replay") != "true" {
		t.Fatalf("replay = %d header %q: %s",
			replay.Code, replay.Header().Get("X-Silo-Idempotent-Replay"), replay.Body.String())
	}

	after := storedValueAt(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro))
	if after == nil {
		t.Fatal("the replay removed the mirror row")
	}
	if after.Revision != mirrored.Revision {
		t.Errorf("mirror revision moved from %d to %d on replay; the replay wrote again",
			mirrored.Revision, after.Revision)
	}
	if string(after.Value) != `true` {
		t.Errorf("mirror value = %s after replay, want true", after.Value)
	}
}

// TestProfileScopeEnumWriteUpdatesTheLegacyColumn. GET /profiles still serves
// auto_skip_intro from user_profiles, so a household that switches intros off
// through the new key must not keep seeing the old answer in the profile DTO.
func TestProfileScopeEnumWriteUpdatesTheLegacyColumn(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if err := store.UpdateProfile(context.Background(), "profile-1",
		userstore.UpdateProfileInput{AutoSkipIntro: boolPtr(true)}); err != nil {
		t.Fatalf("seeding the column: %v", err)
	}

	rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackIntroSkipMode, "scope=profile", []byte(`{"value":"never"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("reading profile: %v", err)
	}
	if profile.AutoSkipIntro {
		t.Error("user_profiles.auto_skip_intro still true after intro_skip_mode was set to never")
	}

	// And back the other way, so the column tracks the mode rather than only
	// ever being cleared.
	if rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackIntroSkipMode, "scope=profile",
		[]byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
		t.Fatalf("second PUT = %d: %s", rec.Code, rec.Body.String())
	}
	profile, err = store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("re-reading profile: %v", err)
	}
	if !profile.AutoSkipIntro {
		t.Error("user_profiles.auto_skip_intro still false after intro_skip_mode was set to always")
	}
}

// TestDeviceScopeEnumWriteLeavesTheProfileColumnAlone: the column is
// profile-wide, and one television's override is not the household's choice.
func TestDeviceScopeEnumWriteLeavesTheProfileColumnAlone(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackIntroSkipMode, "scope=profile_device", []byte(`{"value":"always"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("reading profile: %v", err)
	}
	if profile.AutoSkipIntro {
		t.Error("a device-scope write moved the profile-wide column")
	}
}

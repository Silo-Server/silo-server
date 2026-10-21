package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

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
			if err := store.UpdateProfile(t.Context(), "profile-1", userstore.UpdateProfileInput{AutoSkipIntro: boolPtr(tc.wantBool != "true")}); err != nil {
				t.Fatal(err)
			}

			rec := routeValues(t, handler, http.MethodPut,
				settingskeys.PlaybackIntroSkipMode, "scope=profile",
				[]byte(`{"value":"`+tc.mode+`"}`))
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT %q = %d: %s", tc.mode, rec.Code, rec.Body.String())
			}

			requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode),
				`"`+tc.mode+`"`)
			requireStored(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro), tc.wantBool)
			requireColumn(t, store, tc.wantBool == "true", "after the enum write")
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
	requireColumn(t, store, false, "after a device-scope write")
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

			requireColumn(t, store, true, "after the seeding write")

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
			requireColumn(t, store, false, "after the profile-scope value was cleared")
		})
	}
}

// requireColumn asserts what user_profiles.auto_skip_intro holds, which is
// still what GET /profiles serves and what the web player reads.
func requireColumn(t *testing.T, store userstore.UserStore, want bool, when string) {
	t.Helper()
	profile, err := store.GetProfile(context.Background(), "profile-1")
	if err != nil || profile == nil {
		t.Fatalf("reading profile: %v", err)
	}
	if profile.AutoSkipIntro != want {
		t.Errorf("user_profiles.auto_skip_intro = %v %s, want %v",
			profile.AutoSkipIntro, when, want)
	}
}

// TestABooleanWriteCorrectsAColumnTheEnumMoved. The column has to be movable
// from both halves of the pair, or the first enum write pins it: an old client
// that then saves the switch stores its value in the canonical rows and reads
// the opposite back out of the profile DTO.
func TestABooleanWriteCorrectsAColumnTheEnumMoved(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackIntroSkipMode, "scope=profile",
		[]byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
		t.Fatalf("enum PUT = %d: %s", rec.Code, rec.Body.String())
	}
	requireColumn(t, store, true, "after intro_skip_mode was set to always")

	if rec := routeValues(t, handler, http.MethodPut,
		settingskeys.PlaybackAutoSkipIntro, "scope=profile",
		[]byte(`{"value":false}`)); rec.Code != http.StatusOK {
		t.Fatalf("boolean PUT = %d: %s", rec.Code, rec.Body.String())
	}
	requireStored(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro), `false`)
	requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), `"ask"`)
	requireColumn(t, store, false, "after the switch was turned off")
}

// TestClearingADeviceOverrideLeavesTheProfileColumnAlone: the column follows the
// profile-wide choice, and giving up one television's override does not revoke
// it.
func TestClearingADeviceOverrideLeavesTheProfileColumnAlone(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	for _, scope := range []string{"scope=profile", "scope=profile_device"} {
		if rec := routeValues(t, handler, http.MethodPut,
			settingskeys.PlaybackIntroSkipMode, scope,
			[]byte(`{"value":"always"}`)); rec.Code != http.StatusOK {
			t.Fatalf("seed PUT at %s = %d: %s", scope, rec.Code, rec.Body.String())
		}
	}
	requireColumn(t, store, true, "after the profile-scope write")

	if rec := routeValues(t, handler, http.MethodDelete,
		settingskeys.PlaybackIntroSkipMode, "scope=profile_device", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("device DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	requireColumn(t, store, true, "after a device override was cleared")
	requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), `"always"`)
}

// --- Atomicity ---
//
// A mirrored write is three writes: the addressed row, its companion, and (at
// profile scope) the legacy column GET /profiles serves. Landing some of them
// is worse than landing none. The pair disagreeing is exactly the drift the
// mirror exists to prevent, and the client has no way to notice, let alone
// repair it — a 500 tells it the write failed while a preference it never chose
// is now stored.

// failingMirrorStore injects a failure into one write inside the mutation
// transaction, standing in for a store that dies partway through it.
type failingMirrorStore struct {
	userstore.UserStore
	failUpsertKey  string
	failProfileCol bool
}

func (s failingMirrorStore) WithSettingMutationTransaction(
	ctx context.Context,
	mutationID string,
	fn func(userstore.SettingMutationWriter) error,
) error {
	transactioner, ok := s.UserStore.(userstore.SettingMutationTransactioner)
	if !ok {
		return errors.New("wrapped store does not support setting mutation transactions")
	}
	return transactioner.WithSettingMutationTransaction(ctx, mutationID,
		func(writer userstore.SettingMutationWriter) error {
			return fn(failingMirrorWriter{
				SettingMutationWriter: writer,
				failUpsertKey:         s.failUpsertKey,
				failProfileCol:        s.failProfileCol,
			})
		})
}

type failingMirrorWriter struct {
	userstore.SettingMutationWriter
	failUpsertKey  string
	failProfileCol bool
}

func (w failingMirrorWriter) UpsertSettingValue(
	ctx context.Context, id userstore.SettingIdentity, value json.RawMessage,
) (*userstore.SettingValue, error) {
	if w.failUpsertKey != "" && id.Key == w.failUpsertKey {
		return nil, errors.New("storage unavailable")
	}
	return w.SettingMutationWriter.UpsertSettingValue(ctx, id, value)
}

func (w failingMirrorWriter) UpdateProfile(
	ctx context.Context, id string, u userstore.UpdateProfileInput,
) error {
	if w.failProfileCol {
		return errors.New("profile storage unavailable")
	}
	return w.SettingMutationWriter.UpdateProfile(ctx, id, u)
}

func handlerOverStore(t *testing.T, store userstore.UserStore) *SettingValuesHandler {
	t.Helper()
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	return NewSettingValuesHandler(testUserStoreProvider{store: store}, contract)
}

// TestAFailedHalfRollsBackTheWholeMirroredWrite covers the path with no
// idempotency key, which is the one shipped clients take: without a receipt to
// replay, a half-applied write is not repaired by any retry the client makes.
func TestAFailedHalfRollsBackTheWholeMirroredWrite(t *testing.T) {
	for name, failure := range map[string]failingMirrorStore{
		// The pair is written in key order, so failing the enum fails the
		// second row and proves the first one was rolled back rather than
		// merely never attempted.
		"the addressed row fails after its companion landed": {
			failUpsertKey: settingskeys.PlaybackIntroSkipMode,
		},
		"the companion row fails": {
			failUpsertKey: settingskeys.PlaybackAutoSkipIntro,
		},
		"the legacy profile column fails": {failProfileCol: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, store := newValuesTestHandler(t)
			failure.UserStore = store
			handler := handlerOverStore(t, failure)

			rec := routeValues(t, handler, http.MethodPut,
				settingskeys.PlaybackIntroSkipMode, "scope=profile", []byte(`{"value":"always"}`))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("PUT = %d, want 500: %s", rec.Code, rec.Body.String())
			}

			for _, key := range []string{
				settingskeys.PlaybackIntroSkipMode,
				settingskeys.PlaybackAutoSkipIntro,
			} {
				if value := storedValueAt(t, store, profileIdentity(key)); value != nil {
					t.Errorf("%s survived a failed write: %s", key, value.Value)
				}
			}
			requireColumn(t, store, false, "after a failed write")
		})
	}
}

// TestClearingSweepsUpAStrayCompanion. A companion with no addressed row beside
// it is only reachable through a partial failure from before this path was
// transactional. Leaving it standing makes that state permanent: every retry
// answers 404 for the row the caller named without ever touching the one that
// is still resolving. The 404 stays — the caller asked about a row that is not
// there — but the stray goes.
func TestClearingSweepsUpAStrayCompanion(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if _, err := store.UpsertSettingValue(context.Background(),
		profileIdentity(settingskeys.PlaybackAutoSkipIntro), json.RawMessage(`true`)); err != nil {
		t.Fatalf("seeding the stray companion: %v", err)
	}

	rec := routeValues(t, handler, http.MethodDelete,
		settingskeys.PlaybackIntroSkipMode, "scope=profile", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if value := storedValueAt(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro)); value != nil {
		t.Errorf("the stray companion survived: %s", value.Value)
	}
}

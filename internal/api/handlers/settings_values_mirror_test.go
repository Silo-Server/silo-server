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

			seamSet(t, seamCtx(t, "profile-1"), handler,
				seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfile), `"`+tc.mode+`"`)

			requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), `"`+tc.mode+`"`)
			requireStored(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro), tc.wantBool)
			requireColumn(t, store, tc.wantBool == "true", "after the enum write")
		})
	}
}

// TestTheMirrorFollowsTheIdentity, not just the key: a device override must not
// silently become a profile-wide one.
func TestTheMirrorFollowsTheIdentity(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	seamSet(t, seamCtx(t, "profile-1"), handler,
		seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfileDevice), `"always"`)

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
			ctx := seamCtx(t, "profile-1")

			seamSet(t, ctx, handler,
				seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfile), `"always"`)
			requireColumn(t, store, true, "after the seeding write")

			if err := handler.DeleteSettingValue(ctx, seamUserID, seamReq(deleted, settingscontract.ScopeProfile)); err != nil {
				t.Fatalf("delete %s: %v", deleted, err)
			}
			for _, key := range []string{
				settingskeys.PlaybackIntroSkipMode,
				settingskeys.PlaybackAutoSkipIntro,
			} {
				if value := storedValueAt(t, store, profileIdentity(key)); value != nil {
					t.Errorf("%s survived a delete of %s: %s", key, deleted, value.Value)
				}
			}
			requireColumn(t, store, false, "after the profile-scope value was cleared")
		})
	}
}

// TestABooleanWriteCorrectsAColumnTheEnumMoved. The column has to be movable
// from both halves of the pair, or the first enum write pins it: an old client
// that then saves the switch stores its value in the canonical rows and reads
// the opposite back out of the profile DTO.
func TestABooleanWriteCorrectsAColumnTheEnumMoved(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")

	seamSet(t, ctx, handler, seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfile), `"always"`)
	requireColumn(t, store, true, "after intro_skip_mode was set to always")

	seamSet(t, ctx, handler, seamReq(settingskeys.PlaybackAutoSkipIntro, settingscontract.ScopeProfile), `false`)
	requireStored(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro), `false`)
	requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), `"ask"`)
	requireColumn(t, store, false, "after the switch was turned off")
}

// TestClearingADeviceOverrideLeavesTheProfileColumnAlone: the column follows the
// profile-wide choice, and giving up one television's override does not revoke
// it.
func TestClearingADeviceOverrideLeavesTheProfileColumnAlone(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")

	for _, scope := range []settingscontract.Scope{settingscontract.ScopeProfile, settingscontract.ScopeProfileDevice} {
		seamSet(t, ctx, handler, seamReq(settingskeys.PlaybackIntroSkipMode, scope), `"always"`)
	}
	requireColumn(t, store, true, "after the profile-scope write")

	if err := handler.DeleteSettingValue(ctx, seamUserID,
		seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfileDevice)); err != nil {
		t.Fatalf("device delete: %v", err)
	}
	requireColumn(t, store, true, "after a device override was cleared")
	requireStored(t, store, profileIdentity(settingskeys.PlaybackIntroSkipMode), `"always"`)
}

// TestAFailedHalfRollsBackTheWholeMirroredWrite: a mirrored write is the
// addressed row, its companion and (at profile scope) the legacy column.
// Landing some of them is worse than landing none, and a write without a
// mutation id leaves no receipt a retry could repair the half-applied state from.
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

			_, err := handler.SetSettingValue(seamCtx(t, "profile-1"), seamUserID,
				seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfile), json.RawMessage(`"always"`))
			requireSettingStatus(t, err, http.StatusInternalServerError, "failed mirrored write")

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
// transactional, and leaving it standing makes that state permanent. The
// not-found answer stays, since the caller named a row that is not there, but
// the stray goes.
func TestClearingSweepsUpAStrayCompanion(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if _, err := store.UpsertSettingValue(context.Background(),
		profileIdentity(settingskeys.PlaybackAutoSkipIntro), json.RawMessage(`true`)); err != nil {
		t.Fatalf("seeding the stray companion: %v", err)
	}

	err := handler.DeleteSettingValue(seamCtx(t, "profile-1"), seamUserID,
		seamReq(settingskeys.PlaybackIntroSkipMode, settingscontract.ScopeProfile))
	requireSettingStatus(t, err, http.StatusNotFound, "delete of the absent addressed row")
	if value := storedValueAt(t, store, profileIdentity(settingskeys.PlaybackAutoSkipIntro)); value != nil {
		t.Errorf("the stray companion survived: %s", value.Value)
	}
}

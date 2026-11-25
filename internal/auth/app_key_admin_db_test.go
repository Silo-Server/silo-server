package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/models"
)

// seededKeys are a person key, an active device key and a waiting device
// key on one account.
type seededKeys struct {
	person, device, waiting string
}

// seedKeys gives userID a live person key (keeping one it already has), an
// active device key and a waiting one, stored directly.
func (e *keyEnv) seedKeys(t *testing.T, userID int) seededKeys {
	t.Helper()
	ctx := t.Context()
	person, err := e.signIn.keys.UnrevokedPersonKey(ctx, userID)
	if errors.Is(err, ErrAppKeyNotFound) {
		person, err = e.signIn.keys.InsertPersonKey(ctx, NewPersonKey{UserID: userID, PublicKey: newTestKey(t).pub, EnrolledVia: models.AppKeyEnrolledSession})
	}
	if err != nil {
		t.Fatal(err)
	}
	out := seededKeys{person: person.ID}
	for _, status := range []string{models.DeviceKeyStatusActive, models.DeviceKeyStatusPending} {
		key, err := e.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: userID, PublicKey: newTestKey(t).pub, Kind: models.DeviceKeyKindDevice,
			Name: "Seeded " + status, Status: status, EnrolledVia: models.AppKeyEnrolledIntroduce, PersonKeyID: &person.ID})
		if err != nil {
			t.Fatal(err)
		}
		if status == models.DeviceKeyStatusActive {
			out.device = key.ID
		} else {
			out.waiting = key.ID
		}
	}
	return out
}

// serverOwner returns the server's Owner, making one of the env's admin
// accounts the Owner when the database has none. Only one account can be
// the Owner, so a test shares the one that exists.
func (e *keyEnv) serverOwner(t *testing.T) int {
	t.Helper()
	ctx := t.Context()
	var id int
	err := e.pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		owner := e.localAccount(t, "adminkeys-owner", models.RoleAdmin)
		if _, err := e.pool.Exec(ctx, `UPDATE users SET is_owner = true WHERE id = $1`, owner.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = e.pool.Exec(context.WithoutCancel(ctx), `UPDATE users SET is_owner = false WHERE id = $1`, owner.ID)
		})
		return owner.ID
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Who may change another account's keys, one row per operation per kind of
// caller: any administrator acts on an ordinary account and on their own,
// only the Owner on another administrator, nobody but the Owner on the
// Owner, and a disabled administrator or an account that is not an
// administrator on none. A key of another account and a missing account are
// not found. Which account a scoped API key or "view as user" acts as is
// handlers.TestAdminKeyActor.
func TestAdminAppKeyAuthorityDB(t *testing.T) {
	env := newKeyEnv(t, "adminkeyauth")
	admin := env.localAccount(t, "adminkeyauth-admin", models.RoleAdmin)
	otherAdmin := env.localAccount(t, "adminkeyauth-other", models.RoleAdmin)
	user := env.localAccount(t, "adminkeyauth-user", models.RoleUser)
	owner := env.serverOwner(t)
	disabled := env.localAccount(t, "adminkeyauth-disabled", models.RoleAdmin)
	if _, err := env.pool.Exec(t.Context(), `UPDATE users SET enabled = false WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	stranger := env.seedKeys(t, otherAdmin.ID)

	ops := []struct {
		name string
		call func(ctx context.Context, actor, target int, keys seededKeys) error
	}{
		{"revoke device key", func(ctx context.Context, actor, target int, keys seededKeys) error {
			return env.signIn.AdminRevokeDeviceKey(ctx, actor, target, keys.device)
		}},
		{"revoke person key", func(ctx context.Context, actor, target int, _ seededKeys) error {
			return env.signIn.AdminRevokePersonKey(ctx, actor, target)
		}},
		{"deny waiting key", func(ctx context.Context, actor, target int, keys seededKeys) error {
			return env.signIn.AdminDenyPendingKey(ctx, actor, target, keys.waiting)
		}},
	}
	callers := []struct {
		name          string
		actor, target int
		// seed gives the target keys first; refused rows act on made-up IDs.
		seed bool
		// keys overrides the seeded IDs, for a key of another account.
		keys *seededKeys
		want error
	}{
		{name: "the Owner on another admin", actor: owner, target: otherAdmin.ID, seed: true},
		{name: "an admin on an ordinary account", actor: admin.ID, target: user.ID, seed: true},
		{name: "an admin on their own account", actor: admin.ID, target: admin.ID, seed: true},
		{name: "an admin on another admin", actor: admin.ID, target: otherAdmin.ID, want: ErrAdminProtected},
		{name: "an admin on the Owner", actor: admin.ID, target: owner, want: ErrOwnerProtected},
		{name: "an account that is not an admin", actor: user.ID, target: user.ID, want: ErrAdminProtected},
		{name: "a disabled admin on an ordinary account", actor: disabled.ID, target: user.ID, want: ErrAdminProtected},
		{name: "a key of another account", actor: admin.ID, target: user.ID, keys: &stranger, want: ErrAppKeyNotFound},
		{name: "a missing account", actor: admin.ID, target: -1, want: ErrNotFound},
	}
	for _, op := range ops {
		for _, tc := range callers {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				keys := seededKeys{person: uuid.NewString(), device: uuid.NewString(), waiting: uuid.NewString()}
				if tc.seed {
					keys = env.seedKeys(t, tc.target)
				}
				if tc.keys != nil {
					keys = *tc.keys
				}
				want := tc.want
				// The person key is the account's own, so naming another
				// account's keys doesn't apply to it.
				if op.name == "revoke person key" && tc.keys != nil {
					want = nil
				}
				if err := op.call(t.Context(), tc.actor, tc.target, keys); !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		}
	}
}

// Removing a device from the Users page signs that device out and stops its
// key: its session ends, its next key sign-in fails as an unknown key would,
// and the account's other sessions stay. The removal is audited once;
// removing it again changes nothing.
func TestAdminRevokeDeviceKeyEndsItsSessionDB(t *testing.T) {
	env := newKeyEnv(t, "adminkeyrevoke")
	ctx := t.Context()
	admin := env.localAccount(t, "adminkeyrevoke-admin", models.RoleAdmin)
	user := env.localAccount(t, "adminkeyrevoke-user", models.RoleUser)
	_, device, enrolled := env.enroll(t, user)
	_, otherDevice, other := env.enroll(t, env.localAccount(t, "adminkeyrevoke-other", models.RoleUser))
	password := env.loginSession(t, user, models.SessionOpenedPassword, nil)
	auditCtx, _ := audittest.Context(t, env.pool, audittest.Request{Method: http.MethodDelete,
		Pattern: "/api/v2/admin/users/{user_id}/keys/devices/{id}", Path: "/api/v2/admin/users/" + strconv.Itoa(user.ID) + "/keys/devices/" + enrolled.DeviceID,
		UserID: &admin.ID, OptIn: true})
	if err := env.signIn.AdminRevokeDeviceKey(auditCtx, admin.ID, user.ID, enrolled.DeviceID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if env.sessionLive(t, enrolled.Pair.SessionID, user.ID) {
		t.Fatal("the revoked key's session is still live")
	}
	if !env.sessionLive(t, password.SessionID, user.ID) || !env.sessionLive(t, other.Pair.SessionID, other.User.ID) {
		t.Fatal("revoking one key ended another session")
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("the revoked key's sign-in = %v", err)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: otherDevice})); err != nil {
		t.Fatalf("another account's key stopped signing in: %v", err)
	}
	if err := env.signIn.AdminRevokeDeviceKey(auditCtx, admin.ID, user.ID, enrolled.DeviceID); err != nil {
		t.Fatalf("revoking again: %v", err)
	}
	row := audittest.One(t, env.pool, auditmutation.ActionDeviceKeyRevoked, auditmutation.TargetUser, strconv.Itoa(user.ID))
	if got := audittest.Value(row.Change(t, auditFieldDeviceKey).Before); got != `"`+enrolled.DeviceID+`"` {
		t.Fatalf("audited device key = %s", got)
	}
}

// Revoking the person key stops it vouching for new devices and denies the
// devices it introduced that are still waiting, which may be the work of a
// copy of it. Devices already enrolled keep signing in. A repeat, with no
// person key left, succeeds and records nothing more.
func TestAdminRevokePersonKeyDB(t *testing.T) {
	env := newKeyEnv(t, "adminkeyperson")
	ctx := t.Context()
	admin := env.localAccount(t, "adminkeyperson-admin", models.RoleAdmin)
	person, device, enrolled := env.enroll(t, admin)
	waiting, waitingDevice := env.introduceWaiting(t, person)
	auditCtx, _ := audittest.Context(t, env.pool, audittest.Request{Method: http.MethodDelete,
		Pattern: "/api/v2/admin/users/{user_id}/keys/person", Path: "/api/v2/admin/users/" + strconv.Itoa(admin.ID) + "/keys/person",
		UserID: &admin.ID, OptIn: true})
	if err := env.signIn.AdminRevokePersonKey(auditCtx, admin.ID, admin.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !env.sessionLive(t, enrolled.Pair.SessionID, admin.ID) {
		t.Fatal("revoking the person key ended a device's session")
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); err != nil {
		t.Fatalf("an enrolled device stopped signing in: %v", err)
	}
	fresh := newTestKey(t)
	if _, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &fresh})); !errors.Is(err, ErrKeySignatureInvalid) {
		t.Fatalf("introduce with the revoked person key = %v", err)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: waitingDevice})); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("the waiting device it introduced = %v", err)
	}
	row := audittest.One(t, env.pool, auditmutation.ActionPersonKeyRevoked, auditmutation.TargetUser, strconv.Itoa(admin.ID))
	if got := audittest.Value(row.Change(t, auditFieldDeniedDeviceKeys).After); got != "1" {
		t.Fatalf("audited denied devices = %s (waiting key %s)", got, waiting)
	}
	if err := env.signIn.AdminRevokePersonKey(auditCtx, admin.ID, admin.ID); err != nil {
		t.Fatalf("revoking again: %v", err)
	}
	audittest.One(t, env.pool, auditmutation.ActionPersonKeyRevoked, auditmutation.TargetUser, strconv.Itoa(admin.ID))
}

// The administrator's list shows what the person's own list hides: a key
// whose provider account is gone, marked dead. Denied keys are left out of
// both.
func TestAdminListShowsDeadKeysDB(t *testing.T) {
	env := newKeyEnv(t, "adminkeylist")
	ctx := t.Context()
	_, enrolled := env.enrollFromProvider(t)
	gone, _ := env.otherIdentity(t, "adminkeylist-gone")
	dead, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: env.user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindDevice, Name: "Dead", EnrolledVia: models.AppKeyEnrolledSession, IdentityID: &gone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `DELETE FROM plugin_auth_identities WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	denied, err := env.signIn.keys.InsertDeviceKey(ctx, NewDeviceKey{UserID: env.user.ID, PublicKey: newTestKey(t).pub,
		Kind: models.DeviceKeyKindDevice, Name: "Denied", Status: models.DeviceKeyStatusDenied, EnrolledVia: models.AppKeyEnrolledSession})
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := env.listedKeys(t, dead.ID); listed {
		t.Fatal("the person's own list shows a dead key")
	}
	keys, err := env.signIn.AdminListKeys(ctx, env.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, key := range keys.Devices {
		listed[key.ID] = key.IdentityGone
	}
	if gone, ok := listed[dead.ID]; !ok || !gone {
		t.Fatalf("dead key listed = %v, marked gone = %v", ok, gone)
	}
	if gone, ok := listed[enrolled.DeviceID]; !ok || gone {
		t.Fatalf("live key listed = %v, marked gone = %v", ok, gone)
	}
	if _, ok := listed[denied.ID]; ok {
		t.Fatal("a denied key is listed")
	}
	if keys.Person == nil || keys.Person.IdentityGone || !keys.Person.ProviderBound {
		t.Fatalf("person key = %+v", keys.Person)
	}
	if _, err := env.signIn.AdminListKeys(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing account = %v", err)
	}
}

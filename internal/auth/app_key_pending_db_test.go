package auth

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/models"
)

// introduceWaiting introduces a new device to an administrator's account
// with person and returns its waiting key's ID and the device key.
func (e *keyEnv) introduceWaiting(t *testing.T, person testKey) (string, testKey) {
	t.Helper()
	device := newTestKey(t)
	result, err := e.signIn.Introduce(t.Context(), e.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &device}))
	if err != nil || result.Status != appkeys.ReplyApprovalRequired {
		t.Fatalf("introduce = %+v, %v", result, err)
	}
	return result.DeviceID, device
}

// pendingIDs lists the IDs of the account's waiting keys as claims see them.
func (e *keyEnv) pendingIDs(t *testing.T, claims *Claims) []string {
	t.Helper()
	keys, err := e.signIn.ListPendingKeys(t.Context(), claims)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, key.ID)
	}
	return ids
}

// The approval is what turns a waiting key into a credential for an
// administrator account: approved from one of the admin's own signed-in
// devices, the device's next sign-in opens a session, the decision is
// audited, and approving again changes nothing. A session can't approve the
// key that opened it.
func TestPendingKeyApproveDB(t *testing.T) {
	env := newKeyEnv(t, "keyapprove")
	ctx := t.Context()
	admin := env.localAccount(t, "keyapprove-admin", models.RoleAdmin)
	person, _, enrolled := env.enroll(t, admin)
	ownDevice := &Claims{UserID: admin.ID, Role: admin.Role, SessionID: enrolled.Pair.SessionID, TokenType: TokenTypeAccess}
	first, _ := env.introduceWaiting(t, person)
	id, device := env.introduceWaiting(t, person)
	if got := env.pendingIDs(t, ownDevice); len(got) != 2 || got[0] != first || got[1] != id {
		t.Fatalf("waiting keys = %v, want oldest first [%s %s]", got, first, id)
	}
	auditCtx, _ := audittest.Context(t, env.pool, audittest.Request{Method: http.MethodPost,
		Pattern: "/api/v2/auth/keys/pending/{id}/approve", Path: "/api/v2/auth/keys/pending/" + id + "/approve", UserID: &admin.ID, OptIn: true})
	if err := env.signIn.ApprovePendingKey(auditCtx, ownDevice, id, ActingProfile{}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	row := audittest.One(t, env.pool, auditmutation.ActionDeviceKeyApproved, auditmutation.TargetUser, strconv.Itoa(admin.ID))
	if got := audittest.Value(row.Change(t, auditFieldDeviceKey).After); got != `"`+id+`"` {
		t.Fatalf("audited device key = %s", got)
	}
	result, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device}))
	if err != nil || result.Status != appkeys.ReplySignedIn || result.Pair == nil {
		t.Fatalf("sign-in after approval = %+v, %v", result, err)
	}
	if got := env.pendingIDs(t, ownDevice); len(got) != 1 || got[0] != first {
		t.Fatalf("waiting keys after approval = %v", got)
	}
	if err := env.signIn.ApprovePendingKey(ctx, ownDevice, id, ActingProfile{}); err != nil {
		t.Fatalf("approving an active key again: %v", err)
	}
	if err := env.signIn.ApprovePendingKey(ctx, ownDevice, enrolled.DeviceID, ActingProfile{}); !errors.Is(err, ErrKeyDecidesItself) {
		t.Fatalf("a session approving its own key = %v", err)
	}
}

// Replacing the person key is how a person cuts off a copy of it. A device
// the old key introduced that is still waiting may be the copy's work, so it
// is refused with the key: it can no longer be approved, and its sign-in
// answers key_denied. A device already approved keeps its key.
func TestReplacingPersonKeyDeniesItsWaitingDevicesDB(t *testing.T) {
	env := newKeyEnv(t, "keyreplacewaiting")
	ctx := t.Context()
	admin := env.localAccount(t, "keyreplacewaiting-admin", models.RoleAdmin)
	person, _, enrolled := env.enroll(t, admin)
	claims := env.loginSession(t, admin, models.SessionOpenedPassword, nil)
	id, device := env.introduceWaiting(t, person)
	if err := env.signIn.ReplacePersonKey(ctx, claims, newTestKey(t).pub, ActingProfile{}); err != nil {
		t.Fatal(err)
	}
	if err := env.signIn.ApprovePendingKey(ctx, claims, id, ActingProfile{}); !errors.Is(err, ErrAppKeyNotFound) {
		t.Fatalf("approving a device the replaced key introduced = %v", err)
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("its sign-in = %v", err)
	}
	if env.keyRevoked(t, enrolled.DeviceID) {
		t.Fatal("replacing the person key revoked a device that was already enrolled")
	}
}

// A denied device is refused at its next sign-in and is gone from both
// lists; denying it again changes nothing, and it can't be approved after.
func TestPendingKeyDenyDB(t *testing.T) {
	env := newKeyEnv(t, "keydeny")
	ctx := t.Context()
	admin := env.localAccount(t, "keydeny-admin", models.RoleAdmin)
	person, _, _ := env.enroll(t, admin)
	claims := env.loginSession(t, admin, models.SessionOpenedPassword, nil)
	id, device := env.introduceWaiting(t, person)
	auditCtx, _ := audittest.Context(t, env.pool, audittest.Request{Method: http.MethodPost,
		Pattern: "/api/v2/auth/keys/pending/{id}/deny", Path: "/api/v2/auth/keys/pending/" + id + "/deny", UserID: &admin.ID, OptIn: true})
	if err := env.signIn.DenyPendingKey(auditCtx, claims, id); err != nil {
		t.Fatalf("deny: %v", err)
	}
	audittest.One(t, env.pool, auditmutation.ActionDeviceKeyDenied, auditmutation.TargetUser, strconv.Itoa(admin.ID))
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("sign-in after denial = %v", err)
	}
	if got := env.pendingIDs(t, claims); len(got) != 0 {
		t.Fatalf("denied key still waiting: %v", got)
	}
	keys, err := env.signIn.ListOwnKeys(ctx, claims)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys.Devices {
		if key.ID == id {
			t.Fatal("denied key is in the key list")
		}
	}
	if err := env.signIn.DenyPendingKey(ctx, claims, id); err != nil {
		t.Fatalf("denying again: %v", err)
	}
	if err := env.signIn.ApprovePendingKey(ctx, claims, id, ActingProfile{}); !errors.Is(err, ErrAppKeyNotFound) {
		t.Fatalf("approving a denied key = %v", err)
	}
}

// Who and what can't be approved: a key past its 24 hours, another account's
// key (nor seen or denied by it), an account no longer an administrator, and
// sessions that may not decide (a TV's, another app's, one older than the
// record).
func TestPendingKeyRefusalsDB(t *testing.T) {
	env := newKeyEnv(t, "keypendno")
	ctx := t.Context()
	admin := env.localAccount(t, "keypendno-admin", models.RoleAdmin)
	other := env.localAccount(t, "keypendno-other", models.RoleAdmin)
	member := env.localAccount(t, "keypendno-member", models.RoleUser)
	person, _, _ := env.enroll(t, admin)
	claims := env.loginSession(t, admin, models.SessionOpenedPassword, nil)

	t.Run("past its 24 hours", func(t *testing.T) {
		id, _ := env.introduceWaiting(t, person)
		if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW() - INTERVAL '25 hours' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if err := env.signIn.ApprovePendingKey(ctx, claims, id, ActingProfile{}); !errors.Is(err, ErrAppKeyNotFound) {
			t.Fatalf("approve = %v", err)
		}
		for _, waiting := range env.pendingIDs(t, claims) {
			if waiting == id {
				t.Fatal("an expired key is listed as waiting")
			}
		}
	})
	// Listing and denying need no administrator account: another account,
	// an administrator or not, lists nothing and is told a key it does not
	// own is not found, never refused for its role. Approving is for an
	// administrator, which a non-administrator is told first.
	for _, tc := range []struct {
		name     string
		stranger *models.User
		approve  error
	}{
		{"another administrator", other, ErrAppKeyNotFound},
		{"another account, not an administrator", member, ErrApproverNotAdmin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := env.introduceWaiting(t, person)
			session := env.loginSession(t, tc.stranger, models.SessionOpenedPassword, nil)
			if got := env.pendingIDs(t, session); len(got) != 0 {
				t.Fatalf("another account sees %v", got)
			}
			if err := env.signIn.ApprovePendingKey(ctx, session, id, ActingProfile{}); !errors.Is(err, tc.approve) {
				t.Fatalf("approve = %v, want %v", err, tc.approve)
			}
			if err := env.signIn.DenyPendingKey(ctx, session, id); !errors.Is(err, ErrAppKeyNotFound) {
				t.Fatalf("deny = %v", err)
			}
		})
	}
	t.Run("session kinds", func(t *testing.T) {
		id, _ := env.introduceWaiting(t, person)
		for name, via := range map[string]string{"device sign-in": models.SessionOpenedDeviceLogin, "older than the record": ""} {
			session := env.loginSession(t, admin, via, nil)
			if _, err := env.signIn.ListPendingKeys(ctx, session); !errors.Is(err, ErrSessionNotAllowed) {
				t.Fatalf("%s: list = %v", name, err)
			}
			if err := env.signIn.ApprovePendingKey(ctx, session, id, ActingProfile{}); !errors.Is(err, ErrSessionNotAllowed) {
				t.Fatalf("%s: approve = %v", name, err)
			}
			if err := env.signIn.DenyPendingKey(ctx, session, id); !errors.Is(err, ErrSessionNotAllowed) {
				t.Fatalf("%s: deny = %v", name, err)
			}
		}
	})
	t.Run("demoted from administrator", func(t *testing.T) {
		id, _ := env.introduceWaiting(t, person)
		if _, err := env.pool.Exec(ctx, `UPDATE users SET role = 'user' WHERE id = $1`, admin.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = env.pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, admin.ID) })
		if err := env.signIn.ApprovePendingKey(ctx, claims, id, ActingProfile{}); !errors.Is(err, ErrApproverNotAdmin) {
			t.Fatalf("approve = %v", err)
		}
	})
}

// Anyone holding an administrator's person key can introduce devices, so the
// waiting keys an account holds are capped: the tenth is accepted, the
// eleventh refused, and a key past its 24 hours doesn't count.
func TestPendingKeyCapDB(t *testing.T) {
	env := newKeyEnv(t, "keypendcap")
	ctx := t.Context()
	admin := env.localAccount(t, "keypendcap-admin", models.RoleAdmin)
	person, _, _ := env.enroll(t, admin)
	var oldest string
	for i := range MaxPendingKeys {
		id, _ := env.introduceWaiting(t, person)
		if i == 0 {
			oldest = id
		}
	}
	eleventh := newTestKey(t)
	introduce := func() error {
		_, err := env.signIn.Introduce(ctx, env.request(t, message{purpose: appkeys.PurposeIntroduce, signer: person, device: &eleventh}))
		return err
	}
	if err := introduce(); !errors.Is(err, ErrPendingKeyLimit) {
		t.Fatalf("eleventh waiting key = %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW() - INTERVAL '25 hours' WHERE id = $1`, oldest); err != nil {
		t.Fatal(err)
	}
	if err := introduce(); err != nil {
		t.Fatalf("with one waiting key expired: %v", err)
	}
}

// The retention step revokes a waiting key past its 24 hours, keeping the
// row, and leaves a fresh waiting key and an active key alone. The swept
// device still learns it was not approved.
func TestExpiredPendingKeySweepDB(t *testing.T) {
	env := newKeyEnv(t, "keysweep")
	ctx := t.Context()
	admin := env.localAccount(t, "keysweep-admin", models.RoleAdmin)
	person, _, enrolled := env.enroll(t, admin)
	expired, device := env.introduceWaiting(t, person)
	fresh, _ := env.introduceWaiting(t, person)
	if _, err := env.pool.Exec(ctx, `UPDATE auth_device_keys SET created_at = NOW() - INTERVAL '25 hours' WHERE id = $1`, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := env.signIn.keys.RevokeExpiredPendingKeys(ctx); err != nil {
		t.Fatal(err)
	}
	revoked := func(id string) bool {
		t.Helper()
		var gone bool
		if err := env.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM auth_device_keys WHERE id = $1`, id).Scan(&gone); err != nil {
			t.Fatal(err)
		}
		return gone
	}
	if !revoked(expired) || revoked(fresh) || revoked(enrolled.DeviceID) {
		t.Fatalf("revoked: expired %v, fresh %v, active %v", revoked(expired), revoked(fresh), revoked(enrolled.DeviceID))
	}
	if _, err := env.signIn.SignIn(ctx, env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})); !errors.Is(err, ErrAppKeyDenied) {
		t.Fatalf("swept key's sign-in = %v", err)
	}
}

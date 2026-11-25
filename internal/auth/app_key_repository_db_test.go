package auth

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

func newAppPublicKey(t *testing.T) appkeys.PublicKey {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := appkeys.PublicKeyFromBytes(priv.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// Revoking a device key must end the sessions it opened on their next check,
// and leave the account's other sign-ins alone. A public key is enrolled
// once per server: neither table takes it again, even after revocation.
func TestDeviceKeyRevocationEndsItsSessionsDB(t *testing.T) {
	f := newSignInFixture(t)
	ctx := t.Context()
	keys := NewAppKeyRepository(f.pool)
	user := f.account(models.RoleUser)

	public := newAppPublicKey(t)
	device, err := keys.InsertDeviceKey(ctx, NewDeviceKey{
		UserID: user, PublicKey: public, Kind: models.DeviceKeyKindDevice, Name: "Phone", Platform: "ios",
		EnrolledVia: models.AppKeyEnrolledSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := keys.DeviceKeyByKeyID(ctx, public.KeyID()); err != nil || found.ID != device.ID {
		t.Fatalf("lookup by key ID = %v, %v", found, err)
	}

	keySession := uuid.NewString()
	if err := f.sessions.Create(ctx, models.AuthSession{
		ID: keySession, UserID: user, DeviceName: "Phone", ExpiresAt: time.Now().Add(time.Hour), DeviceKeyID: &device.ID,
	}); err != nil {
		t.Fatal(err)
	}
	f.owners[keySession] = user
	passwordSession := f.session(user, nil)
	if stored, err := f.sessions.GetByID(ctx, keySession); err != nil || stored.DeviceKeyID == nil || *stored.DeviceKeyID != device.ID {
		t.Fatalf("session device key = %v, %v", stored, err)
	}
	if !f.active(keySession) || !f.active(passwordSession) {
		t.Fatal("sessions inactive before revocation")
	}

	if err := keys.RevokeDeviceKey(ctx, user, device.ID); err != nil {
		t.Fatal(err)
	}
	if f.active(keySession) {
		t.Fatal("the revoked key's session is still active")
	}
	if !f.active(passwordSession) {
		t.Fatal("revoking a device key ended the account's password session")
	}

	if _, err := keys.InsertDeviceKey(ctx, NewDeviceKey{
		UserID: user, PublicKey: public, Kind: models.DeviceKeyKindDevice, Name: "Phone", EnrolledVia: models.AppKeyEnrolledSession,
	}); !errors.Is(err, ErrAppKeyAlreadyEnrolled) {
		t.Fatalf("re-enrolling a revoked device key: err = %v", err)
	}
	if _, err := keys.InsertPersonKey(ctx, NewPersonKey{
		UserID: f.account(models.RoleUser), PublicKey: public, EnrolledVia: models.AppKeyEnrolledSession,
	}); !errors.Is(err, ErrAppKeyAlreadyEnrolled) {
		t.Fatalf("enrolling a device key as a person key: err = %v", err)
	}
}

// The rules the sign-in code relies on when it enrolls and revokes for a
// whole account: an account holds one live person key, a device key can only
// be vouched for by a live person key of its own account, and revoking the
// account's keys ends every session they opened.
func TestAccountKeyRulesDB(t *testing.T) {
	f := newSignInFixture(t)
	ctx := t.Context()
	keys := NewAppKeyRepository(f.pool)
	user, other := f.account(models.RoleUser), f.account(models.RoleUser)

	person, err := keys.InsertPersonKey(ctx, NewPersonKey{UserID: user, PublicKey: newAppPublicKey(t), EnrolledVia: models.AppKeyEnrolledSession})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.InsertPersonKey(ctx, NewPersonKey{
		UserID: user, PublicKey: newAppPublicKey(t), EnrolledVia: models.AppKeyEnrolledSession,
	}); !errors.Is(err, ErrPersonKeyExists) {
		t.Fatalf("second live person key: err = %v", err)
	}
	vouched := func(account int) NewDeviceKey {
		return NewDeviceKey{
			UserID: account, PublicKey: newAppPublicKey(t), Kind: models.DeviceKeyKindDevice, Name: "Phone",
			EnrolledVia: models.AppKeyEnrolledIntroduce, PersonKeyID: &person.ID,
		}
	}
	if _, err := keys.InsertDeviceKey(ctx, vouched(other)); !errors.Is(err, ErrAppKeyNotFound) {
		t.Fatalf("device key vouched for by another account's person key: err = %v", err)
	}
	device, err := keys.InsertDeviceKey(ctx, vouched(user))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.Create(ctx, models.AuthSession{
		UserID: other, DeviceName: "Phone", ExpiresAt: time.Now().Add(time.Hour), DeviceKeyID: &device.ID,
	}); err == nil {
		t.Fatal("a session of one account took another account's device key")
	}

	keySession := uuid.NewString()
	if err := f.sessions.Create(ctx, models.AuthSession{
		ID: keySession, UserID: user, DeviceName: "Phone", ExpiresAt: time.Now().Add(time.Hour), DeviceKeyID: &device.ID,
	}); err != nil {
		t.Fatal(err)
	}
	f.owners[keySession] = user
	if err := keys.RevokeAccountKeys(ctx, user); err != nil {
		t.Fatal(err)
	}
	if f.active(keySession) {
		t.Fatal("revoking the account's keys left a key session active")
	}
	if _, err := keys.InsertDeviceKey(ctx, vouched(user)); !errors.Is(err, ErrAppKeyRevoked) {
		t.Fatalf("device key vouched for by a revoked person key: err = %v", err)
	}
}

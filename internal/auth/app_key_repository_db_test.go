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

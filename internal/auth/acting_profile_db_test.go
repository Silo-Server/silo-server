package auth

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A route checks the main profile's PIN before its operation's transaction
// starts, and the transaction may then wait for the account. Each operation
// that creates lasting access checks the profile again under the account
// lock: it refuses when the PIN changed in between, when another write holds
// the profile's row, and when the profile is gone, and it changes nothing.
// With the profile as it now stands, each goes through.
func TestKeyOperationsRecheckActingProfileDB(t *testing.T) {
	env := newKeyEnv(t, "keyacting")
	ctx := t.Context()
	admin := env.localAccount(t, "keyacting-admin", models.RoleAdmin)
	person, _, _ := env.enroll(t, admin)
	claims := env.loginSession(t, admin, models.SessionOpenedPassword, nil)
	waiting, _ := env.introduceWaiting(t, person)
	devices := NewDeviceLoginService(env.pool, NewUserRepository(env.pool), env.jwt, NewSessionRepository(env.pool), nil, nil)

	checked := ActingProfile{ID: uuid.NewString()}
	if _, err := env.pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary, pin_hash)
		VALUES ($1, $2, 'Main', true, 'hash-of-1234')`, checked.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	current := checked
	if err := env.pool.QueryRow(ctx, `UPDATE user_profiles SET pin_hash = 'hash-of-4321'
		WHERE user_id = $1 AND id = $2 RETURNING pin_revision`, admin.ID, checked.ID).Scan(&current.PINRevision); err != nil {
		t.Fatal(err)
	}
	if current.PINRevision == checked.PINRevision {
		t.Fatal("changing the PIN did not advance its revision")
	}

	replacement := newTestKey(t)
	operations := []struct {
		name string
		run  func(ActingProfile) error
	}{
		{"enroll", func(acting ActingProfile) error {
			device := newTestKey(t)
			_, err := env.signIn.Enroll(ctx, KeyEnrollRequest{
				KeyRequest: env.request(t, message{purpose: appkeys.PurposeEnroll, signer: person, device: &device}),
				PersonKey:  person.pub, Claims: claims, Acting: acting,
			})
			return err
		}},
		{"approve in advance", func(acting ActingProfile) error {
			_, binding := preapprovalSecret(t)
			_, err := devices.Preapprove(WithClaims(ctx, claims), DevicePreapprovalInput{DeviceName: "TV", Binding: binding, Acting: acting}, admin.ID)
			return err
		}},
		{"approve a waiting device", func(acting ActingProfile) error {
			return env.signIn.ApprovePendingKey(ctx, claims, waiting, acting)
		}},
		// Last: a new person key refuses the devices the old one introduced.
		{"replace the person key", func(acting ActingProfile) error {
			return env.signIn.ReplacePersonKey(ctx, claims, replacement.pub, acting)
		}},
	}
	refused := func(t *testing.T, why string, acting ActingProfile) {
		t.Helper()
		for _, op := range operations {
			if err := op.run(acting); !errors.Is(err, ErrActingProfileChanged) {
				t.Fatalf("%s, %s = %v, want ErrActingProfileChanged", op.name, why, err)
			}
		}
	}

	refused(t, "checked against the PIN before it changed", checked)
	refused(t, "as a profile that is gone", ActingProfile{ID: uuid.NewString()})

	// A profile write in progress: the operation doesn't wait for it.
	writer, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `UPDATE user_profiles SET name = 'Renamed' WHERE user_id = $1 AND id = $2`, admin.ID, checked.ID); err != nil {
		t.Fatal(err)
	}
	refused(t, "while another write holds the profile", current)
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if got := env.pendingIDs(t, claims); len(got) != 1 || got[0] != waiting {
		t.Fatalf("waiting keys after the refusals = %v, want [%s]", got, waiting)
	}
	var keys, preapprovals int
	if err := env.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM auth_device_keys WHERE user_id = $1 AND status = 'active'),
		(SELECT count(*) FROM device_login_requests WHERE approved_by_user_id = $1 AND preapproved_at IS NOT NULL)`,
		admin.ID).Scan(&keys, &preapprovals); err != nil {
		t.Fatal(err)
	}
	if keys != 1 || preapprovals != 0 {
		t.Fatalf("after the refusals: %d active keys and %d sign-ins approved in advance, want 1 and 0", keys, preapprovals)
	}

	for _, op := range operations {
		if err := op.run(current); err != nil {
			t.Fatalf("%s as the profile stands now: %v", op.name, err)
		}
	}
}

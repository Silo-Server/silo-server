package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/secret"
)

type appleFinalizerFixture struct {
	notifications.PushDeviceStore
	receipt   notifications.ApplePushReceipt
	err       error
	commitErr error
	before    func()
	calls     int
}

func (f *appleFinalizerFixture) ApplyApplePushAndFinalize(_ context.Context, _ notifications.ApplePushCommand, _ *secret.Cipher, finish func(notifications.ApplePushReceipt) error) (notifications.ApplePushReceipt, error) {
	f.calls++
	if f.err != nil {
		return notifications.ApplePushReceipt{}, f.err
	}
	if f.before != nil {
		f.before()
	}
	if err := finish(f.receipt); err != nil {
		return notifications.ApplePushReceipt{}, err
	}
	return f.receipt, f.commitErr
}

type appleIssuerFixture struct {
	calls                  int
	err                    error
	session, profile, role string
}

func (f *appleIssuerFixture) GenerateApplePushDisplayToken(_ int, role, session, profile string, _ *int) (string, time.Time, error) {
	f.calls++
	f.session = session
	f.profile = profile
	f.role = role
	return "synthetic-display", time.Now().Add(time.Hour), f.err
}
func TestOrderedAppleDisplayRenewalAndAuthority(t *testing.T) {
	for _, name := range []string{"renew", "disabled", "removed", "conflict", "mint_failure", "revoked_before", "revoked_after_wait", "pin_after_wait", "role_after_wait", "owner_mismatch", "commit_failure", "account_disabled_after_wait", "pin_rejected_after_wait"} {
		t.Run(name, func(t *testing.T) {
			store := &appleFinalizerFixture{receipt: notifications.ApplePushReceipt{Generation: 1, RegistrationID: "registration", ServerDeviceID: "server-device", Enabled: true}}
			issuer := new(appleIssuerFixture)
			sessions := &socketSessionFixture{valid: true}
			users := &socketUserFixture{user: models.User{ID: 7, Enabled: true, Role: "user"}}
			viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true, PolicyRevision: 1}}
			h := NewOrderedApplePushV2(notifications.NewPushDeviceService(store, handlerPushCipher(t)), issuer, sessions, users, viewer, func(context.Context, int, string) (bool, bool, error) { return false, true, nil })
			cmd := notifications.ApplePushCommand{UserID: 7, ProfileID: "profile", Generation: 1}
			identity := evt.SocketIdentity{UserID: 7, ProfileID: "profile", SessionID: "session", Role: "user", ProfileToken: "original-pin", AccessExpiresAt: time.Now().Add(time.Minute)}
			wantErr, wantMint := false, true
			switch name {
			case "disabled":
				store.receipt.Enabled = false
				wantMint = false
			case "removed":
				store.receipt.Enabled = false
				store.receipt.Removed = true
				wantMint = false
			case "conflict":
				store.err = notifications.ErrPushGenerationConflict
				wantErr = true
				wantMint = false
			case "commit_failure":
				store.commitErr = errors.New("synthetic commit failure")
				wantErr = true
			case "mint_failure":
				issuer.err = errors.New("synthetic mint failure")
			case "revoked_before":
				sessions.valid = false
				wantErr = true
				wantMint = false
			case "revoked_after_wait":
				store.before = func() { sessions.valid = false }
				wantErr = true
				wantMint = false
			case "pin_after_wait":
				store.before = func() { viewer.scope.PolicyRevision++ }
				wantErr = true
				wantMint = false
			case "role_after_wait":
				store.before = func() { users.user.Role = "admin" }
				wantErr = true
				wantMint = false
			case "account_disabled_after_wait":
				store.before = func() { users.user.Enabled = false }
				wantErr = true
				wantMint = false
			case "pin_rejected_after_wait":
				store.before = func() { viewer.err = errors.New("PIN replaced") }
				wantErr = true
				wantMint = false
			case "owner_mismatch":
				cmd.UserID++
				wantErr = true
				wantMint = false
			}
			receipt, token, expiry, err := h.RegisterApplePush(t.Context(), cmd, identity)
			if (err != nil) != wantErr {
				t.Fatalf("err=%v want=%v", err, wantErr)
			}
			if (issuer.calls == 1) != wantMint {
				t.Fatalf("mint calls=%d", issuer.calls)
			}
			if wantErr || !wantMint || name == "mint_failure" {
				if token != "" || !expiry.IsZero() {
					t.Fatal("refused/failed mint leaked credential")
				}
			} else if token == "" || expiry.IsZero() || issuer.session != "session" || issuer.profile != "profile" {
				t.Fatal("missing captured display credential")
			}
			if !wantErr && receipt != store.receipt {
				t.Fatal("registration receipt changed")
			}
			if name == "renew" {
				again, token, _, err := h.RegisterApplePush(t.Context(), cmd, identity)
				if err != nil || token == "" || again != receipt || issuer.calls != 2 {
					t.Fatal("unchanged replay cannot renew")
				}
			}
		})
	}
}

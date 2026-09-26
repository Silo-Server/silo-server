package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type emailVerificationServiceStore struct {
	calls   int
	intent  EmailVerificationIntent
	receipt EmailVerificationReceipt
	err     error
}

func (f *emailVerificationServiceStore) QueueVerification(_ context.Context, in EmailVerificationIntent, _ *secret.Cipher) (EmailVerificationReceipt, error) {
	f.calls++
	f.intent = in
	return f.receipt, f.err
}
func TestEmailVerificationServiceAdmissionOnly(t *testing.T) {
	for _, name := range []string{"valid", "http", "missing_profile", "child", "missing_base", "invalid_scheme", "credentials", "query", "fragment", "unavailable", "conflict", "replay_without_base"} {
		t.Run(name, func(t *testing.T) {
			f := &emailVerificationServiceStore{receipt: EmailVerificationReceipt{ID: "intent", ExpiresAt: time.Now().Add(time.Hour), Current: true}}
			profile := &userstore.Profile{Name: "Current name"}
			base := "https://example.test/silo/"
			s := &EmailVerificationService{store: f, cipher: testPushCipher(t), profile: func(_ context.Context, user int, id string) *userstore.Profile {
				if user != 7 || id != "profile" {
					t.Fatal("lost acting authority")
				}
				return profile
			}, linkBase: func(context.Context) string { return base }}
			switch name {
			case "http":
				base = "http://example.test"
			case "missing_profile":
				profile = nil
			case "child":
				profile.IsChild = true
			case "missing_base", "replay_without_base":
				base = ""
			case "invalid_scheme":
				base = "file:///path"
			case "credentials":
				base = "https://name:password@example.test"
			case "query":
				base = "https://example.test?other=true"
			case "fragment":
				base = "https://example.test#part"
			case "unavailable":
				s.cipher = nil
			case "conflict":
				f.err = ErrEmailVerificationConflict
			}
			wantAllowed := name != "missing_profile" && name != "child" && name != "unavailable"
			if got := s.EmailVerificationAllowed(t.Context(), 7, "profile"); got != wantAllowed {
				t.Fatalf("capability permission = %v, want %v", got, wantAllowed)
			}
			receipt, err := s.QueueEmailVerification(t.Context(), 7, "profile", "intent", "address@example.test")
			if name == "missing_profile" || name == "child" || name == "unavailable" {
				if err == nil || f.calls != 0 {
					t.Fatal("refused request reached outbox")
				}
				return
			}
			if name == "conflict" {
				if !errors.Is(err, ErrEmailVerificationConflict) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || receipt != f.receipt || f.calls != 1 || f.intent.ID != "intent" || f.intent.ProfileName != "Current name" {
				t.Fatal("admission", err)
			}
			switch name {
			case "valid":
				if f.intent.LinkBase != "https://example.test/silo" {
					t.Fatal(f.intent.LinkBase)
				}
			case "http":
				if f.intent.LinkBase != base {
					t.Fatal("http refused")
				}
			default:
				if f.intent.LinkBase != "" {
					t.Fatal("invalid base reached new admission")
				}
			}
		})
	}
}

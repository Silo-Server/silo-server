package notifications

import (
	"context"
	"net/url"
	"strings"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const emailVerificationHTTPScheme = "http"

type emailVerificationStore interface {
	QueueVerification(context.Context, EmailVerificationIntent, *secret.Cipher) (EmailVerificationReceipt, error)
}

// emailVerificationDispatch is the outbox drain the service nudges after
// admission. Its retry policy lives in email_verification_dispatch.go.
type emailVerificationDispatch interface {
	Available(context.Context) bool
	Nudge()
}

// EmailVerificationService admits durable requests. The receipt is admission;
// the dispatcher (when present) hands the retained message to the provider.
type EmailVerificationService struct {
	store    emailVerificationStore
	cipher   *secret.Cipher
	profile  func(context.Context, int, string) *userstore.Profile
	linkBase func(context.Context) string
	dispatch emailVerificationDispatch
}

func (s *EmailVerificationService) EmailVerificationAvailable() bool {
	return s != nil && s.store != nil && s.cipher != nil && s.profile != nil && s.linkBase != nil
}

// EmailVerificationAllowed uses the same profile authority as admission. The
// queue's configuration is reported separately from this effective permission.
func (s *EmailVerificationService) EmailVerificationAllowed(ctx context.Context, user int, profile string) bool {
	if !s.EmailVerificationAvailable() {
		return false
	}
	current := s.profile(ctx, user, profile)
	return current != nil && !current.IsChild
}

// EmailDispatchAvailable reports whether a dispatcher is wired and its provider
// currently accepts hand-offs. It never asserts that any message was delivered.
func (s *EmailVerificationService) EmailDispatchAvailable(ctx context.Context) bool {
	return s.EmailVerificationAvailable() && s.dispatch != nil && s.dispatch.Available(ctx)
}
func (s *EmailVerificationService) QueueEmailVerification(ctx context.Context, user int, profile, id, address string) (EmailVerificationReceipt, error) {
	if !s.EmailVerificationAvailable() {
		return EmailVerificationReceipt{}, ErrEmailVerificationUnavailable
	}
	current := s.profile(ctx, user, profile)
	if current == nil || current.IsChild {
		return EmailVerificationReceipt{}, ErrEmailChildProfile
	}
	// An invalid/removed external base blocks a NEW intent. The repository can
	// still return the original admitted receipt without rewriting its message.
	base := strings.TrimRight(s.linkBase(ctx), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != emailVerificationHTTPScheme && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		base = ""
	}
	receipt, err := s.store.QueueVerification(ctx, EmailVerificationIntent{ID: id, UserID: user, ProfileID: profile, Address: address, ProfileName: current.Name, LinkBase: base}, s.cipher)
	if err == nil && receipt.Current && s.dispatch != nil {
		// A replay nudges too: it is harmless, and the row may still be queued.
		s.dispatch.Nudge()
	}
	return receipt, err
}

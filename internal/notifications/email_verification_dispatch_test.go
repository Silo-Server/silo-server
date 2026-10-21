package notifications

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
)

// fakeVerificationSender records every provider hand-off. It never touches a
// network; err is returned from Send after the hand-off is recorded, which
// models "the provider may or may not have accepted".
type fakeVerificationSender struct {
	mu      sync.Mutex
	enabled bool
	err     error
	cancel  context.CancelFunc
	sent    []mail.Message
}

func (f *fakeVerificationSender) Enabled(context.Context) bool { return f.enabled }
func (f *fakeVerificationSender) Send(_ context.Context, msg mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.enabled {
		return mail.ErrNotConfigured
	}
	f.sent = append(f.sent, msg)
	if f.cancel != nil {
		f.cancel()
	}
	return f.err
}
func (f *fakeVerificationSender) messages() []mail.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mail.Message(nil), f.sent...)
}

func dispatchFixture(t *testing.T) (*EmailPrefsRepository, *emailVerificationDispatcher, *fakeVerificationSender, *clockStub) {
	t.Helper()
	repo, _, _ := emailOutboxFixture(t)
	sender := &fakeVerificationSender{enabled: true}
	d := newEmailVerificationDispatcher(repo, testPushCipher(t), sender)
	clock := &clockStub{t: time.Now().UTC().Truncate(time.Microsecond)}
	d.now = clock.now
	return repo, d, sender, clock
}

type clockStub struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockStub) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clockStub) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// The user-decided policy: a crash between provider hand-off and record leaves
// the row 'sending'. After the lease lapses (a restart, or a dead node) the
// row is retried exactly once with the same message id and link, and the
// second attempt is recorded with the attempt count. The duplicate is accepted.
func TestEmailVerificationDispatchRetriesUncertainSendOnceWithSameMessage(t *testing.T) {
	repo, d, sender, clock := dispatchFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
		t.Fatal(err)
	}
	// Losing the worker context after hand-off makes the real receipt write fail.
	interrupted, cancel := context.WithCancel(ctx)
	defer cancel()
	sender.cancel = cancel
	d.runPass(interrupted)
	state, err := readVerificationDispatchState(ctx, repo, in.ID)
	if err != nil || state.State != dispatchSending || state.Attempts != 1 || len(sender.messages()) != 1 {
		t.Fatalf("crash left %+v %v", state, err)
	}
	// Before the lease lapses the claim still belongs to the crashed worker.
	sender.cancel = nil
	d.runPass(ctx)
	if len(sender.messages()) != 1 {
		t.Fatal("retried inside lease")
	}
	clock.advance(emailVerificationClaimLease + time.Second)
	d.runPass(ctx)
	sent := sender.messages()
	if len(sent) != 2 || sent[0].TextBody != sent[1].TextBody || sent[0].Headers["Message-ID"] != sent[1].Headers["Message-ID"] || sent[1].To[0] != in.Address {
		t.Fatalf("retry changed message: %+v", sent)
	}
	state, err = readVerificationDispatchState(ctx, repo, in.ID)
	if err != nil || state.State != dispatchDelivered || state.Attempts != 2 {
		t.Fatalf("retry not recorded %+v %v", state, err)
	}
	// Both copies carry the one retained link, which still verifies once.
	hash := tokenHashFromMessage(t, sent[1])
	if outcome, err := repo.ConsumeVerifyToken(ctx, hash); err != nil || outcome != EmailVerifyOK {
		t.Fatal("retained link", outcome, err)
	}
}

func TestEmailVerificationDispatchStopsAfterSecondUncertainty(t *testing.T) {
	repo, d, sender, clock := dispatchFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		interrupted, cancel := context.WithCancel(ctx)
		sender.cancel = cancel
		d.runPass(interrupted)
		cancel()
		clock.advance(emailVerificationClaimLease + time.Second)
	}
	if n := len(sender.messages()); n != emailVerificationMaxAttempts {
		t.Fatalf("hand-offs %d, want %d", n, emailVerificationMaxAttempts)
	}
	state, err := readVerificationDispatchState(ctx, repo, in.ID)
	if err != nil || state.State != dispatchFailed || state.Attempts != 2 || state.Error == "" {
		t.Fatalf("%+v %v", state, err)
	}
	if replay, err := repo.QueueVerification(ctx, in, d.cipher); err != nil || !replay.Current {
		t.Fatal("dispatch failure altered admission receipt", err)
	}
}

type verificationDispatchState struct {
	State       string
	Attempts    int
	Error       string
	CompletedAt *time.Time
	HasPayload  bool
}

func readVerificationDispatchState(ctx context.Context, r *EmailPrefsRepository, id string) (verificationDispatchState, error) {
	var s verificationDispatchState
	err := r.pool.QueryRow(ctx, `SELECT dispatch_state,dispatch_attempts,dispatch_error,dispatch_completed_at,payload_ciphertext<>'' FROM notification_email_verifications WHERE id=$1`, id).Scan(&s.State, &s.Attempts, &s.Error, &s.CompletedAt, &s.HasPayload)
	return s, err
}

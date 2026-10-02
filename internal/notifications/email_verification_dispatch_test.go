package notifications

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/google/uuid"
)

// fakeVerificationSender records every provider hand-off. It never touches a
// network; err is returned from Send after the hand-off is recorded, which
// models "the provider may or may not have accepted".
type fakeVerificationSender struct {
	mu      sync.Mutex
	enabled bool
	err     error
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

func TestEmailVerificationDispatchDeliversRetainedMessageOnce(t *testing.T) {
	repo, d, sender, _ := dispatchFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
		t.Fatal(err)
	}
	d.runPass(ctx)
	d.runPass(ctx) // idle pass must not resend
	sent := sender.messages()
	if len(sent) != 1 || sent[0].To[0] != in.Address || sent[0].Headers["Message-ID"] != emailVerificationMessageID(in.ID) {
		t.Fatalf("hand-offs %+v", sent)
	}
	state, err := repo.VerificationDispatchState(ctx, in.ID)
	if err != nil || state.State != dispatchDelivered || state.Attempts != 1 || state.CompletedAt == nil || !state.HasPayload {
		t.Fatalf("%+v %v", state, err)
	}
	if replay, err := repo.QueueVerification(ctx, in, d.cipher); err != nil || !replay.Current {
		t.Fatal("delivery changed receipt", err)
	}
	if len(sender.messages()) != 1 {
		t.Fatal("replay resent")
	}
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
	d.afterSend = func(context.Context, string) error { return errEmailVerificationCrashInjected }
	d.runPass(ctx)
	state, err := repo.VerificationDispatchState(ctx, in.ID)
	if err != nil || state.State != dispatchSending || state.Attempts != 1 || len(sender.messages()) != 1 {
		t.Fatalf("crash left %+v %v", state, err)
	}
	// Before the lease lapses the claim still belongs to the crashed worker.
	d.afterSend = nil
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
	state, err = repo.VerificationDispatchState(ctx, in.ID)
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
	d.afterSend = func(context.Context, string) error { return errEmailVerificationCrashInjected }
	for range 3 {
		d.runPass(ctx)
		clock.advance(emailVerificationClaimLease + time.Second)
	}
	if n := len(sender.messages()); n != emailVerificationMaxAttempts {
		t.Fatalf("hand-offs %d, want %d", n, emailVerificationMaxAttempts)
	}
	state, err := repo.VerificationDispatchState(ctx, in.ID)
	if err != nil || state.State != dispatchFailed || state.Attempts != 2 || state.Error == "" {
		t.Fatalf("%+v %v", state, err)
	}
	if replay, err := repo.QueueVerification(ctx, in, d.cipher); err != nil || !replay.Current {
		t.Fatal("dispatch failure altered admission receipt", err)
	}
}

func TestEmailVerificationDispatchProviderRejectionRetriesOnceThenFails(t *testing.T) {
	repo, d, sender, clock := dispatchFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
		t.Fatal(err)
	}
	sender.err = errors.New("smtp send: 451 try again later")
	d.runPass(ctx)
	state, err := repo.VerificationDispatchState(ctx, in.ID)
	if err != nil || state.State != dispatchQueued || state.Attempts != 1 || state.Error == "" {
		t.Fatalf("first rejection %+v %v", state, err)
	}
	d.runPass(ctx)
	if len(sender.messages()) != 1 {
		t.Fatal("hot retry inside backoff")
	}
	clock.advance(emailVerificationClaimLease + time.Second)
	d.runPass(ctx)
	state, err = repo.VerificationDispatchState(ctx, in.ID)
	if err != nil || state.State != dispatchFailed || state.Attempts != 2 || len(sender.messages()) != 2 {
		t.Fatalf("second rejection %+v %v", state, err)
	}
	d.runPass(ctx)
	if len(sender.messages()) != 2 {
		t.Fatal("failed row resent")
	}
	// The provider going away leaves the row queued for a later pass.
	next := emailOutboxIntent()
	next.ProfileID = "second"
	if _, err := repo.QueueVerification(ctx, next, d.cipher); err != nil {
		t.Fatal(err)
	}
	sender.enabled = false
	d.runPass(ctx)
	state, err = repo.VerificationDispatchState(ctx, next.ID)
	if err != nil || state.State != dispatchQueued || state.Attempts != 1 {
		t.Fatalf("unconfigured %+v %v", state, err)
	}
	if d.Available(ctx) {
		t.Fatal("dispatch reported available without provider")
	}
	sender.enabled = true
	sender.err = nil
	clock.advance(emailVerificationClaimLease + time.Second)
	d.runPass(ctx)
	if state, err = repo.VerificationDispatchState(ctx, next.ID); err != nil || state.State != dispatchDelivered || state.Attempts != 2 {
		t.Fatalf("recovered %+v %v", state, err)
	}
}

func TestEmailVerificationDispatchAuthorizesUnderClaim(t *testing.T) {
	repo, d, sender, clock := dispatchFixture(t)
	ctx := t.Context()
	type authCase struct {
		name   string
		mutate func(in EmailVerificationIntent)
	}
	// Ordered: the expiry case moves the dispatcher clock past every link.
	cases := []authCase{
		{"cleared", func(in EmailVerificationIntent) {
			if err := repo.ClearCustomAddress(ctx, in.ProfileID); err != nil {
				t.Fatal(err)
			}
		}},
		{"superseded", func(in EmailVerificationIntent) {
			if _, err := repo.pool.Exec(ctx, `UPDATE notification_email_prefs SET pending_last_sent_at=now()-interval '1 day' WHERE profile_id=$1`, in.ProfileID); err != nil {
				t.Fatal(err)
			}
			next := in
			next.ID = uuid.NewString()
			next.Address = "next-" + in.ProfileID + "@example.test"
			if _, err := repo.QueueVerification(ctx, next, d.cipher); err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted", func(in EmailVerificationIntent) {
			if err := repo.DeleteForProfile(ctx, in.ProfileID); err != nil {
				t.Fatal(err)
			}
		}},
		{"expired", func(EmailVerificationIntent) { clock.advance(emailVerifyTTL + time.Minute) }},
	}
	for _, tc := range cases {
		name := tc.name
		in := emailOutboxIntent()
		in.ProfileID = name
		in.Address = name + "@example.test"
		if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
			t.Fatal(name, err)
		}
		tc.mutate(in)
		d.runPass(ctx)
		state, err := repo.VerificationDispatchState(ctx, in.ID)
		if err != nil || state.State != dispatchFailed || state.Error == "" {
			t.Fatalf("%s: %+v %v", name, state, err)
		}
		for _, m := range sender.messages() {
			if m.To[0] == in.Address {
				t.Fatalf("%s: unauthorized send", name)
			}
		}
	}
	// The superseding intent itself is still delivered.
	if len(sender.messages()) != 1 || sender.messages()[0].To[0] != "next-superseded@example.test" {
		t.Fatalf("%+v", sender.messages())
	}
}

func TestEmailVerificationDispatchConcurrentClaimsSendOnce(t *testing.T) {
	repo, d, sender, _ := dispatchFixture(t)
	ctx := t.Context()
	var intents []EmailVerificationIntent
	for i := range 5 {
		in := emailOutboxIntent()
		in.ProfileID = uuid.NewString()
		in.Address = "p" + string(rune('a'+i)) + "@example.test"
		if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
			t.Fatal(err)
		}
		intents = append(intents, in)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { d.runPass(ctx) })
	}
	wg.Wait()
	if n := len(sender.messages()); n != len(intents) {
		t.Fatalf("hand-offs %d for %d intents", n, len(intents))
	}
	for _, in := range intents {
		if state, err := repo.VerificationDispatchState(ctx, in.ID); err != nil || state.State != dispatchDelivered || state.Attempts != 1 {
			t.Fatalf("%+v %v", state, err)
		}
	}
}

func TestEmailVerificationDispatchRetention(t *testing.T) {
	repo, d, _, _ := dispatchFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	if _, err := repo.QueueVerification(ctx, in, d.cipher); err != nil {
		t.Fatal(err)
	}
	d.runPass(ctx)
	if payloads, rows, err := repo.RetireVerificationDispatch(ctx, time.Now()); err != nil || payloads != 0 || rows != 0 {
		t.Fatal("retired live row", payloads, rows, err)
	}
	// Admission replay judges expiry by wall-clock time, so expire in SQL
	// rather than through the dispatcher's clock.
	expire := func(id string, age time.Duration) {
		if _, err := repo.pool.Exec(ctx, `UPDATE notification_email_verifications SET expires_at=$2 WHERE id=$1`, id, time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	expire(in.ID, time.Minute)
	payloads, rows, err := repo.RetireVerificationDispatch(ctx, time.Now())
	if err != nil || payloads != 1 || rows != 0 {
		t.Fatal("payload retirement", payloads, rows, err)
	}
	state, err := repo.VerificationDispatchState(ctx, in.ID)
	if err != nil || state.HasPayload || state.State != dispatchDelivered {
		t.Fatalf("receipt lost with payload %+v %v", state, err)
	}
	// The receipt still answers an exact replay (current=false) rather than
	// admitting the old UUID as a new message.
	if replay, err := repo.QueueVerification(ctx, in, d.cipher); err != nil || replay.Current || replay.ID != in.ID {
		t.Fatal("receipt", replay, err)
	}
	// A queued row that expired without a worker fails rather than sending.
	late := emailOutboxIntent()
	late.ProfileID = "late"
	late.Address = "late@example.test"
	if _, err := repo.QueueVerification(ctx, late, d.cipher); err != nil {
		t.Fatal(err)
	}
	expire(late.ID, time.Minute)
	d.runPass(ctx)
	if state, err = repo.VerificationDispatchState(ctx, late.ID); err != nil || state.State != dispatchFailed || state.Attempts != 0 {
		t.Fatalf("expired queued row %+v %v", state, err)
	}
	expire(in.ID, emailVerificationReceiptRetention+time.Minute)
	expire(late.ID, emailVerificationReceiptRetention+time.Minute)
	if _, rows, err = repo.RetireVerificationDispatch(ctx, time.Now()); err != nil || rows != 2 {
		t.Fatal("receipt retention", rows, err)
	}
}

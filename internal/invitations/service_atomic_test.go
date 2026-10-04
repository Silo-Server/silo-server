package invitations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestInvitationServiceCommittedOutcomesDB(t *testing.T) {
	f := atomicInvitationDB(t)
	users := auth.NewUserRepository(f.pool)
	sessions := &fakeSessions{err: errors.New("session unavailable")}
	sender := &fakeMail{configured: true, err: errors.New("SMTP acknowledgement lost")}
	svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), sessions, sender, nil, nil, "https://server.example.invalid")
	sent, err := svc.Send(t.Context(), SendInput{Email: "Claim@EXAMPLE.invalid", Role: models.RoleUser, InvitedBy: 1, CreateProfile: true, LibraryIDs: []int{}})
	if err == nil || sent == nil || sent.EmailSent || len(sender.sent) != 1 {
		t.Fatalf("committed delivery result=%v err=%v sends=%d", sent, err, len(sender.sent))
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://server.example.invalid/invite/")
	pair, user, err := svc.Accept(t.Context(), token, "", "test-password", "test-device", "")
	if !errors.Is(err, ErrSessionStart) || pair != nil || user == nil {
		t.Fatalf("pair=%v user=%v err=%v", pair, user, err)
	}
	stored, err := users.GetByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Username != auth.NormalizeUsername(sent.Invitation.Email) || stored.Email != auth.NormalizeEmail(sent.Invitation.Email) || stored.Role != models.RoleUser || stored.LibraryIDs == nil || len(stored.LibraryIDs) != 0 {
		t.Fatalf("bound account state: %#v", stored)
	}
	inv, err := f.repo.GetByID(t.Context(), sent.Invitation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inv.AcceptedUserID == nil || *inv.AcceptedUserID != int64(user.ID) {
		t.Fatal("post-commit login failure lost invitation claim")
	}
	var profiles int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_profiles WHERE user_id=$1`, user.ID).Scan(&profiles); err != nil || profiles != 1 {
		t.Fatalf("profiles=%d err=%v", profiles, err)
	}
	if _, _, err := svc.Accept(t.Context(), token, "", "test-password", "test-device", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if len(sessions.logins) != 1 || len(sender.sent) != 1 {
		t.Fatal("retry repeated committed effects")
	}
	// A failed database insertion must not reach the sender.
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE invitations ADD CONSTRAINT reject_fixture_email CHECK(email <> 'rejected@example.invalid')`); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.Send(t.Context(), SendInput{Email: "rejected@example.invalid", InvitedBy: 1}); err == nil || result != nil {
		t.Fatalf("failed insertion result=%v err=%v", result, err)
	}
	if len(sender.sent) != 1 {
		t.Fatal("storage failure sent mail")
	}
}

func TestLinkInvitationLifecycleDB(t *testing.T) {
	f := atomicInvitationDB(t)
	users := auth.NewUserRepository(f.pool)
	sender := &fakeMail{configured: true}
	svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), &fakeSessions{}, sender, nil, nil, "https://server.example.invalid")
	first, err := svc.Send(t.Context(), SendInput{Delivery: DeliveryLink, Role: models.RoleUser, InvitedBy: 1, CreateProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Send(t.Context(), SendInput{Delivery: DeliveryLink, Role: models.RoleUser, InvitedBy: 1})
	if err != nil {
		t.Fatalf("a second pending link invitation: %v", err)
	}
	if len(sender.sent) != 0 || first.Invitation.Email != "" || first.Invitation.Delivery != models.InvitationDeliveryLink {
		t.Fatalf("link invitation = %+v sends=%d", first.Invitation, len(sender.sent))
	}
	tokenOf := func(r *SendResult) string {
		return strings.TrimPrefix(r.ClaimURL, "https://server.example.invalid/invite/")
	}

	if _, user, err := svc.Accept(t.Context(), tokenOf(first), "Sam@Example.invalid", "test-password", "d", ""); err != nil || user == nil {
		t.Fatalf("accept user=%v err=%v", user, err)
	}
	accepted, err := f.repo.GetByID(t.Context(), first.Invitation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(accepted.Email, "sam@example.invalid") || accepted.AcceptedAt == nil {
		t.Fatalf("accepted link invitation = %+v", accepted)
	}

	// The second link cannot take the same address, and stays claimable.
	if _, _, err := svc.Accept(t.Context(), tokenOf(second), "sam@example.invalid", "test-password", "d", ""); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("taken address err = %v, want ErrEmailTaken", err)
	}
	if _, _, err := svc.Accept(t.Context(), tokenOf(second), "lee@example.invalid", "test-password", "d", ""); err != nil {
		t.Fatalf("retry with a free address: %v", err)
	}

	// Accepting a link as an address with a live emailed invitation revokes
	// that invitation, which could no longer be accepted.
	emailedBob, err := svc.Send(t.Context(), SendInput{Email: "bob@example.invalid", Role: models.RoleUser, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	bobLink, err := svc.Send(t.Context(), SendInput{Delivery: DeliveryLink, Role: models.RoleUser, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Accept(t.Context(), tokenOf(bobLink), "Bob@Example.invalid", "test-password", "d", ""); err != nil {
		t.Fatalf("accept as bob: %v", err)
	}
	if stale, _ := f.repo.GetByID(t.Context(), emailedBob.Invitation.ID); stale.RevokedAt == nil {
		t.Fatal("emailed invitation for the accepted address is still pending")
	}

	// Replacing a link revokes the old one: nothing supersedes it by address.
	third, err := svc.Send(t.Context(), SendInput{Delivery: DeliveryLink, Role: models.RoleUser, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := svc.Resend(t.Context(), third.Invitation.ID, 1, DeliveryDefault)
	if err != nil || replaced.EmailSent || replaced.Invitation.Email != "" {
		t.Fatalf("resend = %+v err=%v", replaced, err)
	}
	if old, _ := f.repo.GetByID(t.Context(), third.Invitation.ID); old.RevokedAt == nil {
		t.Fatal("replaced link invitation is still live")
	}
	if _, err := svc.Lookup(t.Context(), tokenOf(third)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old link lookup err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Lookup(t.Context(), tokenOf(replaced)); err != nil {
		t.Fatalf("new link lookup: %v", err)
	}

	// Email outcomes only replace email_unconfirmed.
	emailed, err := svc.Send(t.Context(), SendInput{Email: "ana@example.invalid", Role: models.RoleUser, InvitedBy: 1})
	if err != nil || emailed.Invitation.Delivery != models.InvitationDeliveryEmailSent {
		t.Fatalf("emailed = %+v err=%v", emailed, err)
	}
	if err := f.repo.RecordEmailOutcome(t.Context(), emailed.Invitation.ID, models.InvitationDeliveryLink); err != nil {
		t.Fatal(err)
	}
	if stored, _ := f.repo.GetByID(t.Context(), emailed.Invitation.ID); stored.Delivery != models.InvitationDeliveryEmailSent {
		t.Fatalf("delivery overwritten to %q", stored.Delivery)
	}
}

// Replacing an emailed invitation's link without email keeps it bound to its
// address: the stored row satisfies the delivery constraint, the emailed link
// stops working, and the account still takes the bound address.
func TestEmailedInvitationLinkReplacementDB(t *testing.T) {
	f := atomicInvitationDB(t)
	users := auth.NewUserRepository(f.pool)
	sender := &fakeMail{configured: true}
	svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), &fakeSessions{}, sender, nil, nil, "https://server.example.invalid")
	tokenOf := func(r *SendResult) string {
		return strings.TrimPrefix(r.ClaimURL, "https://server.example.invalid/invite/")
	}

	emailed, err := svc.Send(t.Context(), SendInput{Email: "kai@example.invalid", Role: models.RoleUser, InvitedBy: 1})
	if err != nil || len(sender.sent) != 1 {
		t.Fatalf("Send: err=%v sends=%d", err, len(sender.sent))
	}
	replaced, err := svc.Resend(t.Context(), emailed.Invitation.ID, 1, DeliveryLink)
	if err != nil || replaced.EmailSent || len(sender.sent) != 1 {
		t.Fatalf("Resend: result=%+v err=%v sends=%d", replaced, err, len(sender.sent))
	}
	stored, err := f.repo.GetByID(t.Context(), replaced.Invitation.ID)
	if err != nil || stored.Email != "kai@example.invalid" || stored.Delivery != models.InvitationDeliveryLink {
		t.Fatalf("stored replacement = %+v err=%v", stored, err)
	}
	if old, _ := f.repo.GetByID(t.Context(), emailed.Invitation.ID); old.RevokedAt == nil {
		t.Fatal("emailed invitation is still live after its link was replaced")
	}
	if _, err := svc.Lookup(t.Context(), tokenOf(emailed)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("emailed link lookup err = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.Accept(t.Context(), tokenOf(replaced), "someone-else@example.invalid", "test-password", "d", ""); err != nil {
		t.Fatalf("accept replacement: %v", err)
	}
	account, err := users.GetByEmail(t.Context(), "kai@example.invalid")
	if err != nil || account.Username != "kai@example.invalid" {
		t.Fatalf("account = %+v err=%v, want the bound address", account, err)
	}
}

// An emailed acceptance holds its invitation row and then inserts the account.
// A concurrent link acceptance for the same address must wait on that row
// before inserting its own account; the reverse order deadlocks.
func TestLinkAcceptWaitsForConcurrentEmailedAcceptanceDB(t *testing.T) {
	f := atomicInvitationDB(t)
	users := auth.NewUserRepository(f.pool)
	accounts := auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool))
	svc := NewService(f.repo, users, accounts, &fakeSessions{}, &fakeMail{configured: true}, nil, nil, "https://server.example.invalid")
	emailed, err := svc.Send(t.Context(), SendInput{Email: "bob@example.invalid", Role: models.RoleUser, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	link, err := svc.Send(t.Context(), SendInput{Delivery: DeliveryLink, Role: models.RoleUser, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}

	// The emailed acceptance's first step: lock its invitation row.
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(t.Context())) //nolint:errcheck
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM invitations WHERE id=$1 FOR UPDATE`, emailed.Invitation.ID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		token := strings.TrimPrefix(link.ClaimURL, "https://server.example.invalid/invite/")
		_, _, err := svc.Accept(context.WithoutCancel(t.Context()), token, "bob@example.invalid", "test-password", "d", "")
		done <- err
	}()
	// Wait until the link acceptance is blocked on a lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("link acceptance finished without waiting for the emailed row: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("link acceptance never waited on the emailed invitation row")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The emailed acceptance's second step: insert the account, then commit.
	if _, err := accounts.CreateAccountInTransaction(t.Context(), tx, auth.CreateAccountInput{User: models.CreateUserInput{Username: "bob@example.invalid", Email: "bob@example.invalid", Password: "test-password", Role: models.RoleUser}}); err != nil {
		t.Fatalf("emailed acceptance insert: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("link acceptance err = %v, want ErrEmailTaken", err)
	}
}

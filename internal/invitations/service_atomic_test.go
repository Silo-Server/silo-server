package invitations

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestInvitationServiceCommittedOutcomesDB(t *testing.T) {
	f := atomicInvitationDB(t)
	users := auth.NewUserRepository(f.pool)
	sessions := &fakeSessions{err: errors.New("session unavailable")}
	sender := &fakeMail{configured: true, err: errors.New("SMTP acknowledgement lost")}
	svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), sessions, sender, nil, "https://server.example.invalid")
	sent, err := svc.Send(t.Context(), SendInput{Email: "Claim@EXAMPLE.invalid", Role: models.RoleUser, InvitedBy: 1, CreateProfile: true, LibraryIDs: []int{}})
	if err == nil || sent == nil || sent.EmailSent || len(sender.sent) != 1 {
		t.Fatalf("committed delivery result=%v err=%v sends=%d", sent, err, len(sender.sent))
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://server.example.invalid/invite/")
	pair, user, err := svc.Accept(t.Context(), token, "test-password", "test-device", "")
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
	if _, _, err := svc.Accept(t.Context(), token, "test-password", "test-device", ""); !errors.Is(err, ErrNotFound) {
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

package invitations

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestInvitationProfileLimitAcceptanceDB(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit         *int
		createProfile bool
		want          int
	}{
		{"omitted", nil, true, 5},
		{"one profile", new(1), true, 1},
		{"profileless", new(3), false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := atomicInvitationDB(t)
			users := auth.NewUserRepository(f.pool)
			svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), &fakeSessions{err: errors.New("post-commit login fault")}, &fakeMail{}, nil, nil, "https://server.example.invalid")
			sent, err := svc.Send(t.Context(), SendInput{Email: "limit@example.invalid", Role: models.RoleUser, InvitedBy: 1, CreateProfile: tc.createProfile, MaxProfiles: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			// Even a stale or modified service input cannot replace the locked
			// original invitation's profile limit.
			resent, err := f.repo.Resend(t.Context(), sent.Invitation.ID, models.CreateInvitationInput{InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour), MaxProfiles: new(99)}, HashToken("replacement"))
			if err != nil {
				t.Fatal(err)
			}
			if (resent.MaxProfiles == nil) != (tc.limit == nil) || (tc.limit != nil && *resent.MaxProfiles != *tc.limit) {
				t.Fatal("resend did not preserve the locked profile limit")
			}
			_, user, err := svc.Accept(t.Context(), "replacement", "", "test-password", "device", "")
			if !errors.Is(err, ErrSessionStart) || user == nil || user.MaxProfiles != tc.want {
				t.Fatalf("accepted user=%v err=%v", user, err)
			}
			stored, err := users.GetByID(t.Context(), user.ID)
			if err != nil || stored.MaxProfiles != tc.want {
				t.Fatalf("persisted profile limit: user=%v err=%v", stored, err)
			}
			profiles := 0
			if tc.createProfile {
				profiles = 1
			}
			f.counts(t, resent, 1, profiles, 1)
			if _, _, err := svc.Accept(t.Context(), "replacement", "", "test-password", "device", ""); !errors.Is(err, ErrNotFound) {
				t.Fatal("committed acceptance could be replayed", err)
			}
		})
	}
}

func TestInvitationProfileLimitAtomicFaultsDB(t *testing.T) {
	for _, fault := range []struct{ name, ddl, cleanup string }{
		{"profile insert", `CREATE FUNCTION reject_profile() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture profile failure'; END $$; CREATE TRIGGER reject_profile BEFORE INSERT ON user_profiles FOR EACH ROW EXECUTE FUNCTION reject_profile()`, `DROP TRIGGER reject_profile ON user_profiles; DROP FUNCTION reject_profile()`},
		{"claim commit", `CREATE FUNCTION reject_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture claim failure'; END $$; CREATE CONSTRAINT TRIGGER reject_claim AFTER UPDATE ON invitations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.accepted_at IS NOT NULL) EXECUTE FUNCTION reject_claim()`, `DROP TRIGGER reject_claim ON invitations; DROP FUNCTION reject_claim()`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := atomicInvitationDB(t)
			users := auth.NewUserRepository(f.pool)
			svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), &fakeSessions{err: errors.New("post-commit login fault")}, &fakeMail{}, nil, nil, "https://server.example.invalid")
			sent, err := svc.Send(t.Context(), SendInput{Email: "rollback-limit@example.invalid", Role: models.RoleUser, InvitedBy: 1, CreateProfile: true, MaxProfiles: new(1)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), fault.ddl); err != nil {
				t.Fatal(err)
			}
			token := strings.TrimPrefix(sent.ClaimURL, "https://server.example.invalid/invite/")
			if _, user, err := svc.Accept(t.Context(), token, "", "test-password", "device", ""); err == nil || user != nil {
				t.Fatalf("fault committed user=%v err=%v", user, err)
			}
			f.counts(t, sent.Invitation, 0, 0, 0)
			pending, err := f.repo.GetByID(t.Context(), sent.Invitation.ID)
			if err != nil || pending.MaxProfiles == nil || *pending.MaxProfiles != 1 || pending.Status(time.Now()) != models.InvitationStatusPending {
				t.Fatalf("failed claim altered invitation: inv=%v err=%v", pending, err)
			}
			if _, err := f.pool.Exec(t.Context(), fault.cleanup); err != nil {
				t.Fatal(err)
			}
			_, user, err := svc.Accept(t.Context(), token, "", "test-password", "device", "")
			if !errors.Is(err, ErrSessionStart) || user == nil || user.MaxProfiles != 1 {
				t.Fatalf("recovery user=%v err=%v", user, err)
			}
			f.counts(t, sent.Invitation, 1, 1, 1)
		})
	}
}

func TestInvitationProfileLimitInvalidInsertPreservesSourceDB(t *testing.T) {
	f := atomicInvitationDB(t)
	prior := f.invite(t, "invalid-limit")
	invalid := models.CreateInvitationInput{Email: prior.Email, Role: models.RoleUser, InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour), MaxProfiles: new(0)}
	if replacement, err := f.repo.Create(t.Context(), invalid, HashToken("invalid-replacement")); err == nil || replacement != nil {
		t.Fatalf("invalid limit inserted: inv=%v err=%v", replacement, err)
	}
	stored, err := f.repo.GetByID(t.Context(), prior.ID)
	if err != nil || stored.RevokedAt != nil {
		t.Fatalf("invalid replacement revoked source: inv=%v err=%v", stored, err)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM invitations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invitation count=%d err=%v", count, err)
	}
}

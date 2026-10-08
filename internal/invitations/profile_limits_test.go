package invitations

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestInvitationProfileLimitServiceValidation(t *testing.T) {
	for _, limit := range []int{0, -1, MaxProfileLimit + 1} {
		repo := newFakeRepo()
		accounts := &fakeAccounts{}
		sender := &fakeMail{configured: true}
		svc := newTestService(repo, &fakeUsers{}, accounts, &fakeSessions{}, sender, nil)
		result, err := svc.Send(t.Context(), SendInput{Email: testInvitee, InvitedBy: 1, MaxProfiles: &limit})
		if !errors.Is(err, ErrInvalidProfileLimit) || result != nil {
			t.Fatalf("limit=%d result=%v err=%v", limit, result, err)
		}
		if len(repo.rows) != 0 || len(accounts.created) != 0 || len(sender.sent) != 0 {
			t.Fatal("invalid profile limit caused effects")
		}
	}
}

func TestInvitationProfileLimitServiceBinding(t *testing.T) {
	for _, limit := range []*int{nil, new(1), new(3)} {
		repo := newFakeRepo()
		accounts := &fakeAccounts{}
		users := &fakeUsers{byID: map[int]*models.User{1: {ID: 1, Role: models.RoleAdmin}}}
		svc := newTestService(repo, users, accounts, &fakeSessions{}, &fakeMail{}, nil)
		sent, err := svc.Send(t.Context(), SendInput{Email: testInvitee, InvitedBy: 1, MaxProfiles: limit})
		if err != nil {
			t.Fatal(err)
		}
		replaced, err := svc.Resend(t.Context(), sent.Invitation.ID, 1, DeliveryDefault)
		if err != nil {
			t.Fatal(err)
		}
		if replaced.Invitation.MaxProfiles != limit {
			t.Fatal("resend changed profile limit")
		}
		if _, _, err := svc.Accept(t.Context(), strings.TrimPrefix(replaced.ClaimURL, "https://silo.example.com/invite/"), "", "test-password", "device", ""); err != nil {
			t.Fatal(err)
		}
		if len(accounts.created) != 1 || accounts.created[0].User.MaxProfiles != limit {
			t.Fatal("account provisioning did not receive invitation profile limit")
		}
	}
}

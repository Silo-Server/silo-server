package invitations

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The name and email a join's account takes. A username that looks like an
// address must be the address given with it: otherwise an invitee could
// hold someone else's address as a name and block that person's own
// invitation.
func TestJoinAccountName(t *testing.T) {
	link := &models.Invitation{}
	for _, tc := range []struct {
		name            string
		inv             *models.Invitation
		in              AppCodeRequest
		username, email string
		err             error
	}{
		{name: "emailed invitation", inv: &models.Invitation{Email: "sam@example.test"}, in: AppCodeRequest{Username: "other"},
			username: "sam@example.test", email: "sam@example.test"},
		{name: "username only", inv: link, in: AppCodeRequest{Username: " sam "}, username: "sam"},
		{name: "username and email", inv: link, in: AppCodeRequest{Username: "sam", Email: "sam@example.test"}, username: "sam", email: "sam@example.test"},
		{name: "no username", inv: link, in: AppCodeRequest{Email: "sam@example.test"}, err: ErrUsernameRequired},
		{name: "malformed email", inv: link, in: AppCodeRequest{Username: "sam", Email: "not an address"}, err: ErrInvalidEmail},
		{name: "someone else's address as a username", inv: link, in: AppCodeRequest{Username: "bob@corp.example"}, err: ErrUsernameIsAddress},
		{name: "an address username with another email", inv: link, in: AppCodeRequest{Username: "bob@corp.example", Email: "sam@example.test"}, err: ErrUsernameIsAddress},
		{name: "the address given as both", inv: link, in: AppCodeRequest{Username: "Sam@Example.test", Email: "sam@example.test"},
			username: "Sam@Example.test", email: "sam@example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			username, email, err := joinAccountName(tc.inv, tc.in)
			if !errors.Is(err, tc.err) || username != tc.username || email != tc.email {
				t.Fatalf("joinAccountName = %q, %q, %v; want %q, %q, %v", username, email, err, tc.username, tc.email, tc.err)
			}
		})
	}
}

// fixedClaims hands IssueAppCode one locked, claimable invitation.
type fixedClaims struct{ inv *models.Invitation }

func (f fixedClaims) InTx(_ context.Context, fn func(pgx.Tx) error) error { return fn(nil) }
func (f fixedClaims) LockClaimableByTokenTx(context.Context, pgx.Tx, string) (*models.Invitation, error) {
	return f.inv, nil
}
func (f fixedClaims) ClaimByIDTx(context.Context, pgx.Tx, int64, func() (string, error), func(*models.Invitation, pgx.Tx) (*models.User, error)) (*models.User, error) {
	return nil, errors.New("not used")
}

// accountsOutsideTransaction can't create a profile in the claim's
// transaction, as when profiles live outside Postgres.
type accountsOutsideTransaction struct{}

func (accountsOutsideTransaction) CreateAccountInTransaction(context.Context, pgx.Tx, auth.CreateAccountInput) (*models.User, error) {
	return nil, errors.New("not used")
}

// A code is refused where accept would be refused, so the page is never
// handed a code no redeem can use: here, an invitation that creates a
// profile the store can't create in the same transaction.
func TestIssueAppCodeRefusedWhereAcceptIs(t *testing.T) {
	svc := &Service{claims: fixedClaims{inv: &models.Invitation{ID: 1, CreateProfile: true}}, accounts: accountsOutsideTransaction{}}
	if _, err := svc.IssueAppCode(t.Context(), AppCodeRequest{Token: "t", Username: "sam"}); !errors.Is(err, auth.ErrTransactionalProfileUnavailable) {
		t.Fatalf("IssueAppCode = %v, want ErrTransactionalProfileUnavailable", err)
	}
}

// A join code may travel in a link only when the account it creates has no
// administrative power: a link can reach an app other than Silo, which would
// redeem the code first. One row per field an invitation carries.
func TestJoinLinkAllowed(t *testing.T) {
	group := int64(3)
	for _, tc := range []struct {
		name string
		inv  models.Invitation
		edit func(*auth.CreateAccountInput)
		want bool
	}{
		{name: "an ordinary invitation", inv: models.Invitation{Role: models.RoleUser}, want: true},
		{name: "the administrator role", inv: models.Invitation{Role: models.RoleAdmin}},
		{name: "a role this server doesn't know", inv: models.Invitation{Role: "owner"}},
		{name: "no role", inv: models.Invitation{}},
		{name: "an access group, which only narrows", inv: models.Invitation{Role: models.RoleUser, AccessGroupID: &group}, want: true},
		{name: "a library list", inv: models.Invitation{Role: models.RoleUser, LibraryIDs: []int{1, 2}}, want: true},
		{name: "a first profile", inv: models.Invitation{Role: models.RoleUser, CreateProfile: true}, want: true},
		{name: "an address", inv: models.Invitation{Role: models.RoleUser, Email: "sam@example.test"}, want: true},
		{name: "catalog curation, were an account to start with it", inv: models.Invitation{Role: models.RoleUser},
			edit: func(in *auth.CreateAccountInput) {
				in.User.Permissions = []string{string(auth.PermissionMetadataCuration)}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := joinAccountInput(&tc.inv, "sam", tc.inv.Email)
			if tc.edit != nil {
				tc.edit(&input)
			}
			if got := linkAllowed(input); got != tc.want {
				t.Fatalf("linkAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}

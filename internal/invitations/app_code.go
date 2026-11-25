package invitations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrUsernameRequired refuses an app code for a link invitation without an
// account name: the invitation has no email to take one from.
var ErrUsernameRequired = errors.New("a username is required to join with this invitation")

// claimStore is the part of the repository a join through the Silo app
// uses: it claims in a transaction the caller owns. Satisfied by
// *Repository.
type claimStore interface {
	InTx(ctx context.Context, fn func(pgx.Tx) error) error
	LockClaimableByTokenTx(ctx context.Context, tx pgx.Tx, tokenHash string) (*models.Invitation, error)
	ClaimByIDTx(ctx context.Context, tx pgx.Tx, id int64, linkAddress func() (string, error), provision func(*models.Invitation, pgx.Tx) (*models.User, error)) (*models.User, error)
}

// AppCodeRequest asks for a join code: the invitation's token, and the
// account name and email the person chose on the invite page.
type AppCodeRequest struct {
	Token    string
	Username string
	Email    string
}

// IssueAppCode starts a join through the Silo app: it checks the invitation
// as accept does, reserves the account name and returns an app code. The
// invitation stays open; nothing is created until the app redeems the code
// (auth.AppKeySignIn.RedeemAppCode), and a second code replaces the first.
//
// An emailed invitation's account takes the invitation's address as its
// name and email, as accept gives it; the request's username and email are
// not used. A link invitation's account takes the username, which is
// required, and the optional email. With local password sign-in off the
// answer is auth.ErrJoinNotAvailable: the account would have keys as its
// only way in, so there the person joins through the sign-in provider.
func (s *Service) IssueAppCode(ctx context.Context, in AppCodeRequest) (auth.IssuedAppCode, error) {
	if strings.TrimSpace(in.Token) == "" || s.claims == nil {
		return auth.IssuedAppCode{}, ErrNotFound
	}
	var out auth.IssuedAppCode
	err := s.claims.InTx(ctx, func(tx pgx.Tx) error {
		inv, err := s.claims.LockClaimableByTokenTx(ctx, tx, HashToken(in.Token))
		if errors.Is(err, auth.ErrLocalLoginDisabled) {
			return auth.ErrJoinNotAvailable
		}
		if err != nil {
			return err
		}
		username, email, err := joinAccountName(inv, in)
		if err != nil {
			return err
		}
		out, err = auth.IssueJoinAppCodeTx(ctx, tx, auth.JoinAppCode{
			InvitationID: inv.ID, Username: username, Email: email, InvitationExpiresAt: inv.ExpiresAt,
		})
		return err
	})
	return out, err
}

// joinAccountName is the name and email a join's account takes: an emailed
// invitation's address for both, or the username the person chose (required)
// and their optional email, checked by the rule accept, signup and the
// administrator create form use.
func joinAccountName(inv *models.Invitation, in AppCodeRequest) (string, string, error) {
	if inv.Email != "" {
		return inv.Email, inv.Email, nil
	}
	username := auth.NormalizeUsername(in.Username)
	if username == "" {
		return "", "", ErrUsernameRequired
	}
	if strings.TrimSpace(in.Email) == "" {
		return username, "", nil
	}
	email, err := auth.ValidateEmail(in.Email)
	if err != nil {
		return "", "", ErrInvalidEmail
	}
	return username, email, nil
}

// ClaimJoinTx claims a join code's invitation in the redeem's transaction
// (auth.JoinClaimer): the account is created with no password, taking the
// code's name and email and the invitation's role, libraries, access group
// and first-profile choice, and the invitation is marked accepted. An
// invitation revoked, replaced, accepted or expired since the code was
// issued is auth.ErrAppCodeInvalid.
func (s *Service) ClaimJoinTx(ctx context.Context, tx pgx.Tx, join auth.JoinAccount) (*models.User, error) {
	if s.claims == nil {
		return nil, auth.ErrAppCodeInvalid
	}
	user, err := s.claims.ClaimByIDTx(ctx, tx, join.InvitationID, func() (string, error) { return join.Email, nil },
		func(inv *models.Invitation, tx pgx.Tx) (*models.User, error) {
			return s.accounts.CreateAccountInTransaction(ctx, tx, auth.CreateAccountInput{
				User: models.CreateUserInput{
					Username: join.Username, Email: join.Email, NoPassword: true,
					Role: inv.Role, LibraryIDs: inv.LibraryIDs, AccessGroupID: inv.AccessGroupID,
				},
				DefaultProfile: auth.DefaultProfileOptions{Enabled: inv.CreateProfile, Name: profileNameFromEmail(join.Username)},
			})
		})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotClaimable) {
		return nil, auth.ErrAppCodeInvalid
	}
	return user, err
}

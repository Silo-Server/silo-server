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

// ErrUsernameIsAddress refuses a username that looks like an email address
// other than the one the same request gives: it could be someone else's
// address, and holding it would block that person's own invitation.
var ErrUsernameIsAddress = errors.New("a username with @ must be the email address given with it")

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
		// What accept needs first (acceptance_available in the lookup): a
		// code that could never be redeemed is not issued.
		if inv.CreateProfile && !s.SupportsDefaultProfile() {
			return auth.ErrTransactionalProfileUnavailable
		}
		username, email, err := joinAccountName(inv, in)
		if err != nil {
			return err
		}
		out, err = auth.IssueJoinAppCodeTx(ctx, tx, auth.JoinAppCode{
			InvitationID: inv.ID, Username: username, Email: email, InvitationExpiresAt: inv.ExpiresAt,
		})
		out.LinkAllowed = linkAllowed(joinAccountInput(inv, username, email))
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
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		address, err := auth.ValidateEmail(in.Email)
		if err != nil {
			return "", "", ErrInvalidEmail
		}
		email = address
	}
	if strings.Contains(username, "@") && !strings.EqualFold(username, email) {
		return "", "", ErrUsernameIsAddress
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
			return s.accounts.CreateAccountInTransaction(ctx, tx, joinAccountInput(inv, join.Username, join.Email))
		})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotClaimable) {
		return nil, auth.ErrAppCodeInvalid
	}
	return user, err
}

// joinAccountInput is the account a join creates from an invitation: no
// password, the chosen name and email, and the invitation's role, libraries,
// access group and first-profile choice. Issuing a code judges linkAllowed
// on it and redeeming creates it, so the two can't disagree.
func joinAccountInput(inv *models.Invitation, username, email string) auth.CreateAccountInput {
	return auth.CreateAccountInput{
		User: models.CreateUserInput{
			Username: username, Email: email, NoPassword: true,
			Role: inv.Role, LibraryIDs: inv.LibraryIDs, AccessGroupID: inv.AccessGroupID,
		},
		DefaultProfile: auth.DefaultProfileOptions{Enabled: inv.CreateProfile, Name: profileNameFromEmail(username)},
	}
}

// linkPermissions are the only permissions an account whose join code may
// travel in a link can hold. marker_edit edits the account's own markers;
// metadata_curation edits catalog data every account sees, which is close
// enough to administrative power that it is refused.
var linkPermissions = map[string]bool{string(auth.PermissionMarkerEdit): true}

// linkAllowed reports whether a join code that creates this account may be
// put in a link (auth.IssuedAppCode.LinkAllowed): only when the account has
// no administrative power. Field by field, for what an invitation carries:
//
//   - Role: only the user role; the admin role, or any other value, refuses.
//   - Permissions: the invitation sets none, so the account takes the
//     defaults for its role (auth.NewUserPermissions); anything outside
//     linkPermissions refuses.
//   - AccessGroupID: a group narrows an account's permissions and limits and
//     never adds any (access.ApplyGroupPolicy intersects), so it can't
//     confer power.
//   - LibraryIDs, the first profile, the email and the name: access to media
//     and identity, no power.
//   - Ownership and break-glass: an invitation can't carry them, and
//     creating an account never sets them.
//
// Nothing changes an invitation after it is issued: resending makes a new
// invitation (copying role, group and libraries) whose codes are new, and
// the old invitation's codes stop working.
func linkAllowed(input auth.CreateAccountInput) bool {
	if input.User.Role != models.RoleUser {
		return false
	}
	permissions, err := auth.NewUserPermissions(input.User)
	if err != nil {
		return false
	}
	for _, permission := range permissions {
		if !linkPermissions[permission] {
			return false
		}
	}
	return true
}

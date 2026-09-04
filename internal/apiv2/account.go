package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The account domain: the login account, independent of any profile.

// Impersonation says an administrator is acting as this account.
type Impersonation struct {
	Active               bool   `json:"active" doc:"Always true when the object is present"`
	ImpersonatorUserID   ID     `json:"impersonator_user_id" doc:"The administrator account acting as this account"`
	ImpersonatorUsername string `json:"impersonator_username" doc:"The administrator's username; empty when the account no longer exists"`
}

// Account is the authenticated caller's login account.
type Account struct {
	ID              ID             `json:"id" doc:"Account identifier"`
	Username        string         `json:"username" doc:"Login name"`
	Email           string         `json:"email" doc:"Contact email; empty when none is set"`
	Role            string         `json:"role" enum:"admin,user" doc:"Server-wide role"`
	Permissions     []string       `json:"permissions" doc:"Effective assignable permissions; empty for a disabled account"`
	DownloadAllowed bool           `json:"download_allowed" doc:"Whether the effective policy permits downloads"`
	Impersonation   *Impersonation `json:"impersonation,omitempty" doc:"Present only while an administrator impersonates this account"`
}

// AccountOutput is the getCurrentUser response.
type AccountOutput struct {
	Body Account
}

func registerAccount(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/account/me", "getCurrentUser", "account",
			"Get the authenticated caller's login account."),
		Class: ClassAuthenticated,
	}, reg.getCurrentUser)
}

// getCurrentUser answers from the same account lookup v1 GET /auth/me uses.
func (reg *Registry) getCurrentUser(ctx context.Context, _ *struct{}) (*AccountOutput, error) {
	if reg.deps.Accounts == nil {
		return nil, unavailable("account")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	view, err := reg.deps.Accounts.CurrentUser(ctx, claims)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &AccountOutput{Body: accountFromView(view)}, nil
}

func accountFromView(v handlers.UserView) Account {
	out := Account{
		ID:              IDFromInt(int64(v.ID)),
		Username:        v.Username,
		Email:           v.Email,
		Role:            roleOf(v.Role),
		Permissions:     NonNil(v.Permissions),
		DownloadAllowed: v.DownloadAllowed,
	}
	if v.Impersonation != nil {
		out.Impersonation = &Impersonation{
			Active:               v.Impersonation.Active,
			ImpersonatorUserID:   IDFromInt(int64(v.Impersonation.ImpersonatorUserID)),
			ImpersonatorUsername: v.Impersonation.ImpersonatorUsername,
		}
	}
	return out
}

// roleOf keeps the stored role inside the declared enum: the accounts table
// only holds admin and user, and anything else is reported as the least
// privileged value rather than as a value the contract does not name.
func roleOf(role string) string {
	if role == models.RoleAdmin {
		return models.RoleAdmin
	}
	return models.RoleUser
}

package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The account domain: the login account, independent of any profile.

// Impersonation says an administrator is acting as this account.
type Impersonation struct {
	Active               bool   `json:"active" doc:"Always true when the object is present" example:"true"`
	ImpersonatorUserID   ID     `json:"impersonator_user_id" doc:"The administrator account acting as this account" example:"7"`
	ImpersonatorUsername string `json:"impersonator_username" doc:"The administrator's username; empty when the account no longer exists" example:"alice"`
}

// Account is the authenticated caller's login account.
type Account struct {
	ID              ID             `json:"id" doc:"Account identifier" example:"1"`
	Username        string         `json:"username" doc:"Login name" example:"alice"`
	Email           string         `json:"email" doc:"Contact email; empty when none is set" example:"alice@example.test"`
	Role            string         `json:"role" enum:"admin,user" doc:"Server-wide role" example:"user"`
	Permissions     []Permission   `json:"permissions" doc:"Effective assignable permissions; empty for a disabled account" example:"[\"marker_edit\"]"`
	DownloadAllowed bool           `json:"download_allowed" doc:"Whether the effective policy permits downloads" example:"true"`
	Impersonation   *Impersonation `json:"impersonation,omitempty" doc:"Present only while an administrator impersonates this account"`
}

// AccountOutput is the getCurrentUser response.
type AccountOutput struct {
	Body Account
}

// AccountPasswordCapability is the self-service password capability document
// of the caller's account and declared profile. The password is
// account-wide, so allowed is true only for the primary profile (or an admin
// with no profile declared) on a plain login session: never for an API key,
// an impersonated session, or a secondary profile.
type AccountPasswordCapability struct {
	Capability
	RequiresCurrentPassword bool `json:"requires_current_password" doc:"Whether changePassword needs current_password; always true today" example:"true"`
	MinimumPasswordLength   int  `json:"minimum_password_length" doc:"Fewest characters a new password may have" example:"8"`
	MaximumPasswordBytes    int  `json:"maximum_password_bytes" doc:"Most UTF-8 bytes a new password may have" example:"72"`
}

// AccountPasswordCapabilityOutput is the getAccountPasswordCapability response.
type AccountPasswordCapabilityOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         AccountPasswordCapability
}

// ChangePasswordInput replaces the account password.
type ChangePasswordInput struct {
	Body struct {
		CurrentPassword string `json:"current_password" minLength:"1" maxLength:"1024" doc:"The password in use" example:"correct horse battery staple"`
		NewPassword     string `json:"new_password" minLength:"1" maxLength:"1024" doc:"The replacement; the capability document names the length limits" example:"margin fossil quench hollow"`
	}
}

func registerAccount(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/account/me", "getCurrentUser", "account",
			"Get the authenticated caller's login account."),
		Class:         ClassAuthenticated,
		ServiceBacked: true,
	}, reg.getCurrentUser)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/account/password/capability", "getAccountPasswordCapability", "account",
			"Describe whether the caller may change the account password."),
		Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true,
	}, reg.getAccountPasswordCapability)
	change := humaOp(http.MethodPost, Prefix+"/account/password", "changePassword", "account",
		"Replace the account password.")
	// A secondary profile, an API key or an impersonated session is 403
	// permission_denied; an account without local password sign-in is 409
	// conflict; a wrong current password or a rejected new one is 422 at the
	// member.
	change.Errors = []int{http.StatusForbidden, http.StatusConflict}
	Register(reg, Operation{
		Operation: change,
		Class:     ClassProfileScoped, ProfileOptional: true, ServiceBacked: true,
	}, reg.changePassword)
}

// getAccountPasswordCapability answers from the same decision v1 GET
// /auth/account/capability makes, for the profile viewer access resolved from
// the optional X-Profile-Id. v1's change_password is the capability head's
// allowed; a server without a password service is state not_configured.
func (reg *Registry) getAccountPasswordCapability(ctx context.Context, _ *struct{}) (*AccountPasswordCapabilityOutput, error) {
	if reg.deps.Accounts == nil {
		return nil, unavailable("account")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	view, err := reg.deps.Accounts.AccountPasswordCapability(ctx, claims, profileFrom(ctx))
	if err != nil {
		return nil, serviceProblem(err)
	}
	allowed := view.ChangePassword
	out := &AccountPasswordCapabilityOutput{CacheControl: cacheNoCache, Body: AccountPasswordCapability{
		Capability:              Capability{Revision: "1", State: StateAvailable, Allowed: &allowed},
		RequiresCurrentPassword: view.RequiresCurrentPassword,
		MinimumPasswordLength:   view.MinimumPasswordLength,
		MaximumPasswordBytes:    view.MaximumPasswordBytes,
	}}
	if !view.Configured {
		out.Body.State = StateNotConfigured
	}
	return out, nil
}

// changePassword runs the same two steps as v1 POST /auth/account/password:
// the authority check for the declared profile, then the replacement.
func (reg *Registry) changePassword(ctx context.Context, in *ChangePasswordInput) (*struct{}, error) {
	if reg.deps.Accounts == nil {
		return nil, unavailable("account")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if err := reg.deps.Accounts.AuthorizePasswordChange(ctx, claims, profileFrom(ctx)); err != nil {
		return nil, passwordProblem(err)
	}
	if err := reg.deps.Accounts.ChangePassword(ctx, claims, in.Body.CurrentPassword, in.Body.NewPassword); err != nil {
		return nil, passwordProblem(err)
	}
	return nil, nil
}

// passwordProblem renders a password-change refusal. A rejected member (the
// wrong current password, a new one outside the limits; v1 400
// invalid_current_password, weak_password, password_too_long) is a
// validation problem at that member; a missing password service is 503
// dependency_unavailable (v1 503 password_change_unavailable); everything
// else follows the v1 status (403 permission_denied, 409 conflict).
func passwordProblem(err error) *Problem {
	var apiErr *handlers.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Field != "" {
			return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationBody + "." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message + "."})
		}
		if apiErr.Status == http.StatusServiceUnavailable {
			return NewProblem(TypeDependencyUnavailable, apiErr.Message+".").WithRetryAfter(30)
		}
	}
	return serviceProblem(err)
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
		Permissions:     permissionsOf(v.Permissions),
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

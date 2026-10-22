package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/passwordreset"
)

// PasswordResetService issues and completes password reset links
// (*handlers.PasswordResetHandler).
type PasswordResetService interface {
	PasswordResetCapabilities(context.Context) passwordreset.Capabilities
	IssuePasswordReset(context.Context, passwordreset.IssueInput) (*passwordreset.IssueResult, error)
	PasswordResetSelfService(context.Context) (enabled, configured bool, err error)
	RequestPasswordReset(context.Context, string) error
	LookupPasswordReset(context.Context, string) (*passwordreset.LookupResult, error)
	CompletePasswordReset(context.Context, string, string, string, string) (handlers.PasswordResetCompletionView, error)
}

// passwordResetCompleted is the only PasswordResetCompletion status.
const passwordResetCompleted = "completed"

// bucketPasswordReset is the per-client-IP budget of the public reset
// screen's operations (ratelimit.auth.password_reset.*).
const bucketPasswordReset = "password_reset"

// bucketPasswordResetRequest is the tighter per-client-IP budget of the
// sign-in page's reset request, which can send email
// (ratelimit.auth.password_reset_request.*).
const bucketPasswordResetRequest = "password_reset_request"

// selfServiceResetDomain names the capability in problem details.
const selfServiceResetDomain = "self-service password reset"

// AdminPasswordResetInput issues a reset link for one account.
type AdminPasswordResetInput struct {
	ID   ID `path:"id"`
	Body struct {
		Delivery string `json:"delivery" enum:"email,link" doc:"email sends the link to the account's address; link returns it to you to share. Either way you never see the new password" example:"email"`
	}
}

// AdminPasswordReset is an issued link. Issuing replaces any earlier link for
// the account.
type AdminPasswordReset struct {
	Delivery       string  `json:"delivery" enum:"email,link" example:"link"`
	DeliveryStatus string  `json:"delivery_status" enum:"sent,failed_or_unknown,not_requested" doc:"For email: sent, or failed_or_unknown when the mail server did not confirm delivery (the link is still live; send again or share a link). For link: not_requested" example:"not_requested"`
	ResetURL       string  `json:"reset_url,omitempty" doc:"Present for delivery=link only. One-time disclosure of a bearer credential for the account's password; share it only with the account holder. The token is the URL's fragment, which browsers never send to the server" example:"https://silo.example.test/reset-password#3f2b47eb7b36dd2d"`
	ExpiresAt      Instant `json:"expires_at" doc:"When the link stops working"`
}

// AdminPasswordResetOutput is the createAdminUserPasswordReset response.
type AdminPasswordResetOutput struct {
	Body AdminPasswordReset
}

// PasswordResetCapability reports whether the sign-in page may offer
// self-service password reset. The state is available only when an
// administrator turned it on and the server can email a link; disabled when
// it is off; not_configured when email or the server's public URL is missing.
type PasswordResetCapability struct {
	Capability
}

// PasswordResetCapabilityOutput is the getPasswordResetCapability response.
type PasswordResetCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         PasswordResetCapability
}

// PasswordResetRequestInput asks for a reset link for one's own account.
type PasswordResetRequestInput struct {
	Body struct {
		Login string `json:"login" minLength:"1" maxLength:"320" doc:"The account's sign-in name or email address" example:"alice"`
	}
}

// PasswordResetLinkInput names a link by its token. The token travels in the
// body, never the URL, so proxies and access logs in front of the server do
// not record it.
type PasswordResetLinkInput struct {
	Body struct {
		Token string `json:"token" minLength:"1" maxLength:"128" doc:"The token from the reset link's fragment"`
	}
}

// PasswordResetLookup is the reset screen's view of a usable link.
type PasswordResetLookup struct {
	Username   string  `json:"username" doc:"The account whose password the link replaces" example:"alice"`
	ServerName string  `json:"server_name" example:"Silo"`
	ExpiresAt  Instant `json:"expires_at"`
}

// PasswordResetLookupOutput is the lookupPasswordResetLink response.
type PasswordResetLookupOutput struct {
	Body PasswordResetLookup
}

// PasswordResetCompleteInput sets the new password through a link.
type PasswordResetCompleteInput struct {
	UserAgent string `header:"User-Agent"`
	Body      struct {
		Token    string `json:"token" minLength:"1" maxLength:"128" doc:"The token from the reset link's fragment"`
		Password string `json:"password" minLength:"8" maxLength:"72" doc:"The new password" example:"margin fossil quench hollow"`
	}
}

// PasswordResetCompletion always means the new password is in place and the
// link is spent. A sign-in-required result must never cause a caller to
// replay the reset; the account signs in with the new password instead.
type PasswordResetCompletion struct {
	Status      string     `json:"status" enum:"completed"`
	LoginStatus string     `json:"login_status" enum:"signed_in,sign_in_required"`
	Username    string     `json:"username"`
	Tokens      *TokenPair `json:"tokens,omitempty"`
}

// PasswordResetCompleteOutput is the completePasswordResetLink response.
type PasswordResetCompleteOutput struct {
	Body PasswordResetCompletion
}

func passwordResetProblem(err error) *Problem {
	var apiErr *handlers.APIError
	switch {
	case errors.As(err, &apiErr):
		return serviceProblem(err)
	case errors.Is(err, passwordreset.ErrNotFound):
		return NewProblem(TypeNotFound, "Password reset link not found or no longer available.")
	case errors.Is(err, auth.ErrNotFound):
		return NewProblem(TypeNotFound, "Account not found.")
	case errors.Is(err, auth.ErrPasswordLoginDisabled):
		return NewProblem(TypeConflict, "An external provider manages this account's sign-in; it has no password to reset.")
	case errors.Is(err, passwordreset.ErrAccountDisabled):
		return NewProblem(TypeConflict, "The account is disabled or does not sign in with a password.")
	case errors.Is(err, passwordreset.ErrNotEligible):
		return NewProblem(TypeConflict, "The account changed while issuing the reset link. Reload the account and try again.")
	case errors.Is(err, passwordreset.ErrNoEmail):
		return NewProblem(TypeConflict, "The account has no email address; share a reset link instead.")
	case errors.Is(err, mail.ErrNotConfigured):
		return NewProblem(TypeCapabilityNotConfigured, "Email is not configured; share a reset link instead.")
	case errors.Is(err, passwordreset.ErrNoLinkBase):
		return NewProblem(TypeCapabilityNotConfigured, "Reset links need the server's public URL; set it in the server settings.")
	case errors.Is(err, auth.ErrPasswordTooShort):
		return passwordMemberProblem("Use at least 8 characters.")
	case errors.Is(err, auth.ErrPasswordTooLong):
		return passwordMemberProblem("Use at most 72 bytes.")
	}
	return NewProblem(TypeInternalError, "Unable to process the password reset.")
}

func passwordMemberProblem(detail string) *Problem {
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: locationBody + ".password", Code: codeInvalid, Detail: detail})
}

func (reg *Registry) passwordResets() (PasswordResetService, *Problem) {
	if reg.deps.PasswordResets == nil {
		return nil, CapabilityProblem(StateNotConfigured, "password resets")
	}
	return reg.deps.PasswordResets, nil
}

func registerPasswordResets(reg *Registry) {
	issue := adminAccountOperation(http.MethodPost, "/{id}/password-reset", "createAdminUserPasswordReset", false)
	issue.Summary = "Email an account a password reset link, or create one to share."
	issue.DefaultStatus = http.StatusCreated
	issue.Errors = []int{http.StatusConflict}
	Register(reg, issue, reg.createAdminUserPasswordReset)

	// The reset screen's operations take the link's token in the body: a URL
	// would leave it in proxy and CDN access logs.
	public := func(path, id, summary string, safety RetrySafety) Operation {
		op := Operation{Operation: humaOp(http.MethodPost, Prefix+path, id, "auth", summary), Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketPasswordReset, RetrySafety: safety}
		op.Errors = []int{http.StatusTooManyRequests}
		return op
	}
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/capabilities/password-reset", "getPasswordResetCapability", "auth",
			"Discover whether the sign-in page may offer self-service password reset."),
		Class: ClassPublic, ServiceBacked: true,
	}, reg.getPasswordResetCapability)
	request := humaOp(http.MethodPost, Prefix+"/password-resets", "requestPasswordReset", "auth",
		"Email a reset link to the account a sign-in name or email address names, if one matches.")
	request.DefaultStatus = http.StatusAccepted
	// Every accepted request is 202 alike, matched or not; only the
	// capability state (409) or the rate limit (429) refuses one.
	request.Errors = []int{http.StatusConflict, http.StatusTooManyRequests}
	Register(reg, Operation{Operation: request, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true, RateLimitBucket: bucketPasswordResetRequest}, reg.requestPasswordReset)
	Register(reg, public("/password-resets/lookup", "lookupPasswordResetLink",
		"Describe a usable password reset link for the reset screen.", RetrySafetyNaturalIdempotent), reg.lookupPasswordResetLink)
	Register(reg, public("/password-resets/complete", "completePasswordResetLink",
		"Set a new password through a reset link, sign out everywhere, and sign in.", RetrySafetyNonRetryable), reg.completePasswordResetLink)
}

func (reg *Registry) createAdminUserPasswordReset(ctx context.Context, in *AdminPasswordResetInput) (*AdminPasswordResetOutput, error) {
	svc, p := reg.passwordResets()
	if p != nil {
		return nil, p
	}
	id, p := adminAccountID(in.ID)
	if p != nil {
		return nil, p
	}
	delivery := passwordreset.Delivery(in.Body.Delivery)
	result, err := svc.IssuePasswordReset(ctx, passwordreset.IssueInput{UserID: id, IssuedBy: claimsFrom(ctx).UserID, Delivery: delivery})
	if result == nil {
		return nil, passwordResetProblem(err)
	}
	out := AdminPasswordReset{Delivery: in.Body.Delivery, DeliveryStatus: "not_requested", ResetURL: result.URL, ExpiresAt: NewInstant(result.ExpiresAt)}
	if delivery == passwordreset.DeliveryEmail {
		out.DeliveryStatus = "sent"
		if err != nil || !result.EmailSent {
			out.DeliveryStatus = "failed_or_unknown"
		}
	}
	return &AdminPasswordResetOutput{Body: out}, nil
}

func (reg *Registry) getPasswordResetCapability(ctx context.Context, _ *CapabilityInput) (*PasswordResetCapabilityOutput, error) {
	out := &PasswordResetCapabilityOutput{Body: PasswordResetCapability{Capability{State: StateNotConfigured}}}
	if reg.deps.PasswordResets == nil {
		return out, nil
	}
	enabled, configured, err := reg.deps.PasswordResets.PasswordResetSelfService(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out.Body.State = configuredEnabledCapabilityState(configured, enabled)
	return out, nil
}

func (reg *Registry) requestPasswordReset(ctx context.Context, in *PasswordResetRequestInput) (*struct{}, error) {
	svc, p := reg.passwordResets()
	if p != nil {
		return nil, p
	}
	switch err := svc.RequestPasswordReset(ctx, in.Body.Login); {
	case err == nil:
		return &struct{}{}, nil
	case errors.Is(err, passwordreset.ErrSelfServiceDisabled):
		return nil, CapabilityProblem(StateDisabled, selfServiceResetDomain)
	case errors.Is(err, passwordreset.ErrSelfServiceNotConfigured):
		return nil, CapabilityProblem(StateNotConfigured, selfServiceResetDomain)
	default:
		return nil, serviceProblem(err)
	}
}

func (reg *Registry) lookupPasswordResetLink(ctx context.Context, in *PasswordResetLinkInput) (*PasswordResetLookupOutput, error) {
	svc, p := reg.passwordResets()
	if p != nil {
		return nil, p
	}
	view, err := svc.LookupPasswordReset(ctx, in.Body.Token)
	if err != nil {
		return nil, passwordResetProblem(err)
	}
	return &PasswordResetLookupOutput{Body: PasswordResetLookup{Username: view.Username, ServerName: view.ServerName, ExpiresAt: NewInstant(view.ExpiresAt)}}, nil
}

func (reg *Registry) completePasswordResetLink(ctx context.Context, in *PasswordResetCompleteInput) (*PasswordResetCompleteOutput, error) {
	svc, p := reg.passwordResets()
	if p != nil {
		return nil, p
	}
	view, err := svc.CompletePasswordReset(withClientDevice(ctx), in.Body.Token, in.Body.Password, in.UserAgent, clientip.FromContext(ctx))
	if err != nil && !errors.Is(err, passwordreset.ErrSessionStart) {
		return nil, passwordResetProblem(err)
	}
	out := PasswordResetCompletion{Status: passwordResetCompleted, LoginStatus: "sign_in_required", Username: view.Username}
	if view.Tokens != nil && err == nil {
		out.LoginStatus = "signed_in"
		out.Tokens = new(tokenPairFromView(*view.Tokens))
	}
	return &PasswordResetCompleteOutput{Body: out}, nil
}

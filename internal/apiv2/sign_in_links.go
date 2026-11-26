package apiv2

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/auth/passwordhash"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/signinlinks"
)

// SignInLinkService makes and redeems sign-in links
// (*handlers.SignInLinkHandler).
type SignInLinkService interface {
	SignInLinkEmailAvailable(context.Context) bool
	IssueSignInLink(context.Context, signinlinks.IssueInput) (*signinlinks.IssueResult, error)
	RedeemSignInLink(ctx context.Context, token string, keysOn bool) (auth.SignInLinkRedemption, error)
	SetPasswordWithSignInLink(ctx context.Context, token, newPassword string) error
}

// AdminSignInLinkInput makes a sign-in link for one account.
type AdminSignInLinkInput struct {
	UserID  ID `path:"user_id"`
	RawBody []byte
	Body    struct {
		SendEmail bool `json:"send_email,omitempty" doc:"Also email the link to the account's address. The link is returned either way" example:"false"`
	}
}

// AdminSignInLink is a new sign-in link.
type AdminSignInLink struct {
	URL            string  `json:"url" format:"uri" doc:"The link: the server address, /sign-in-link, and the token in the fragment, which browsers never send to the server. A bearer credential for the account until it is redeemed or expires; give it only to the person" example:"https://media.example.com/sign-in-link#8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA"`
	ExpiresAt      Instant `json:"expires_at" doc:"When the link stops working, 24 hours after it was made"`
	DeliveryStatus string  `json:"delivery_status" enum:"sent,failed_or_unknown,not_requested" doc:"With send_email: sent, or failed_or_unknown when the mail server did not confirm delivery (the link is live; send it another way). Without: not_requested" example:"not_requested"`
}

// AdminSignInLinkOutput is the createAdminUserSignInLink response. Like
// every credential response it is Cache-Control: no-store.
type AdminSignInLinkOutput struct {
	Body AdminSignInLink
}

// SignInLinkRedeemInput trades a sign-in link's token.
type SignInLinkRedeemInput struct {
	RawBody []byte
	Body    struct {
		Token string `json:"token" minLength:"1" maxLength:"128" doc:"The token from the sign-in link's fragment"`
	}
}

// SignInLinkRedeemed is what a redeemed sign-in link hands its holder.
// Every member is optional: code, expires_in, server_address and
// link_allowed come together, only while app key sign-in is on.
type SignInLinkRedeemed struct {
	Code             string `json:"code,omitempty" doc:"An app code for the account, for the Silo app to redeem (redeemAppCode). A session code: it signs the Silo app in to an existing account, so it is never put in a link. Absent while auth.app_key_sign_in is off" example:"8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA"`
	ExpiresIn        int    `json:"expires_in,omitempty" doc:"Seconds the code stays good. Present with code" example:"600"`
	ServerAddress    string `json:"server_address,omitempty" doc:"The server address the app connects to, as createAppCode answers it. Present with code" example:"https://media.example.com"`
	LinkAllowed      *bool  `json:"link_allowed,omitempty" doc:"Present with code, and always false: show the server address and the code as text to type in the app, and never build a link or anything a device could open from them" example:"false"`
	SetPasswordToken string `json:"set_password_token,omitempty" doc:"For setSignInLinkPassword, when the account may set a password: local password sign-in is on for this server. Lives an hour, never past the link's own expiry, and works once" example:"Zt1Wq3m0sP2Kc8vY5dQhX7nB4rJ6eL9aF0uG2iT1oMw"`
}

// SignInLinkRedeemedOutput is the redeemSignInLink response, no-store.
type SignInLinkRedeemedOutput struct {
	Body SignInLinkRedeemed
}

// SignInLinkSetPasswordInput sets a password with a redeemed link's token.
type SignInLinkSetPasswordInput struct {
	RawBody []byte
	Body    struct {
		Token       string `json:"token" minLength:"1" maxLength:"128" doc:"set_password_token from redeemSignInLink"`
		NewPassword string `json:"new_password" minLength:"8" maxLength:"72" doc:"The new password" example:"margin fossil quench hollow"`
	}
}

// The delivery_status values of an emailed link, shared with
// createAdminUserPasswordReset.
const (
	deliveryNotRequested    = "not_requested"
	deliverySent            = "sent"
	deliveryFailedOrUnknown = "failed_or_unknown"
)

// signInLinkUnknownDetail is the one answer for a link or token that is
// unknown, used or expired, or whose account is disabled.
const signInLinkUnknownDetail = "This sign-in link is unknown, used or expired. Ask the server's owner for a new one."

// signInLinkNeedsProviderDetail explains why a sign-in link is refused
// while local password sign-in is off.
const signInLinkNeedsProviderDetail = "Local password sign-in is off on this server, so a sign-in link would skip its sign-in provider. The person signs in with the provider on this server's site, then uses Open in Silo from their account settings."

func registerSignInLinks(reg *Registry) {
	issue := adminAccountOperation(http.MethodPost, "/{user_id}/sign-in-links", "createAdminUserSignInLink", false)
	issue.Summary = "Make a single-use sign-in link for an account, for a person who lost every device."
	issue.Description = "Answers the link to give the person, and emails it to the account's address with send_email. The link's page trades it once for an app code the Silo app redeems (redeemSignInLink) and, while local password sign-in is on, a token that sets a password. A new link replaces the account's earlier links. Who may make one is who may set that account's password as an administrator (updateAdminUser with password): any administrator for an ordinary account, only the server Owner for another administrator or the Owner, only the Owner for an account of their own whose password sign-in is off, and never a scoped API key for an administrator (403 permission_denied). A disabled account is 409 conflict. With send_email, an account with no email address is 409 conflict and a server without mail 409 capability_not_configured, before any link is made. While local password sign-in is off the answer is 409 provider_sign_in_required: the person signs in with the provider on the server's site and uses createAppCode instead. Keys a link's code enrolls start active, also on an administrator's account. Audited as user.sign_in_link_created with the delivery and expiry, never the token. The response is no-store."
	issue.DefaultStatus = http.StatusCreated
	issue.Errors = []int{http.StatusNotFound, http.StatusForbidden, http.StatusConflict}
	Register(reg, issue, reg.createAdminUserSignInLink)

	public := func(path, id, summary, description string) Operation {
		op := humaOp(http.MethodPost, Prefix+path, id, "auth", summary)
		op.Description = description
		op.Errors = []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
		return Operation{Operation: op, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true, Audited: true,
			RateLimitBucket: bucketPasswordReset, MaxBodyBytes: 4 << 10}
	}
	Register(reg, public("/auth/sign-in-links/redeem", "redeemSignInLink",
		"Trade a sign-in link for an app code and a set-password token.",
		"The sign-in link page calls this when the person presses Continue, never on load: the link works once. In one transaction the link is spent and an app code of its own kind is made for the account; of two redemptions exactly one succeeds. The code enrolls a person key and a device key at redeemAppCode, neither bound to a sign-in provider; the person key replaces a different one the account holds (person_key replaced in the sealed answer), which is revoked with the devices it introduced that wait for approval, while devices already enrolled keep their keys. While auth.app_key_sign_in is off, or the server is in demo mode, the answer has only set_password_token. An unknown, used or expired link, and one whose account is disabled, is 404 not_found. While local password sign-in is off it is 409 provider_sign_in_required and the link stays unspent. Spends the per-address password_reset rate-limit budget. Audited as user.sign_in_link_redeemed. The response is no-store."),
		reg.redeemSignInLink)
	Register(reg, public("/auth/sign-in-links/set-password", "setSignInLinkPassword",
		"Set the account's password with a redeemed sign-in link's token.",
		"The password passes the rules a password reset's completion checks (422 at body.new_password, and the token is not spent). It sets the password, turns the account's local password sign-in on, and signs the account out everywhere as a password reset does; its app keys stay, and devices signed in with them sign back in. It signs nobody in: sign in with the new password next. An unknown, used or expired token is 404 not_found; while local password sign-in is off, 409 provider_sign_in_required. Spends the per-address password_reset rate-limit budget. Audited as user.sign_in_link_password_set. Answers 204."),
		reg.setSignInLinkPassword)
}

func (reg *Registry) signInLinks() (SignInLinkService, *Problem) {
	if reg.deps.SignInLinks == nil {
		return nil, unavailable("sign-in links")
	}
	return reg.deps.SignInLinks, nil
}

func (reg *Registry) createAdminUserSignInLink(ctx context.Context, in *AdminSignInLinkInput) (*AdminSignInLinkOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, p := reg.signInLinks()
	if p != nil {
		return nil, p
	}
	id, p := adminAccountID(in.UserID)
	if p != nil {
		return nil, p
	}
	address, ok := reg.appCodeServerAddress(ctx)
	if !ok {
		return nil, NewProblem(TypeInternalError, "This server could not work out the address it was reached on.")
	}
	result, err := svc.IssueSignInLink(ctx, signinlinks.IssueInput{
		UserID: id, IssuedBy: claimsFrom(ctx).UserID, SendEmail: in.Body.SendEmail, ServerAddress: address,
	})
	if result == nil {
		return nil, signInLinkProblem(err)
	}
	out := AdminSignInLink{URL: result.URL, ExpiresAt: NewInstant(result.ExpiresAt), DeliveryStatus: deliveryNotRequested}
	if in.Body.SendEmail {
		out.DeliveryStatus = deliverySent
		if err != nil || !result.EmailSent {
			out.DeliveryStatus = deliveryFailedOrUnknown
		}
	}
	return &AdminSignInLinkOutput{Body: out}, nil
}

func (reg *Registry) redeemSignInLink(ctx context.Context, in *SignInLinkRedeemInput) (*SignInLinkRedeemedOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, p := reg.signInLinks()
	if p != nil {
		return nil, p
	}
	keysOn := false
	if keys := reg.deps.AppKeys; keys != nil {
		on, err := reg.appKeySwitch(ctx, keys)
		if err != nil {
			return nil, serviceProblem(err)
		}
		keysOn = on
	}
	var address string
	if keysOn {
		var ok bool
		if address, ok = reg.appCodeServerAddress(ctx); !ok {
			return nil, NewProblem(TypeInternalError, "This server could not work out the address it was reached on.")
		}
	}
	redeemed, err := svc.RedeemSignInLink(ctx, in.Body.Token, keysOn)
	if err != nil {
		return nil, signInLinkProblem(err)
	}
	out := SignInLinkRedeemed{SetPasswordToken: redeemed.SetPasswordToken}
	if code := redeemed.AppCode; code != nil {
		out.Code, out.ExpiresIn, out.ServerAddress = code.Code, code.ExpiresIn(time.Now()), address
		out.LinkAllowed = new(code.LinkAllowed)
	}
	return &SignInLinkRedeemedOutput{Body: out}, nil
}

func (reg *Registry) setSignInLinkPassword(ctx context.Context, in *SignInLinkSetPasswordInput) (*struct{}, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	svc, p := reg.signInLinks()
	if p != nil {
		return nil, p
	}
	if err := svc.SetPasswordWithSignInLink(ctx, in.Body.Token, in.Body.NewPassword); err != nil {
		return nil, signInLinkProblem(err)
	}
	return nil, nil
}

// signInLinkProblem maps a refusal of the sign-in link operations. The
// account and email refusals are the ones a password reset link answers.
func signInLinkProblem(err error) error {
	if p, ok := passwordRuleProblem(err, "new_password"); ok {
		return p
	}
	var apiErr *handlers.APIError
	switch {
	case errors.As(err, &apiErr):
		return serviceProblem(err)
	case errors.Is(err, auth.ErrSignInLinkInvalid):
		return NewProblem(TypeNotFound, signInLinkUnknownDetail)
	case errors.Is(err, auth.ErrSignInLinkNeedsProvider):
		return NewProblem(TypeProviderSignInRequired, signInLinkNeedsProviderDetail)
	case errors.Is(err, auth.ErrNotFound):
		return NewProblem(TypeNotFound, "Account not found.")
	case errors.Is(err, auth.ErrUserDisabled):
		return NewProblem(TypeConflict, "The account is disabled.")
	case errors.Is(err, signinlinks.ErrNoEmail):
		return NewProblem(TypeConflict, "The account has no email address; share the link instead.")
	case errors.Is(err, mail.ErrNotConfigured):
		return NewProblem(TypeCapabilityNotConfigured, "Email is not configured; share the link instead.")
	case errors.Is(err, passwordhash.ErrBusy):
		return passwordHashBusyProblem()
	}
	return NewProblem(TypeInternalError, "Unable to process the sign-in link.")
}

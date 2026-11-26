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
	SignInLinkCapabilities(context.Context) (signinlinks.Capabilities, error)
	IssueSignInLink(context.Context, signinlinks.IssueInput) (*signinlinks.IssueResult, error)
	RedeemSignInLink(ctx context.Context, token string, canDeliverCode bool) (auth.SignInLinkRedemption, error)
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
// link_allowed come together, only while app key sign-in is on and for an
// account that is not an administrator's; otherwise no_code_reason says
// why there is no code.
type SignInLinkRedeemed struct {
	Code             string `json:"code,omitempty" doc:"An app code for the account, for the Silo app to redeem (redeemAppCode). A session code: it signs the Silo app in to an existing account, so it is never put in a link. Absent while auth.app_key_sign_in is off and for an administrator's account (no_code_reason says which)" example:"8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA"`
	NoCodeReason     string `json:"no_code_reason,omitempty" enum:"app_keys_off,administrator" doc:"Present when code is absent, saying why: administrator when the account is an administrator's at the moment of redeeming, where a link gives the password route only (set a password, sign in on the server's site, then use createAppCode from that session); app_keys_off, for an account that is not an administrator's, while auth.app_key_sign_in is off or the server is in demo mode. When both apply, administrator is sent. A client that does not know a value treats it as no code" example:"administrator"`
	ExpiresIn        int    `json:"expires_in,omitempty" doc:"Seconds the code stays good. Present with code" example:"600"`
	ServerAddress    string `json:"server_address,omitempty" doc:"The server address the app connects to, as createAppCode answers it. Present with code" example:"https://media.example.com"`
	LinkAllowed      *bool  `json:"link_allowed,omitempty" doc:"Present with code, and always false: show the server address and the code as text to type or copy in the app, and never build a link or anything a device could open from them. A scan code (text only the Silo app's own scanner reads, drawn as a QR code) is not such a link" example:"false"`
	SetPasswordToken string `json:"set_password_token,omitempty" doc:"For setSignInLinkPassword. Present on every successful redeem, which happens only for an account that may sign in with a local password on this server: a break-glass account always (so also while local password sign-in is off for the server), any other while the switch is on and no network sign-in provider refuses it. Lives an hour, never past the link's own expiry, and works once" example:"Zt1Wq3m0sP2Kc8vY5dQhX7nB4rJ6eL9aF0uG2iT1oMw"`
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
	issue.Description = "Answers the link to give the person, and emails it to the account's address with send_email. The link is built on the server's public URL (server.public_url), as a password reset link is; a server without one answers 409 capability_not_configured and makes no link. An email goes only to the address the account has when the link is stored: one changed since is 409 conflict. A new link, a password set by any route, a change of the account's email address, signing the account out everywhere or out of every other session, disabling the account and promoting it each end the link, the code its redeem left and its set-password token. The link's page trades it once (redeemSignInLink) for a token that sets a password and, unless the account is an administrator's or app keys are off (or the server is in demo mode), an app code the Silo app redeems. A new link replaces the account's earlier links. A scoped API key can never make one (403 permission_denied from the scope check: no scope names this operation, and a link is at least as strong as setting a password). Otherwise who may make one follows the rule for setting that account's password as an administrator (updateAdminUser with password): any administrator for an ordinary account, only the server Owner for another administrator or the Owner, and only the Owner for an account of their own whose password sign-in is off (403 permission_denied). An account a sign-in provider manages, with its own password sign-in off, can have a link as it can have its password set: setting a password with the link turns its local sign-in on. A disabled account is 409 conflict. With send_email, an account with no email address is 409 conflict and a server without mail 409 capability_not_configured, before any link is made. A link is made only for an account allowed to sign in with a local password on this server, by password sign-in's server-and-account-state rule (the admin account view's local_password_allowed): a break-glass account always; any other while the server-wide auth.local_password_login switch is on and no network identity provider refuses the account. The rule leaves out the account's own password sign-in flag, since setting a password with the link turns it on. An account that fails that rule is refused in one of two ways: 409 provider_sign_in_required while the switch is off (the person signs in with the provider on the server's site and uses createAppCode instead), and 403 not_permitted while a network sign-in provider refuses the account. For an administrator's account the link gives the password route only: its redeem hands out no app code. Audited as user.sign_in_link_created with the link's sign_in_link_id, the delivery and the expiry, never the token. The response is no-store."
	issue.DefaultStatus = http.StatusCreated
	issue.Errors = []int{http.StatusNotFound, http.StatusForbidden, http.StatusConflict}
	Register(reg, issue, reg.createAdminUserSignInLink)

	public := func(path, id, summary, description string) Operation {
		op := humaOp(http.MethodPost, Prefix+path, id, "auth", summary)
		op.Description = description
		op.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}
		return Operation{Operation: op, RetrySafety: RetrySafetyNonRetryable, Class: ClassPublic, ServiceBacked: true, Audited: true,
			RateLimitBucket: bucketPasswordReset, MaxBodyBytes: 4 << 10}
	}
	redeemOp := public("/auth/sign-in-links/redeem", "redeemSignInLink",
		"Trade a sign-in link for a set-password token and, on the app-code path, an app code.",
		"The sign-in link page calls this when the person presses Continue, never on load: the link works once. The answer has one of two shapes: code, expires_in, server_address and link_allowed (false) together, with set_password_token when the account may set a password; or set_password_token and no_code_reason alone. The server sends no other shape. In one transaction the link is spent and, on the app-code path (not for an administrator's account, not while app keys are off or the server is in demo mode, read inside that transaction), an app code of its own kind is made for the account; of two redemptions exactly one succeeds. The code enrolls a person key and a device key at redeemAppCode: a new or replacing person key and its device start unbound to any sign-in provider, while the account's own person key sent again keeps its provider binding and its device takes it; the person key replaces a different one the account holds (person_key replaced in the sealed answer), which is revoked with the devices it introduced that wait for approval, while devices already enrolled keep their keys. For an account that is an administrator's at the moment of redeeming (judged under the account lock in the transaction that spends the link, so an account demoted since the link was made gets a code when the app-code path is available: app keys on and the server not in demo mode) the answer has only set_password_token and no_code_reason administrator, and no key is enrolled: the person sets a password, signs in on this server's site, and opens the app from there with createAppCode. While auth.app_key_sign_in is off, or the server is in demo mode, the answer for an account that is not an administrator's has only set_password_token and no_code_reason app_keys_off; when both apply, no_code_reason is administrator. An unknown, used or expired link, and one whose account is disabled, is 404 not_found. For an account that may not sign in with a local password on this server (it is not break-glass, and local password sign-in is off or a network sign-in provider refuses it) it is 409 provider_sign_in_required (the switch off) or 403 not_permitted (a network identity provider refuses it), and the link stays unspent. Spends the per-address password_reset rate-limit budget. Audited as user.sign_in_link_redeemed. The response is no-store.")
	Register(reg, redeemOp, reg.redeemSignInLink)
	setPasswordOp := public("/auth/sign-in-links/set-password", "setSignInLinkPassword",
		"Set the account's password with a redeemed sign-in link's token.",
		"The password passes the rules a password reset's completion checks (422 at body.new_password, and the token is not spent). It sets the password, turns the account's local password sign-in on, and signs the account out everywhere as a password reset does; its app keys stay, and devices signed in with them sign back in. It signs nobody in: sign in with the new password next. An unknown, used or expired token is 404 not_found; for an account that may not sign in with a local password on this server (it is not break-glass, and local password sign-in is off or a network sign-in provider refuses it), 409 provider_sign_in_required or 403 not_permitted respectively, and the token stays unspent. Spends the per-address password_reset rate-limit budget. Audited as user.sign_in_link_password_set. Answers 204.")
	Register(reg, setPasswordOp, reg.setSignInLinkPassword)
	declareRetryAfter(reg, "Seconds to wait before trying again.", redeemOp, setPasswordOp)
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
	result, err := svc.IssueSignInLink(ctx, signinlinks.IssueInput{
		UserID: id, IssuedBy: claimsFrom(ctx).UserID, SendEmail: in.Body.SendEmail,
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
	// Whether app keys are on is read inside the redemption's transaction;
	// here only whether a code could be handed out: app keys are wired and
	// the server address the app connects to is known.
	address, ok := reg.appCodeServerAddress(ctx)
	redeemed, err := svc.RedeemSignInLink(ctx, in.Body.Token, ok && reg.deps.AppKeys != nil)
	if err != nil {
		return nil, signInLinkProblem(err)
	}
	out, ok := signInLinkRedeemedAnswer(redeemed, address, time.Now())
	if !ok {
		return nil, NewProblem(TypeInternalError, "Unable to process the sign-in link.")
	}
	return &SignInLinkRedeemedOutput{Body: out}, nil
}

// signInLinkRedeemedAnswer is redeemSignInLink's answer, in one of its two
// shapes: the code with expires_in, server_address and link_allowed (false),
// with or without set_password_token; or set_password_token alone with
// no_code_reason. ok is false for anything else, which the route answers as
// an internal error rather than a 200 a client can't use.
func signInLinkRedeemedAnswer(redeemed auth.SignInLinkRedemption, address string, now time.Time) (SignInLinkRedeemed, bool) {
	if code := redeemed.AppCode; code != nil {
		if code.Code == "" || address == "" || code.LinkAllowed || redeemed.NoCode != "" {
			return SignInLinkRedeemed{}, false
		}
		return SignInLinkRedeemed{Code: code.Code, ExpiresIn: code.ExpiresIn(now), ServerAddress: address,
			LinkAllowed: new(false), SetPasswordToken: redeemed.SetPasswordToken}, true
	}
	if redeemed.SetPasswordToken == "" || redeemed.NoCode == "" {
		return SignInLinkRedeemed{}, false
	}
	return SignInLinkRedeemed{SetPasswordToken: redeemed.SetPasswordToken, NoCodeReason: redeemed.NoCode}, true
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
	case errors.Is(err, auth.ErrSignInLinkNoAddress):
		return NewProblem(TypeInternalError, "This server could not work out the address it was reached on.")
	case errors.Is(err, auth.ErrSignInLinkNotPermitted):
		return NewProblem(TypeNotPermitted, "The sign-in provider does not permit this account to sign in, so a sign-in link would not let it in.")
	case errors.Is(err, auth.ErrNotFound):
		return NewProblem(TypeNotFound, "Account not found.")
	case errors.Is(err, auth.ErrUserDisabled):
		return NewProblem(TypeConflict, "The account is disabled.")
	case errors.Is(err, signinlinks.ErrNoEmail):
		return NewProblem(TypeConflict, "The account has no email address; share the link instead.")
	case errors.Is(err, mail.ErrNotConfigured):
		return NewProblem(TypeCapabilityNotConfigured, "Email is not configured; share the link instead.")
	case errors.Is(err, signinlinks.ErrNoLinkBase):
		return NewProblem(TypeCapabilityNotConfigured, "Sign-in links need the server's public URL; set it in the server settings.")
	case errors.Is(err, auth.ErrSignInLinkAccountChanged):
		return NewProblem(TypeConflict, "The account's email address changed while making the link. Reload the account and try again.")
	case errors.Is(err, passwordhash.ErrBusy):
		return passwordHashBusyProblem()
	}
	return NewProblem(TypeInternalError, "Unable to process the sign-in link.")
}

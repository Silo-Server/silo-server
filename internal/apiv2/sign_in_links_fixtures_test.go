package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/signinlinks"
)

// fixtureSignInLinks answers the sign-in link fixtures. The token
// "keys-off-token" answers as a server with app key sign-in off does, and
// "admin-token" as a link for an administrator's account: only the
// set-password token, with why. "used-token" is a spent link.
type fixtureSignInLinks struct{}

func (fixtureSignInLinks) SignInLinkEmailAvailable(context.Context) bool { return true }

func (fixtureSignInLinks) IssueSignInLink(_ context.Context, in signinlinks.IssueInput) (*signinlinks.IssueResult, error) {
	return &signinlinks.IssueResult{URL: in.ServerAddress + signinlinks.PagePath + "#8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA",
		ExpiresAt: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}, nil
}

func (fixtureSignInLinks) RedeemSignInLink(_ context.Context, token string, _ bool) (auth.SignInLinkRedemption, error) {
	out := auth.SignInLinkRedemption{SetPasswordToken: "Zt1Wq3m0sP2Kc8vY5dQhX7nB4rJ6eL9aF0uG2iT1oMw"}
	switch token {
	case "used-token":
		return auth.SignInLinkRedemption{}, auth.ErrSignInLinkInvalid
	case "keys-off-token":
		out.NoCode = auth.NoCodeAppKeysOff
		return out, nil
	case "admin-token":
		out.NoCode = auth.NoCodeAdministrator
		return out, nil
	}
	out.AppCode = &auth.IssuedAppCode{Code: "Hc3o3zH8l1M0lYqk2k7lQZx0bq2f2tTj6a4q9d1Xb2E", ExpiresAt: time.Now().Add(auth.AppCodeLifetime + 500*time.Millisecond)}
	return out, nil
}

func (fixtureSignInLinks) SetPasswordWithSignInLink(context.Context, string, string) error {
	return nil
}

// signInLinkFixtureCases are the sign-in link shapes the web page and the
// apps decode.
func signInLinkFixtureCases() []fixtureCase {
	ok := []string{"Content-Type", "Cache-Control"}
	return []fixtureCase{
		{name: "create_admin_user_sign_in_link_ok", operationID: "createAdminUserSignInLink",
			scenario: "An administrator makes a sign-in link for a person who lost every device, to copy and send themselves.",
			method:   http.MethodPost, path: "/api/v2/admin/users/2/sign-in-links", body: `{"send_email":false}`, headers: bearer(adminToken),
			status: http.StatusCreated, assertHeaders: ok, schema: "#/components/schemas/AdminSignInLink"},
		{name: "redeem_sign_in_link_ok", operationID: "redeemSignInLink",
			scenario: "The link page, after Continue: an app code to type into the Silo app, never put in a link, and a set-password token.",
			method:   http.MethodPost, path: "/api/v2/auth/sign-in-links/redeem", body: `{"token":"link-token"}`,
			status: http.StatusOK, assertHeaders: ok, schema: "#/components/schemas/SignInLinkRedeemed"},
		{name: "redeem_sign_in_link_keys_off", operationID: "redeemSignInLink",
			scenario: "The same while the server has app key sign-in off: only the set-password token.",
			method:   http.MethodPost, path: "/api/v2/auth/sign-in-links/redeem", body: `{"token":"keys-off-token"}`,
			status: http.StatusOK, assertHeaders: ok, schema: "#/components/schemas/SignInLinkRedeemed"},
		{name: "redeem_sign_in_link_not_found", operationID: "redeemSignInLink",
			scenario: "A link that is unknown, used or expired: the page asks the person to get a new one from the server's owner.",
			method:   http.MethodPost, path: "/api/v2/auth/sign-in-links/redeem", body: `{"token":"used-token"}`,
			status: http.StatusNotFound, assertHeaders: ok, schema: "#/components/schemas/Problem"},
		{name: "redeem_sign_in_link_administrator", operationID: "redeemSignInLink",
			scenario: "A link for an administrator's account: the password route only, no app code; the page says to set a password, sign in on the site and open the app from there.",
			method:   http.MethodPost, path: "/api/v2/auth/sign-in-links/redeem", body: `{"token":"admin-token"}`,
			status: http.StatusOK, assertHeaders: ok, schema: "#/components/schemas/SignInLinkRedeemed"},
	}
}

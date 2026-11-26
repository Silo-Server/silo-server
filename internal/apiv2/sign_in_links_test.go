package apiv2

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// The redeem answers one of two shapes and nothing else: the code with its
// three companions (with or without the set-password token), or the
// set-password token alone with why there is no code. Regression guard: an
// empty or half answer used to go out as a 200 a client could not use.
func TestSignInLinkRedeemedAnswerShapes(t *testing.T) {
	now := time.Now()
	code := &auth.IssuedAppCode{Code: "8bqRFq-gVv5Dxjb61BaVfnDB3-F24muoSMMiatX49VA", ExpiresAt: now.Add(auth.AppCodeLifetime)}
	for _, tc := range []struct {
		name     string
		redeemed auth.SignInLinkRedemption
		address  string
		ok       bool
	}{
		{"a code with the token", auth.SignInLinkRedemption{AppCode: code, SetPasswordToken: "t"}, "https://media.example.test", true},
		{"a code alone", auth.SignInLinkRedemption{AppCode: code}, "https://media.example.test", true},
		{"the token alone, with why", auth.SignInLinkRedemption{SetPasswordToken: "t", NoCode: auth.NoCodeAdministrator}, "", true},
		{"nothing", auth.SignInLinkRedemption{}, "", false},
		{"the token without why", auth.SignInLinkRedemption{SetPasswordToken: "t"}, "", false},
		{"a code with no address", auth.SignInLinkRedemption{AppCode: code, SetPasswordToken: "t"}, "", false},
		{"a code a link may carry", auth.SignInLinkRedemption{AppCode: &auth.IssuedAppCode{Code: "c", ExpiresAt: now, LinkAllowed: true}}, "https://media.example.test", false},
		{"a code and a reason", auth.SignInLinkRedemption{AppCode: code, NoCode: auth.NoCodeAppKeysOff}, "https://media.example.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := signInLinkRedeemedAnswer(tc.redeemed, tc.address, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			withCode := got.Code != "" && got.ExpiresIn > 0 && got.ServerAddress != "" && got.LinkAllowed != nil && !*got.LinkAllowed && got.NoCodeReason == ""
			tokenOnly := got.Code == "" && got.ExpiresIn == 0 && got.ServerAddress == "" && got.LinkAllowed == nil && got.SetPasswordToken != "" && got.NoCodeReason != ""
			if withCode == tokenOnly {
				t.Fatalf("answer %+v is not one of the two shapes", got)
			}
		})
	}
}

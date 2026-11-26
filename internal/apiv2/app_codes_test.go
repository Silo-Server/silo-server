package apiv2

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// The app code routes answer 409 conflict for causes with opposite remedies:
// a taken name sends the person back to the page, an enrolled key makes the
// app create a new key. Apps tell them apart by errors[0].code and location,
// never by the detail, so each cause is pinned here.
func TestAppCodeConflictCauses(t *testing.T) {
	redeemBody := `{"person_key":"` + testPersonKey + `","message":{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","device_sig":"c2ln"}}`
	for _, tc := range []struct {
		name, path, body       string
		keysErr, invitationErr error
		code, location         string
	}{
		{name: "redeem, name taken since", path: "/auth/app-codes/redeem", body: redeemBody,
			keysErr: &auth.NameTakenError{}, code: codeDuplicate, location: "body.message.payload"},
		{name: "redeem, person key enrolled", path: "/auth/app-codes/redeem", body: redeemBody,
			keysErr: &auth.KeyEnrolledError{Member: "person_key"}, code: codeKeyEnrolled, location: "body.person_key"},
		{name: "redeem, device key enrolled", path: "/auth/app-codes/redeem", body: redeemBody,
			keysErr: &auth.KeyEnrolledError{Member: "device_key"}, code: codeKeyEnrolled, location: "body.message.payload"},
		{name: "issue, username taken", path: "/invitations/app-code", body: `{"token":"t","username":"sam"}`,
			invitationErr: &auth.NameTakenError{Field: "username"}, code: codeDuplicate, location: "body.username"},
		{name: "issue, email taken", path: "/invitations/app-code", body: `{"token":"t","username":"sam","email":"sam@example.test"}`,
			invitationErr: &auth.NameTakenError{Field: "email"}, code: codeDuplicate, location: "body.email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := appKeyDeps(&fakeAppKeys{enabled: true, err: tc.keysErr})
			invites := fixtureInvitations()
			invites.appCodeErr = tc.invitationErr
			deps.Invitations = invites
			p := requireProblem(t, do(t, NewHandler(deps), http.MethodPost, Prefix+tc.path, tc.body, nil), TypeConflict)
			if len(p.Errors) != 1 || p.Errors[0].Code != tc.code || p.Errors[0].Location != tc.location {
				t.Fatalf("errors = %+v, want %s at %s", p.Errors, tc.code, tc.location)
			}
		})
	}
}

// The invite page offers the Silo app only where a code could be redeemed:
// an invitation that creates a profile the store can't create in the claim
// transaction is refused by accept, and so its app path is off too.
func TestInvitationLookupAppJoinFollowsAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		profile    bool
		wantJoin   bool
		wantAccept bool
	}{
		{name: "profiles in the claim transaction", profile: true, wantJoin: true, wantAccept: true},
		{name: "profiles outside it", profile: false, wantJoin: false, wantAccept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invites := fixtureInvitations()
			invites.profile = tc.profile
			deps := appKeyDeps(&fakeAppKeys{enabled: true})
			deps.Invitations = invites
			rec := do(t, NewHandler(deps), http.MethodPost, Prefix+"/invitations/lookup", `{"token":"pending"}`, nil)
			var got InvitationLookup
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("lookup %d: %s", rec.Code, rec.Body.String())
			}
			if got.AppJoin != tc.wantJoin || got.AcceptanceAvailable != tc.wantAccept {
				t.Fatalf("app_join %v, acceptance_available %v", got.AppJoin, got.AcceptanceAvailable)
			}
		})
	}
}

package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// mainProfile is tok's request acting as account 1's (and account 2's)
// primary profile, which has no PIN.
func mainProfile(tok string) map[string]string {
	return with(bearer(tok), "X-Profile-Id", "p-owner")
}

// lockedAdminHouseholds gives account 2 (an administrator) a PIN-protected
// primary profile; every other account is as fakeHouseholds has it.
type lockedAdminHouseholds struct{ fakeHouseholds }

func (h lockedAdminHouseholds) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	if userID == 2 {
		return fakeHouseholdStore{profiles: []userstore.Profile{{ID: "p-primary-locked", IsPrimary: true, PINHash: "hash"}}}, nil
	}
	return h.fakeHouseholds.ForUser(ctx, userID)
}

const pendingKeyPath = "/auth/keys/pending/0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11"

// Enrolling keys, replacing the person key and approving a waiting device
// give lasting access to the account every profile of the household shares,
// so only the household's main profile, verified with its PIN when it has
// one, may: a secondary profile and an unverified main profile are refused
// before the service runs. A request that names no profile passes only from
// an administrator account whose profiles are all unlimited.
func TestKeyCreatingRoutesNeedVerifiedMainProfile(t *testing.T) {
	enrollBody := `{"person_key":"` + testPersonKey + `","message":{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","device_sig":"c2ln"}}`
	routes := []struct{ method, path, body string }{
		{method: http.MethodPost, path: "/auth/keys/enroll", body: enrollBody},
		{method: http.MethodPut, path: "/auth/keys/person", body: `{"person_key":"` + testPersonKey + `"}`},
		{method: http.MethodPost, path: pendingKeyPath + "/approve", body: `{}`},
	}
	locked := with(bearer(householdToken), "X-Profile-Id", "p-primary-locked")
	for _, tc := range []struct {
		name       string
		headers    map[string]string
		households userstore.UserStoreProvider
		refused    *ProblemType
	}{
		{name: "secondary profile", headers: with(bearer(householdToken), "X-Profile-Id", "p-kid"), refused: &TypePermissionDenied},
		{name: "main profile with a PIN, unverified", headers: locked, refused: &TypeProfileVerificationRequired},
		{name: "main profile with a PIN, verified", headers: with(locked, "X-Profile-Token", "ptok")},
		{name: "main profile without a PIN", headers: mainProfile(memberToken)},
		{name: "one profile without a PIN, no header, not an administrator", headers: bearer(memberToken), refused: &TypePermissionDenied},
		{name: "one profile without a PIN, no header, administrator", headers: bearer(adminToken)},
		{name: "main profile with a PIN, no header, administrator", headers: bearer(adminToken), households: lockedAdminHouseholds{}, refused: &TypePermissionDenied},
	} {
		for _, route := range routes {
			t.Run(tc.name+" "+route.path, func(t *testing.T) {
				keys := &fakeAppKeys{enabled: true, households: tc.households, err: auth.ErrPersonKeyConflict, pendingErr: auth.ErrPersonKeyConflict}
				rec := do(t, NewHandler(appKeyDeps(keys)), route.method, Prefix+route.path, route.body, tc.headers)
				if tc.refused != nil {
					requireProblem(t, rec, *tc.refused)
					if keys.calls != 0 {
						t.Fatal("the service ran for a refused profile")
					}
					return
				}
				if keys.calls != 1 {
					t.Fatalf("status = %d, calls %d: %s", rec.Code, keys.calls, rec.Body.String())
				}
			})
		}
	}
}

// The waiting-device routes need the account's own login session: an API
// key, an administrator acting as the account and a remote-playback session
// are refused before the service runs. Sessions the service refuses (a TV's,
// another app's, one with no record of how it opened; an account no longer
// an administrator) keep their 403, and a key that can't be decided is 404.
func TestPendingKeyRoutesRefusals(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{method: http.MethodGet, path: "/auth/keys/pending"},
		{method: http.MethodPost, path: pendingKeyPath + "/approve", body: `{}`},
		{method: http.MethodPost, path: pendingKeyPath + "/deny", body: `{}`},
	}
	for _, route := range routes {
		for _, token := range []string{apiKeyToken, impersonatedToken, remotePlaybackToken} {
			t.Run(token+" "+route.method+" "+route.path, func(t *testing.T) {
				keys := &fakeAppKeys{enabled: true}
				requireProblem(t, do(t, NewHandler(appKeyDeps(keys)), route.method, Prefix+route.path, route.body, bearer(token)), TypePermissionDenied)
				if keys.calls != 0 {
					t.Fatal("service ran")
				}
			})
		}
		for _, tc := range []struct {
			err  error
			want ProblemType
		}{
			{err: auth.ErrSessionNotAllowed, want: TypePermissionDenied},
			{err: auth.ErrApproverNotAdmin, want: TypePermissionDenied},
			{err: auth.ErrAppKeyNotFound, want: TypeNotFound},
		} {
			if route.method == http.MethodGet && tc.want == TypeNotFound {
				continue
			}
			t.Run(tc.err.Error()+" "+route.method+" "+route.path, func(t *testing.T) {
				keys := &fakeAppKeys{enabled: true, pendingErr: tc.err}
				requireProblem(t, do(t, NewHandler(appKeyDeps(keys)), route.method, Prefix+route.path, route.body, mainProfile(adminToken)), tc.want)
			})
		}
	}
}

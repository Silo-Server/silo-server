package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// fakeProfileSets records the command it was asked to apply.
type fakeProfileSets struct {
	unsupported bool
	calls       int
	cmd         handlers.ProfileSetCommand
	held        handlers.ProfileSetHeld
}

func (f *fakeProfileSets) ProfileSetSupported() bool { return !f.unsupported }

func (f *fakeProfileSets) ApplyProfileSet(_ context.Context, cmd handlers.ProfileSetCommand) (handlers.ProfileSetResult, error) {
	f.calls++
	f.cmd = cmd
	return handlers.ProfileSetResult{Version: 1}, nil
}

func (f *fakeProfileSets) HeldProfileSet(context.Context, int) (handlers.ProfileSetHeld, error) {
	return f.held, nil
}

// profileSetKeys answers a verified signed set for account 1, or verifyErr.
type profileSetKeys struct {
	*fakeAppKeys
	verifyErr  error
	sessionErr error
}

func (k *profileSetKeys) VerifyProfileSet(context.Context, auth.KeyRequest) (*auth.ProfileSetAuthority, error) {
	if k.verifyErr != nil {
		return nil, k.verifyErr
	}
	return &auth.ProfileSetAuthority{UserID: 1, Set: []byte(`{"version":1,"items":[]}`)}, nil
}

func (k *profileSetKeys) ProfileSetSessionAllowed(context.Context, *auth.Claims) error {
	return k.sessionErr
}

const testSessionSet = `{"set":{"version":1,"items":[{"sync_id":"0f8fad5b-d9cb-469f-a165-70867728950e","name":"Sam","picture":"","pin":{"keep":true},"is_child":false,"max_content_rating":"","max_advisory_age":null,"require_advisory_age":false,"requests_blocked":false,"a_newer_member":1}]}}`

// TestPutProfileSetAuthority: who may send a profile set, in which form. It
// catches an admin account changed by a signature alone, a held-back
// session (another app, a TV, an administrator acting as the account)
// sending one, the switch closing the session form, and a stale profile
// header refusing a signed set.
func TestPutProfileSetAuthority(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		headers    map[string]string
		enabled    bool
		verifyErr  error
		sessionErr error
		sets       fakeProfileSets
		want       ProblemType
		signed     bool
	}{
		{name: "admin account by signature", body: testEnvelope, enabled: true, verifyErr: auth.ErrProfileSetAdminAccount, want: TypePermissionDenied},
		{name: "admin account by session", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + adminToken, profileHeader: "p-owner"}},
		{name: "switch off, signed", body: testEnvelope, want: TypeCapabilityDisabled},
		{name: "switch off, session", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-owner"}},
		{name: "held-back session", body: testSessionSet, headers: bearer(memberToken), sessionErr: auth.ErrSessionNotAllowed, want: TypePermissionDenied},
		{name: "session form without a session", body: testSessionSet, want: TypeAuthenticationRequired},
		{name: "main profile PIN not proven", body: testSessionSet, headers: map[string]string{"Authorization": "Bearer " + householdToken, profileHeader: "p-primary-locked"}, want: TypeProfileVerificationRequired},
		{name: "both forms", body: `{"kid":"zT321tSgcB7emUH9k0B0oA","payload":"eyJ9","sig":"c2ln","set":{"version":1,"items":[]}}`, enabled: true, want: TypeValidationFailed},
		{name: "neither form", body: `{}`, enabled: true, want: TypeValidationFailed},
		{name: "SQLite user store", body: testSessionSet, sets: fakeProfileSets{unsupported: true}, want: TypeCapabilityUnsupported},
		{name: "signed with a stale profile header", body: testEnvelope, enabled: true, signed: true,
			headers: map[string]string{"Authorization": "Bearer " + memberToken, profileHeader: "p-gone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := appKeyDeps(&fakeAppKeys{enabled: tc.enabled})
			deps.AppKeys = &profileSetKeys{fakeAppKeys: &fakeAppKeys{enabled: tc.enabled}, verifyErr: tc.verifyErr, sessionErr: tc.sessionErr}
			sets := tc.sets
			deps.ProfileSet = &sets
			rec := do(t, NewHandler(deps), http.MethodPut, Prefix+"/profiles/set", tc.body, tc.headers)
			if tc.want != (ProblemType{}) {
				requireProblem(t, rec, tc.want)
				if sets.calls != 0 {
					t.Fatal("the set was applied")
				}
				return
			}
			if rec.Code != http.StatusOK || sets.calls != 1 || sets.cmd.Signed != tc.signed {
				t.Fatalf("status %d, calls %d, cmd %+v: %s", rec.Code, sets.calls, sets.cmd, rec.Body.String())
			}
		})
	}
}

// TestGetProfileSetETag: an app polls the set with If-None-Match and gets
// 304 while it is unchanged.
func TestGetProfileSetETag(t *testing.T) {
	deps := appKeyDeps(&fakeAppKeys{})
	deps.ProfileSet = &fakeProfileSets{held: handlers.ProfileSetHeld{Version: 3, Items: []handlers.ProfileSetHeldItem{{SyncID: "0f8fad5b-d9cb-469f-a165-70867728950e", ID: "12", Name: "Sam"}}}}
	h := NewHandler(deps)
	first := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", bearer(memberToken))
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || tag == "" {
		t.Fatalf("first read = %d, ETag %q", first.Code, tag)
	}
	if again := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", map[string]string{"Authorization": "Bearer " + memberToken, "If-None-Match": tag}); again.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match = %d, want 304", again.Code)
	}
	if other := do(t, h, http.MethodGet, Prefix+"/profiles/set", "", map[string]string{"Authorization": "Bearer " + memberToken, "If-None-Match": `"ps1-other"`}); other.Code != http.StatusOK {
		t.Fatalf("another If-None-Match = %d, want 200", other.Code)
	}
}

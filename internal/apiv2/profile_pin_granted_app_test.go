package apiv2

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// untouchedStores fails the test if the PIN check opens the account's store,
// which it must do to read the profile's PIN hash.
type untouchedStores struct{ t *testing.T }

func (u untouchedStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	u.t.Error("the PIN check read the account's store")
	return nil, errors.New("untouched")
}

func (untouchedStores) Close() error { return nil }

// A session marked as another app's whose key id is missing cannot be
// counted by its key; counting it as a household device would bring back
// the household lockout. Nothing in the claims enforces the pairing, so the
// PIN check refuses it before any count is read or written or any hash is
// compared. The fake session lookup gives exactly such a session.
func TestGrantedAppWithoutKeyIDIsRefusedPINCheck(t *testing.T) {
	household := ratelimit.NewMemoryAttemptLimiter(ratelimit.AttemptPolicy{MaxAttempts: 1, Window: time.Hour})
	profiles := handlers.NewProfileHandler(untouchedStores{t})
	profiles.PINAttempts = household
	profiles.GrantedAppPINAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.GrantedAppPINPolicy)
	deps := parityDeps(false)
	deps.Profiles = profiles
	h := newTestHandler(t, deps)

	rec := do(t, h, http.MethodPost, Prefix+"/profiles/p-owner/verify-pin", `{"pin":"1234"}`, bearer(grantedAppToken))
	if p := requireProblem(t, rec, TypePermissionDenied); p.Detail != handlers.SessionNotAllowedDetail {
		t.Fatalf("detail = %q, want the gate's own for such a session", p.Detail)
	}
	if _, locked := household.Locked(t.Context(), ratelimit.ProfilePINKey(1, "p-owner")); locked {
		t.Fatal("household count is locked")
	}
	if _, ok := household.Reserve(t.Context(), ratelimit.ProfilePINKey(1, "p-owner")); !ok {
		t.Fatal("the refused check spent the household's only attempt")
	}
}

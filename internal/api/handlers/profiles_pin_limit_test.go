package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// newPINLimitHandler returns a profile handler over a store with two
// PIN-locked profiles (profile-1 PIN 1234, profile-2 PIN 5678) and the given
// attempt limiter.
func newPINLimitHandler(t *testing.T, store userstore.UserStore, limiter *ratelimit.AttemptLimiter) *ProfileHandler {
	t.Helper()
	h := NewProfileHandler(testUserStoreProvider{store: store})
	h.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, MaxProfiles: new(5)}}
	h.ProfileTokens = access.NewProfileTokenService("test-secret", time.Minute)
	h.PINAttempts = limiter
	return h
}

func newPINLimitStore(t *testing.T) userstore.UserStore {
	t.Helper()
	store := newProfileTestStore(t)
	ctx := context.Background()
	if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PINHash: storedPIN(t, "1234")}); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Kid"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if err := store.UpdateProfile(ctx, "profile-2", userstore.UpdateProfileInput{PINHash: storedPIN(t, "5678")}); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	return store
}

// storedPIN is a profile PIN as the profile handlers store it: its bcrypt
// hash, or "" for none.
func storedPIN(t *testing.T, pin string) *string {
	t.Helper()
	if pin == "" {
		return new("")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return new(string(hash))
}

// verifyPIN checks pin for profileID from the account's login session sess-1,
// as both listeners call the shared seam.
func verifyPIN(h *ProfileHandler, profileID, pin string) (ProfileVerification, error) {
	return h.VerifyPIN(profileServiceContext(""), ProfileVerifyPINCommand{
		UserID: 1, SessionID: "sess-1", ProfileID: profileID, PIN: pin,
	})
}

func requireVerifyPIN(t *testing.T, h *ProfileHandler, profileID, pin string, wantValid bool) {
	t.Helper()
	got, err := verifyPIN(h, profileID, pin)
	if err != nil {
		t.Fatalf("verify %s: %v", profileID, err)
	}
	if got.Valid != wantValid {
		t.Fatalf("verify %s: valid = %v, want %v", profileID, got.Valid, wantValid)
	}
	if wantValid && got.ProfileToken == "" {
		t.Fatalf("verify %s: a matching PIN issued no profile token", profileID)
	}
}

func requirePINLocked(t *testing.T, h *ProfileHandler, profileID, pin string) {
	t.Helper()
	_, err := verifyPIN(h, profileID, pin)
	if apiErr := requireAPIStatus(t, err, http.StatusTooManyRequests); apiErr.Code != "rate_limited" || apiErr.RetryAfter != 300 {
		t.Fatalf("locked verify = %+v, want rate_limited with Retry-After 300", apiErr)
	}
}

func TestVerifyPIN_LocksProfileAfterFiveWrongPINs(t *testing.T) {
	h := newPINLimitHandler(t, newPINLimitStore(t), ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for range 5 {
		requireVerifyPIN(t, h, "profile-1", "0000", false)
	}
	requirePINLocked(t, h, "profile-1", "0000")
	// The right PIN is refused too while the profile is locked.
	requirePINLocked(t, h, "profile-1", "1234")
	// Another profile on the same account keeps its own budget.
	requireVerifyPIN(t, h, "profile-2", "5678", true)
}

func TestVerifyPIN_CorrectPINResetsCount(t *testing.T) {
	h := newPINLimitHandler(t, newPINLimitStore(t), ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for range 2 {
		for range 4 {
			requireVerifyPIN(t, h, "profile-1", "0000", false)
		}
		requireVerifyPIN(t, h, "profile-1", "1234", true)
	}
}

// A profile that does not exist or has no PIN is refused before an attempt
// is counted, so it never reaches the lockout and adds no limiter entry.
func TestVerifyPIN_UnknownOrPINlessProfileTakesNoAttempt(t *testing.T) {
	store := newPINLimitStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-3", Name: "Guest"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	h := newPINLimitHandler(t, store, ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for _, id := range []string{"missing", "profile-3"} {
		for range ratelimit.ProfilePINPolicy.MaxAttempts + 1 {
			_, err := verifyPIN(h, id, "0000")
			requireAPIStatus(t, err, http.StatusNotFound)
		}
	}
}

func TestPINLockedErrorRoundsRetryAfterUp(t *testing.T) {
	err := PINLockedError(90*time.Second + time.Millisecond)
	if err.Status != http.StatusTooManyRequests || err.Code != "rate_limited" || err.RetryAfter != 91 {
		t.Fatalf("PINLockedError = %+v", err)
	}
}

// A manager who resets a locked profile's PIN can use the new one at once.
func TestUpdateProfile_PINChangeClearsLockout(t *testing.T) {
	h := newPINLimitHandler(t, newPINLimitStore(t), ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for range 5 {
		requireVerifyPIN(t, h, "profile-2", "0000", false)
	}
	requirePINLocked(t, h, "profile-2", "5678")

	// The household parent (profile-1, already verified) resets the kid's PIN.
	if _, err := h.UpdateProfile(profileServiceContext("profile-1"), ProfileUpdateCommand{
		UserID: 1, ProfileID: "profile-2", ActiveProfileID: "profile-1",
		Request:       ProfileUpdateRequest{PIN: new("2468")},
		VerifyProfile: noProfileVerification,
	}); err != nil {
		t.Fatalf("reset pin: %v", err)
	}

	requireVerifyPIN(t, h, "profile-2", "2468", true)
}

// A server administrator resets a forgotten primary PIN through the admin
// API, which has no household proof to give. Only AccountAdmin lifts the
// household-manager check: the same command without it is refused.
func TestUpdateProfile_AccountAdminResetsPrimaryPIN(t *testing.T) {
	store := newPINLimitStore(t)
	h := newPINLimitHandler(t, store, ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))
	ctx := context.Background()

	for range 5 {
		requireVerifyPIN(t, h, "profile-1", "0000", false)
	}
	requirePINLocked(t, h, "profile-1", "1234")

	cmd := ProfileUpdateCommand{UserID: 1, ProfileID: "profile-1", Request: ProfileUpdateRequest{PIN: new("2468")}}
	var refused *APIError
	if _, err := h.UpdateProfile(ctx, cmd); !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("update without AccountAdmin: err = %v; want 403", err)
	}
	cmd.AccountAdmin = true
	if _, err := h.UpdateProfile(ctx, cmd); err != nil {
		t.Fatalf("admin PIN reset: %v", err)
	}
	requireVerifyPIN(t, h, "profile-1", "2468", true)

	cmd.Request = ProfileUpdateRequest{PIN: new("")}
	view, err := h.UpdateProfile(ctx, cmd)
	if err != nil || view.HasPIN {
		t.Fatalf("admin PIN removal: has_pin = %v, err = %v", view.HasPIN, err)
	}
}

type usersByID map[int]*models.User

func (u usersByID) GetByID(_ context.Context, id int) (*models.User, error) {
	if user, ok := u[id]; ok {
		return user, nil
	}
	return nil, auth.ErrNotFound
}

// An administrator's profile write follows the Owner rules: a primary PIN
// guards its account's admin access, so only the Owner may change the
// Owner's or another admin's profiles.
func TestAccountAdminProfileWritesFollowOwnerRules(t *testing.T) {
	const owner, admin, otherAdmin, member = 1, 2, 3, 4
	users := usersByID{
		owner:      {ID: owner, Role: models.RoleAdmin, IsOwner: true},
		admin:      {ID: admin, Role: models.RoleAdmin},
		otherAdmin: {ID: otherAdmin, Role: models.RoleAdmin},
		member:     {ID: member, Role: models.RoleUser},
	}
	for _, tc := range []struct {
		name          string
		actor, target int
		want          int
	}{
		{"admin on a member", admin, member, http.StatusOK},
		{"admin on its own account", admin, admin, http.StatusOK},
		{"admin on another admin", admin, otherAdmin, http.StatusForbidden},
		{"admin on the Owner", admin, owner, http.StatusForbidden},
		{"Owner on an admin", owner, admin, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPINLimitHandler(t, newPINLimitStore(t), ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))
			h.UserRepo = users
			ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: tc.actor, Role: models.RoleAdmin})
			_, updateErr := h.UpdateProfile(ctx, ProfileUpdateCommand{UserID: tc.target, ProfileID: "profile-1", Request: ProfileUpdateRequest{PIN: new("")}, AccountAdmin: true})
			deleteErr := h.DeleteProfile(ctx, ProfileDeleteCommand{UserID: tc.target, ProfileID: "profile-2", AccountAdmin: true})
			for op, err := range map[string]error{"update": updateErr, "delete": deleteErr} {
				var apiErr *APIError
				switch {
				case tc.want == http.StatusOK && err != nil:
					t.Fatalf("%s: %v; want success", op, err)
				case tc.want != http.StatusOK && (!errors.As(err, &apiErr) || apiErr.Status != tc.want):
					t.Fatalf("%s: err = %v; want %d", op, err, tc.want)
				}
			}
		})
	}
}

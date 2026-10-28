package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
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

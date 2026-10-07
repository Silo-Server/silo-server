package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	h.UserRepo = testProfileUserRepo{user: &models.User{ID: 1, MaxProfiles: 5}}
	h.ProfileTokens = access.NewProfileTokenService("test-secret", time.Minute)
	h.PINAttempts = limiter
	return h
}

func newPINLimitStore(t *testing.T) userstore.UserStore {
	t.Helper()
	store := newProfileTestStore(t)
	ctx := context.Background()
	if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PIN: ptr("1234")}); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Kid"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if err := store.UpdateProfile(ctx, "profile-2", userstore.UpdateProfileInput{PIN: ptr("5678")}); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	return store
}

func postVerifyPIN(t *testing.T, h *ProfileHandler, profileID, pin string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthorizedProfileRequestWithSession(http.MethodPost, "/profiles/"+profileID+"/verify-pin",
		`{"pin":"`+pin+`"}`, "user", "", "sess-1")
	req = withProfileRouteParam(req, "id", profileID)
	rr := httptest.NewRecorder()
	h.HandleVerifyPIN(rr, req)
	return rr
}

func requireVerifyPIN(t *testing.T, rr *httptest.ResponseRecorder, wantValid bool) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", rr.Code, rr.Body.String())
	}
	var resp verifyPINResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Valid != wantValid {
		t.Fatalf("valid = %v, want %v", resp.Valid, wantValid)
	}
}

func requirePINLocked(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s; want 429", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "300" {
		t.Fatalf("Retry-After = %q, want 300", got)
	}
	var body errorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "rate_limited" || body.Message == "" {
		t.Fatalf("body = %+v, want rate_limited with a message", body)
	}
}

func TestHandleVerifyPIN_LocksProfileAfterFiveWrongPINs(t *testing.T) {
	h := newPINLimitHandler(t, newPINLimitStore(t), ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for range 5 {
		requireVerifyPIN(t, postVerifyPIN(t, h, "profile-1", "0000"), false)
	}
	requirePINLocked(t, postVerifyPIN(t, h, "profile-1", "0000"))
	// The right PIN is refused too while the profile is locked.
	requirePINLocked(t, postVerifyPIN(t, h, "profile-1", "1234"))
	// Another profile on the same account keeps its own budget.
	requireVerifyPIN(t, postVerifyPIN(t, h, "profile-2", "5678"), true)
}

func TestHandleVerifyPIN_CorrectPINResetsCount(t *testing.T) {
	h := newPINLimitHandler(t, newPINLimitStore(t), ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for range 4 {
		requireVerifyPIN(t, postVerifyPIN(t, h, "profile-1", "0000"), false)
	}
	requireVerifyPIN(t, postVerifyPIN(t, h, "profile-1", "1234"), true)
	for range 4 {
		requireVerifyPIN(t, postVerifyPIN(t, h, "profile-1", "0000"), false)
	}
	requireVerifyPIN(t, postVerifyPIN(t, h, "profile-1", "1234"), true)
}

// Two API nodes sharing one backend share the count, as Redis does in a
// cluster.
func TestHandleVerifyPIN_SharedLimiterAcrossHandlers(t *testing.T) {
	store := newPINLimitStore(t)
	shared := ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy)
	nodeA := newPINLimitHandler(t, store, shared)
	nodeB := newPINLimitHandler(t, store, shared)

	for range 3 {
		requireVerifyPIN(t, postVerifyPIN(t, nodeA, "profile-1", "0000"), false)
	}
	for range 2 {
		requireVerifyPIN(t, postVerifyPIN(t, nodeB, "profile-1", "0000"), false)
	}
	requirePINLocked(t, postVerifyPIN(t, nodeA, "profile-1", "1234"))
	requirePINLocked(t, postVerifyPIN(t, nodeB, "profile-1", "1234"))
}

// A profile that does not exist or has no PIN is refused before an attempt
// is counted, so it never reaches the lockout and adds no limiter entry.
func TestHandleVerifyPIN_UnknownOrPINlessProfileTakesNoAttempt(t *testing.T) {
	store := newPINLimitStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-3", Name: "Guest"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	h := newPINLimitHandler(t, store, ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for _, id := range []string{"missing", "profile-3"} {
		for range ratelimit.ProfilePINPolicy.MaxAttempts + 1 {
			if rr := postVerifyPIN(t, h, id, "0000"); rr.Code != http.StatusNotFound {
				t.Fatalf("%s: status = %d, body = %s; want 404", id, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestPINLockedErrorRoundsRetryAfterUp(t *testing.T) {
	err := PINLockedError(90*time.Second + time.Millisecond)
	if err.Status != http.StatusTooManyRequests || err.Code != "rate_limited" || err.RetryAfter != 91 {
		t.Fatalf("PINLockedError = %+v", err)
	}
}

func TestUpdateProfile_PINChangeClearsLockout(t *testing.T) {
	store := newPINLimitStore(t)
	h := newPINLimitHandler(t, store, ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy))

	for range 5 {
		requireVerifyPIN(t, postVerifyPIN(t, h, "profile-2", "0000"), false)
	}
	requirePINLocked(t, postVerifyPIN(t, h, "profile-2", "5678"))

	req := newAuthorizedProfileRequestWithRole(http.MethodPut, "/profiles/profile-2", `{"pin":"2468"}`, "admin", "")
	rr := httptest.NewRecorder()
	h.HandleUpdateProfile(rr, withProfileRouteParam(req, "id", "profile-2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", rr.Code, rr.Body.String())
	}

	requireVerifyPIN(t, postVerifyPIN(t, h, "profile-2", "2468"), true)
}

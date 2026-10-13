package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

const absPINTestPassword = "staple battery horse"

type absPINHousehold struct {
	validator *SiloCredValidator
	limiter   *ratelimit.AttemptLimiter
	store     userstore.UserStore
	user      *models.User
	parentID  string // primary, PIN 4321
	kidID     string // no PIN
}

// newABSPINHousehold creates an account whose primary profile "Parent" has a
// PIN and whose "Kid" profile has none.
func newABSPINHousehold(t *testing.T) absPINHousehold {
	t.Helper()
	return newABSPINHouseholdWithPassword(t, absPINTestPassword)
}

func newABSPINHouseholdWithPassword(t *testing.T, password string) absPINHousehold {
	t.Helper()
	pool := newClaimTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("abs-cred-validator-test-secret-0123456789", time.Minute, time.Hour),
		sessions, users, nil, nil, nil)

	user, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("abs-household-%d", suffix), Email: fmt.Sprintf("abs-household-%d@example.test", suffix),
		Password: password, Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	stores := pgstore.NewPostgresProvider(pool)
	store, err := stores.ForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := absPINHousehold{
		limiter:  ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy),
		store:    store,
		user:     user,
		parentID: uuid.New().String(),
		kidID:    uuid.New().String(),
	}
	// The first profile becomes the primary.
	if err := store.CreateProfile(ctx, userstore.Profile{ID: h.parentID, Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	pin := "4321"
	if err := store.UpdateProfile(ctx, h.parentID, userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: h.kidID, Name: "Kid", IsChild: true, MaxContentRating: "PG"}); err != nil {
		t.Fatal(err)
	}
	h.validator = &SiloCredValidator{Auth: svc, Pool: pool, Stores: stores, PINAttempts: h.limiter}
	return h
}

func (h absPINHousehold) profile(t *testing.T, id string) *userstore.Profile {
	t.Helper()
	p, err := h.store.GetProfile(context.Background(), id)
	if err != nil || p == nil {
		t.Fatalf("load profile %s: %v", id, err)
	}
	return p
}

// TestABSSignInChecksProfilePINDB: the account password alone opens only a
// profile without a PIN. A PIN-protected profile has to be named and its PIN
// given as password#pin, and a bare username never falls back to a
// PIN-protected primary.
func TestABSSignInChecksProfilePINDB(t *testing.T) {
	h := newABSPINHousehold(t)
	ctx := context.Background()
	name := h.user.Username

	refused := []struct {
		username, password string
		want               error
	}{
		{name, absPINTestPassword, abs.ErrProfilePINRequired},
		{name, absPINTestPassword + "#4321", abs.ErrProfilePINRequired},
		{name + "#Parent", absPINTestPassword, abs.ErrProfilePINRequired},
		{name + "#parent", absPINTestPassword + "#0000", abs.ErrProfilePINIncorrect},
		{name + "#Parent", "wrong password#4321", auth.ErrInvalidCredentials},
		{name + "#Nobody", absPINTestPassword, auth.ErrInvalidCredentials},
	}
	for _, tc := range refused {
		got, err := h.validator.Validate(ctx, tc.username, tc.password)
		if !errors.Is(err, tc.want) {
			t.Errorf("Validate(%q, %q) = %+v, %v; want %v", tc.username, tc.password, got, err, tc.want)
		}
	}

	parent := h.profile(t, h.parentID)
	got, err := h.validator.Validate(ctx, name+"#Parent", absPINTestPassword+"#4321")
	if err != nil {
		t.Fatalf("Parent with its PIN: %v", err)
	}
	want := abs.Principal{UserID: fmt.Sprint(h.user.ID), ProfileID: h.parentID, DisplayName: "Parent", PINRevision: parent.PINRevision}
	if got != want {
		t.Fatalf("Parent with its PIN = %+v, want %+v", got, want)
	}

	got, err = h.validator.Validate(ctx, name+"#Kid", absPINTestPassword)
	if err != nil || got.ProfileID != h.kidID {
		t.Fatalf("Kid without a PIN = %+v, %v; want the Kid profile", got, err)
	}
}

// TestABSSignInPINAttemptsShareProfileLimitDB: PIN guesses count against the
// profile's budget shared with the native verify-pin endpoint, but only once
// the account password matched. After five wrong PINs even the right one is
// refused.
func TestABSSignInPINAttemptsShareProfileLimitDB(t *testing.T) {
	h := newABSPINHousehold(t)
	ctx := context.Background()
	login := func(password string) error {
		_, err := h.validator.Validate(ctx, h.user.Username+"#Parent", password)
		return err
	}

	for i := 0; i < 6; i++ {
		if err := login("wrong password#0000"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("wrong password %d: %v", i+1, err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := login(absPINTestPassword + "#0000"); !errors.Is(err, abs.ErrProfilePINIncorrect) {
			t.Fatalf("wrong PIN %d: %v", i+1, err)
		}
	}
	if err := login(absPINTestPassword + "#4321"); err != nil {
		t.Fatalf("right PIN after four wrong ones (wrong passwords not counted): %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := login(absPINTestPassword + "#0000"); !errors.Is(err, abs.ErrProfilePINIncorrect) {
			t.Fatalf("wrong PIN %d after reset: %v", i+1, err)
		}
	}
	if err := login(absPINTestPassword + "#4321"); !errors.Is(err, abs.ErrProfilePINLocked) {
		t.Fatalf("right PIN while locked: %v, want ErrProfilePINLocked", err)
	}
	if _, ok := h.limiter.Reserve(ctx, ratelimit.ProfilePINKey(h.user.ID, h.parentID)); ok {
		t.Fatal("native PIN check allowed while ABS sign-ins locked the profile")
	}
}

// TestABSTokenProfileVerifierDB: a token keeps working only while its
// profile's PIN is the one it was signed in with.
func TestABSTokenProfileVerifierDB(t *testing.T) {
	h := newABSPINHousehold(t)
	ctx := context.Background()
	uid := fmt.Sprint(h.user.ID)
	verify := func(profileID string, rev *int64) error {
		return h.validator.VerifyTokenProfile(ctx, uid, profileID, rev)
	}
	accepted := func(step string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v, want accepted", step, err)
		}
	}
	refused := func(step string, err error) {
		t.Helper()
		if !errors.Is(err, abs.ErrTokenProfileUnverified) {
			t.Fatalf("%s: %v, want ErrTokenProfileUnverified", step, err)
		}
	}

	parentRev := h.profile(t, h.parentID).PINRevision
	accepted("Parent at its PIN revision", verify(h.parentID, &parentRev))
	refused("Parent token without a revision", verify(h.parentID, nil))

	newPIN := "8765"
	if err := h.store.UpdateProfile(ctx, h.parentID, userstore.UpdateProfileInput{PIN: &newPIN}); err != nil {
		t.Fatal(err)
	}
	refused("Parent after its PIN changed", verify(h.parentID, &parentRev))

	kidRev := h.profile(t, h.kidID).PINRevision
	accepted("Kid without a PIN, no revision", verify(h.kidID, nil))
	accepted("Kid without a PIN", verify(h.kidID, &kidRev))
	kidPIN := "1111"
	if err := h.store.UpdateProfile(ctx, h.kidID, userstore.UpdateProfileInput{PIN: &kidPIN}); err != nil {
		t.Fatal(err)
	}
	refused("Kid after a PIN was set", verify(h.kidID, &kidRev))

	if err := h.store.DeleteProfile(ctx, h.kidID); err != nil {
		t.Fatal(err)
	}
	refused("deleted profile", verify(h.kidID, &kidRev))
	refused("no profile on an account that has profiles", verify("", nil))
	refused("another account's user id", h.validator.VerifyTokenProfile(ctx, "not-a-number", h.parentID, &parentRev))
}

// TestABSSignInAccountNamedProfileDB: a bare username names the profile
// called like the account, as on the Jellyfin-compatible listener, so its PIN
// goes in the password like any named profile's.
func TestABSSignInAccountNamedProfileDB(t *testing.T) {
	h := newABSPINHousehold(t)
	ctx := context.Background()
	ownID := uuid.New().String()
	if err := h.store.CreateProfile(ctx, userstore.Profile{ID: ownID, Name: h.user.Username}); err != nil {
		t.Fatal(err)
	}
	pin := "2468"
	if err := h.store.UpdateProfile(ctx, ownID, userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.validator.Validate(ctx, h.user.Username, absPINTestPassword); !errors.Is(err, abs.ErrProfilePINRequired) {
		t.Fatalf("account-named profile without its PIN: %v, want ErrProfilePINRequired", err)
	}
	if _, err := h.validator.Validate(ctx, h.user.Username, absPINTestPassword+"#4321"); !errors.Is(err, abs.ErrProfilePINIncorrect) {
		t.Fatalf("account-named profile with the primary's PIN: %v, want ErrProfilePINIncorrect", err)
	}
	got, err := h.validator.Validate(ctx, h.user.Username, absPINTestPassword+"#2468")
	if err != nil || got.ProfileID != ownID {
		t.Fatalf("account-named profile with its PIN = %+v, %v; want profile %s", got, err, ownID)
	}
}

// TestABSSignInMaxLengthPasswordKeepsPINDB: bcrypt compares only the first 72
// bytes, so a 72-byte account password followed by "#pin" still carries the
// PIN rather than matching as the whole password.
func TestABSSignInMaxLengthPasswordKeepsPINDB(t *testing.T) {
	password := strings.Repeat("p", auth.MaximumPasswordBytes)
	h := newABSPINHouseholdWithPassword(t, password)
	ctx := context.Background()

	got, err := h.validator.Validate(ctx, h.user.Username+"#Parent", password+"#4321")
	if err != nil || got.ProfileID != h.parentID {
		t.Fatalf("72-byte password with the PIN = %+v, %v; want the Parent profile", got, err)
	}
	if _, err := h.validator.Validate(ctx, h.user.Username+"#Parent", password+"#0000"); !errors.Is(err, abs.ErrProfilePINIncorrect) {
		t.Fatalf("72-byte password with a wrong PIN: %v, want ErrProfilePINIncorrect", err)
	}
	if _, err := h.validator.Validate(ctx, h.user.Username+"#Kid", password); err != nil {
		t.Fatalf("72-byte password alone for a profile without a PIN: %v", err)
	}
}

// TestABSSignInByEmailSelectsAccountNamedProfileDB: signing in with the
// account's email picks the profile named like the account, as the username
// does.
func TestABSSignInByEmailSelectsAccountNamedProfileDB(t *testing.T) {
	h := newABSPINHousehold(t)
	ctx := context.Background()
	ownID := uuid.New().String()
	if err := h.store.CreateProfile(ctx, userstore.Profile{ID: ownID, Name: h.user.Username}); err != nil {
		t.Fatal(err)
	}

	got, err := h.validator.Validate(ctx, h.user.Email, absPINTestPassword)
	if err != nil || got.ProfileID != ownID {
		t.Fatalf("email sign-in = %+v, %v; want the account-named profile %s", got, err, ownID)
	}
}

package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestLoginPINAttemptsShareProfileLimitDB: password#PIN guesses at a Jellyfin
// sign-in count against the profile's PIN budget shared with the native
// verify-pin endpoint. A correct PIN clears the count; after five wrong PINs
// the profile is locked and even the right PIN fails, the same way a wrong
// PIN does (401, no new wire shape).
func TestLoginPINAttemptsShareProfileLimitDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("jellycompat-login-test-secret-0123456789", time.Minute, time.Hour),
		sessions, users, nil, nil, nil)

	user, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfpin-%d", suffix), Email: fmt.Sprintf("jfpin-%d@example.test", suffix),
		Password: "correct horse battery", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	stores := pgstore.NewPostgresProvider(pool)
	store, err := stores.ForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	pinHash, err := bcrypt.GenerateFromPassword([]byte("4321"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	profileID := uuid.New().String()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Kid", PINHash: string(pinHash), IsPrimary: true}); err != nil {
		t.Fatal(err)
	}

	limiter := ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy)
	resolver := NewLoginResolver(svc, stores, NewSessionStore(time.Hour, nil), nil, nil).WithPINAttempts(limiter)
	login := func(pin string) error {
		_, err := resolver.Resolve(ctx, user.Username+"#Kid", "correct horse battery#"+pin, "test", "")
		return err
	}
	wrong := func(n int) {
		t.Helper()
		for i := range n {
			if err := login("0000"); !errors.Is(err, ErrInvalidPIN) {
				t.Fatalf("wrong PIN %d: error = %v, want ErrInvalidPIN", i+1, err)
			}
		}
	}

	wrong(4)
	if err := login("4321"); err != nil {
		t.Fatalf("correct PIN after four wrong: %v", err)
	}
	wrong(5)
	err = login("4321")
	if !errors.Is(err, ErrInvalidPIN) || !strings.Contains(err.Error(), "too many incorrect PINs") {
		t.Fatalf("correct PIN while locked: error = %v, want the lockout", err)
	}
	if status, code, _ := mapLoginError(err); status != http.StatusUnauthorized || code != loginErrorCode {
		t.Fatalf("lockout maps to %d %s, want the wrong-PIN 401", status, code)
	}
	// The native verify-pin endpoint sees the same lock.
	if _, ok := limiter.Reserve(ctx, ratelimit.ProfilePINKey(user.ID, profileID)); ok {
		t.Fatal("native PIN check allowed while the Jellyfin sign-in locked the profile")
	}
}

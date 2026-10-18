package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// TestPseudoUserOwnerAcrossReplicasPostgres: a replica that never saw a
// sign-in still maps the pseudo-user id to its account and profile through
// the stored session, so any replica can serve the user's avatar. Expired
// sessions and unknown ids map to nothing.
func TestPseudoUserOwnerAcrossReplicasPostgres(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	user, err := auth.NewUserRepository(pool).Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfowner-%d", suffix),
		Email: fmt.Sprintf("jfowner-%d@example.test", suffix), Password: "correct horse battery", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	cipher, err := secret.New([]byte("jellycompat-owner-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := auth.NewJWTService("jellycompat-owner-test-secret-0123456789", time.Hour, 24*time.Hour).GenerateRefreshToken(user.ID, models.RoleUser, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	signIn := NewPersistentSessionStore(time.Hour, func() time.Time { return now }, NewSessionRepository(pool, cipher))
	live, expired := PseudoUserID(user.ID, "kids"), PseudoUserID(user.ID, "gone")
	for token, s := range map[string]Session{
		fmt.Sprintf("jfowner-live-%d", suffix):    {ProfileID: "kids", PseudoUserID: live},
		fmt.Sprintf("jfowner-expired-%d", suffix): {ProfileID: "gone", PseudoUserID: expired, ExpiresAt: now.Add(-time.Minute)},
	} {
		s.Token, s.StreamAppUserID = token, user.ID
		s.StreamAppAccessToken, s.StreamAppRefreshToken, s.StreamAppTokenExpiry = "access", refresh, now.Add(time.Hour)
		if err := signIn.Put(s); err != nil {
			t.Fatal(err)
		}
	}

	other := NewPersistentSessionStore(time.Hour, func() time.Time { return now }, NewSessionRepository(pool, cipher))
	owner, err := other.PseudoUserOwner(ctx, live)
	if err != nil || owner != (pseudoUserOwner{UserID: user.ID, ProfileID: "kids"}) {
		t.Fatalf("PseudoUserOwner(live) = %+v, %v; want user %d profile kids", owner, err, user.ID)
	}
	for name, id := range map[string]uuid.UUID{"expired": expired, "unknown": PseudoUserID(user.ID, "nobody")} {
		if owner, err := other.PseudoUserOwner(ctx, id); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("PseudoUserOwner(%s) = %+v, %v; want ErrSessionNotFound", name, owner, err)
		}
	}
}

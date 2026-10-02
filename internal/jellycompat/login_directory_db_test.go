package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// countingDirectory stands in for an LDAP plugin: it records every password
// it was asked to check and accepts none.
type countingDirectory struct{ passwords []string }

func (d *countingDirectory) Authenticate(_ context.Context, creds auth.Credentials) (*models.User, error) {
	d.passwords = append(d.passwords, creds.Password)
	return nil, auth.ErrInvalidCredentials
}

func (d *countingDirectory) ValidateSession(context.Context, string) (bool, error) { return true, nil }

type directorySource []auth.RegisteredProvider

func (s directorySource) Providers() []auth.RegisteredProvider { return s }

// TestLoginPINConventionStaysLocalDB: the password#PIN fallback makes a
// second attempt only for a local account. A name that signs in with the
// directory gets one bind with the password as typed, so a failed Jellyfin
// sign-in counts once toward the directory's lockout and the PIN is never
// sent there. A local account still signs in to a PIN-protected profile with
// password#PIN, and a wrong base password is refused.
func TestLoginPINConventionStaysLocalDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("jellycompat-login-test-secret-0123456789", time.Minute, time.Hour),
		sessions, users, nil, nil, nil)
	directory := &countingDirectory{}
	svc.SetPluginProviderSource(directorySource{{
		Info:     auth.LoginProviderInfo{ID: "plugin:1:ldap", DisplayName: "Directory", Mode: auth.ProviderModeCredentials, InstallationID: 1},
		Provider: directory,
	}})
	off := false
	linked, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfdir-%d", suffix), Email: fmt.Sprintf("jfdir-%d@example.test", suffix),
		Password: "correct horse battery", Role: models.RoleUser, LocalPasswordLoginEnabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, linked.ID) })

	stores := pgstore.NewPostgresProvider(pool)
	resolver := NewLoginResolver(svc, stores, NewSessionStore(time.Hour, nil), nil, nil)
	for _, name := range []string{linked.Username, fmt.Sprintf("jfnew-%d", suffix)} {
		directory.passwords = nil
		if _, err := resolver.Resolve(ctx, name, "directory-secret#1234", "test", ""); err == nil {
			t.Fatalf("%s: a refused password signed in", name)
		}
		if len(directory.passwords) != 1 || directory.passwords[0] != "directory-secret#1234" {
			t.Fatalf("%s: directory asked %q, want one attempt with the password as typed", name, directory.passwords)
		}
	}

	local, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jflocal-%d", suffix), Email: fmt.Sprintf("jflocal-%d@example.test", suffix),
		Password: "correct horse battery", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, local.ID) })
	store, err := stores.ForUser(ctx, local.ID)
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
	directory.passwords = nil
	session, err := resolver.Resolve(ctx, local.Username+"#Kid", "correct horse battery#4321", "test", "")
	if err != nil {
		t.Fatalf("password#PIN sign-in: %v", err)
	}
	if session.ProfileID != profileID || session.StreamAppUserID != local.ID {
		t.Fatalf("session = profile %q account %d, want profile %q account %d", session.ProfileID, session.StreamAppUserID, profileID, local.ID)
	}
	if _, err := resolver.Resolve(ctx, local.Username+"#Kid", "wrong horse battery#4321", "test", ""); err == nil {
		t.Fatal("a wrong base password signed in to the PIN-protected profile")
	}
	if len(directory.passwords) != 0 {
		t.Fatalf("a local account's password reached the directory: %q", directory.passwords)
	}

	// Selecting the directory permits only the original password attempt,
	// even when the caller supplied the account's old local password as fallback.
	_, _, usedFallback, err := svc.CompatLoginWithLocalFallback(ctx, linked.Username, "directory-secret#4321", "correct horse battery", "test", "")
	if !errors.Is(err, auth.ErrInvalidCredentials) || usedFallback {
		t.Fatalf("directory fallback: used = %v, error = %v", usedFallback, err)
	}
	if len(directory.passwords) != 1 || directory.passwords[0] != "directory-secret#4321" {
		t.Fatalf("directory received passwords: %q", directory.passwords)
	}
}

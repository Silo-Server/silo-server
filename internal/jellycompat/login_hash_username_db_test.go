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

// TestLoginAccountNameWithHashDB: an account whose username contains '#',
// such as one created from an old Discord name like name#1234, signs in by its
// whole name, and name#1234#Profile selects one of its profiles. When an
// account has the name before the last '#', the name is read as
// username#profile as before, so registering a name with '#' in it cannot
// take over another account's profile sign-in. With a directory (LDAP), a
// name before the last '#' that has no account yet goes to the directory, so
// a local account named with the whole name cannot block its first sign-in.
func TestLoginAccountNameWithHashDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("jellycompat-login-test-secret-0123456789", time.Minute, time.Hour),
		sessions, users, nil, nil, nil)
	stores := pgstore.NewPostgresProvider(pool)
	resolver := NewLoginResolver(svc, stores, NewSessionStore(time.Hour, nil), nil, nil)
	const password = "correct horse battery"

	profileIDs := map[string]string{}
	account := func(username string, profiles ...string) *models.User {
		t.Helper()
		user, err := users.Create(ctx, models.CreateUserInput{Username: username, Email: fmt.Sprintf("jfhash-%s@example.test", uuid.NewString()),
			Password: password, Role: models.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
		store, err := stores.ForUser(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		for i, name := range profiles {
			id := uuid.NewString()
			if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: name, IsPrimary: i == 0}); err != nil {
				t.Fatal(err)
			}
			profileIDs[username+"/"+name] = id
		}
		return user
	}
	signIn := func(name string, want *models.User, wantProfile string) {
		t.Helper()
		session, err := resolver.Resolve(ctx, name, password, "test", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if session.StreamAppUserID != want.ID || session.ProfileID != profileIDs[want.Username+"/"+wantProfile] {
			t.Fatalf("%s: signed in to account %d profile %q, want account %d profile %q", name, session.StreamAppUserID, session.ProfileName, want.ID, wantProfile)
		}
	}

	single := account(fmt.Sprintf("jfhash-%d#1234", suffix), "Main")
	signIn(single.Username, single, "Main")

	several := account(fmt.Sprintf("jfhashes-%d#1234", suffix), "Main", "Kids")
	signIn(several.Username+"#Kids", several, "Kids")
	if _, err := resolver.Resolve(ctx, several.Username, password, "test", ""); !errors.Is(err, ErrProfileRequired) {
		t.Fatalf("%s with two profiles and no suffix: error = %v, want ErrProfileRequired", several.Username, err)
	}

	plain := account(fmt.Sprintf("jfplain-%d", suffix), "Main", "Kid")
	signIn(plain.Username+"#Kid", plain, "Kid")

	pinned := account(fmt.Sprintf("jfhashpin-%d#1234", suffix), "Main")
	store, err := stores.ForUser(ctx, pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	pinHash, err := bcrypt.GenerateFromPassword([]byte("4321"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	kidID := uuid.NewString()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: kidID, Name: "Kid", PINHash: string(pinHash)}); err != nil {
		t.Fatal(err)
	}
	session, err := resolver.Resolve(ctx, pinned.Username+"#Kid", password+"#4321", "test", "")
	if err != nil || session.ProfileID != kidID || session.StreamAppUserID != pinned.ID {
		t.Fatalf("%s#Kid with password#PIN: session = %+v, error = %v", pinned.Username, session, err)
	}

	owner := account(fmt.Sprintf("jfowner-%d", suffix), "Main", "Kids")
	squatter := account(owner.Username+"#Kids", "Main")
	signIn(squatter.Username, owner, "Kids")

	for _, name := range []string{fmt.Sprintf("#jfedge-%d", suffix), fmt.Sprintf("jfedge-%d#", suffix)} {
		account(name, "Main")
		if _, err := resolver.Resolve(ctx, name, password, "test", ""); !errors.Is(err, ErrProfileRequired) {
			t.Fatalf("%q: error = %v, want ErrProfileRequired", name, err)
		}
	}

	directory := &countingDirectory{}
	directorySvc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("jellycompat-login-test-secret-0123456789", time.Minute, time.Hour),
		sessions, users, nil, nil, nil)
	directorySvc.SetPluginProviderSource(directorySource{{
		Info:     auth.LoginProviderInfo{ID: "plugin:1:ldap", DisplayName: "Directory", Mode: auth.ProviderModeCredentials, InstallationID: 1},
		Provider: directory,
	}})
	directoryResolver := NewLoginResolver(directorySvc, stores, NewSessionStore(time.Hour, nil), nil, nil)
	unprovisioned := fmt.Sprintf("jfdirhash-%d", suffix)
	account(unprovisioned+"#Kids", "Main")
	if _, err := directoryResolver.Resolve(ctx, unprovisioned+"#Kids", "directory-secret", "test", ""); err == nil {
		t.Fatalf("%s#Kids: a password the directory refused signed in", unprovisioned)
	}
	if len(directory.passwords) != 1 || directory.passwords[0] != "directory-secret" {
		t.Fatalf("%s#Kids: directory asked %q, want one attempt for the unprovisioned name", unprovisioned, directory.passwords)
	}
}

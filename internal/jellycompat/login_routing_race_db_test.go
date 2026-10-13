package jellycompat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type linkingLocalProvider struct {
	*auth.LocalProvider
	users  *auth.UserRepository
	userID int
	calls  int
}

func (p *linkingLocalProvider) Authenticate(ctx context.Context, creds auth.Credentials) (*models.User, error) {
	p.calls++
	if p.calls == 1 {
		if err := p.users.Update(ctx, p.userID, models.UpdateUserInput{LocalPasswordLoginEnabled: new(false)}); err != nil {
			return nil, err
		}
	}
	return p.LocalProvider.Authenticate(ctx, creds)
}

func TestLoginPINFallbackKeepsLocalRouteAfterLinkDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := t.Context()
	users, sessions := auth.NewUserRepository(pool), auth.NewSessionRepository(pool)
	user, err := users.Create(ctx, models.CreateUserInput{Username: fmt.Sprintf("jfroute-%d", suffix), Email: fmt.Sprintf("jfroute-%d@example.test", suffix), Password: "correct horse battery", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	provider := &linkingLocalProvider{LocalProvider: auth.NewLocalProvider(users, sessions), users: users, userID: user.ID}
	svc := auth.NewService(provider, auth.NewJWTService("synthetic-jelly-route-secret-0123456789", time.Minute, time.Hour), sessions, users, nil, nil, nil)
	directory := &countingDirectory{}
	svc.SetPluginProviderSource(directorySource{{Info: auth.LoginProviderInfo{ID: "plugin:1:ldap", Mode: auth.ProviderModeCredentials, InstallationID: 1}, Provider: directory}})
	resolver := NewLoginResolver(svc, nil, NewSessionStore(time.Hour, nil), nil, nil)
	if _, err := resolver.Resolve(ctx, user.Username, "correct horse battery#1234", "test", ""); err == nil {
		t.Fatal("retired local credentials signed in")
	}
	if len(directory.passwords) != 0 {
		t.Fatalf("local password fallback reached the directory: %q", directory.passwords)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1`, user.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("retired local credentials opened %d sessions", count)
	}
}

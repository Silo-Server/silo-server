package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type pausedLocalLoginProvider struct {
	*LocalProvider
	verified chan struct{}
	resume   chan struct{}
}

func (p *pausedLocalLoginProvider) Authenticate(ctx context.Context, creds Credentials) (*models.User, error) {
	user, err := p.LocalProvider.Authenticate(ctx, creds)
	if err != nil {
		return nil, err
	}
	close(p.verified)
	select {
	case <-p.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return user, nil
}

func TestLocalLoginRechecksAccountBeforeSessionDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(context.Context, *externalSignInEnv, *models.User) error
		want   error
	}{
		{
			name: "verified email auto-match",
			change: func(ctx context.Context, env *externalSignInEnv, user *models.User) error {
				identity := env.identity("verified-provider-person")
				identity.Email, identity.EmailVerified = user.Email, new(true)
				_, _, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, AutoProvision: true, Identity: identity})
				return err
			},
			want: ErrInvalidCredentials,
		},
		{
			name: "explicit identity link",
			change: func(ctx context.Context, env *externalSignInEnv, user *models.User) error {
				_, _, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, LinkingUserID: user.ID, Identity: env.identity("linked-provider-person")})
				return err
			},
			want: ErrInvalidCredentials,
		},
		{
			name: "password reset",
			change: func(ctx context.Context, env *externalSignInEnv, user *models.User) error {
				return NewUserRepository(env.pool).Update(ctx, user.ID, models.UpdateUserInput{Password: new("replacement password")})
			},
			want: ErrInvalidCredentials,
		},
		{
			name: "disabled account",
			change: func(ctx context.Context, env *externalSignInEnv, user *models.User) error {
				return NewUserRepository(env.pool).Update(ctx, user.ID, models.UpdateUserInput{Enabled: new(false)})
			},
			want: ErrUserDisabled,
		},
		{
			name: "disabled local sign-in",
			change: func(ctx context.Context, env *externalSignInEnv, _ *models.User) error {
				_, err := env.pool.Exec(ctx, `UPDATE server_settings SET value = 'false' WHERE key = $1`, config.AuthLocalPasswordLoginSettingKey)
				return err
			},
			want: ErrLocalLoginDisabled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newExternalSignInEnv(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
			env.setSetting(t, config.AuthEmailAutoMatchSettingKey, "true")
			user := env.localAccount(t, "in-flight-local-login", models.RoleUser)
			users, sessions := NewUserRepository(env.pool), NewSessionRepository(env.pool)
			provider := &pausedLocalLoginProvider{LocalProvider: NewLocalProvider(users, sessions), verified: make(chan struct{}), resume: make(chan struct{})}
			svc := NewService(provider, NewJWTService("synthetic-local-login-secret-0123456789", time.Minute, 24*time.Hour), sessions, users, nil, nil, nil)
			type loginResult struct {
				pair *TokenPair
				user *models.User
				err  error
			}
			result := make(chan loginResult, 1)
			go func() {
				pair, signedIn, err := svc.Login(ctx, user.Username, "correct horse battery", "race-test", "")
				result <- loginResult{pair: pair, user: signedIn, err: err}
			}()
			select {
			case <-provider.verified:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			changeErr := tc.change(ctx, env, user)
			close(provider.resume)
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			select {
			case login := <-result:
				if !errors.Is(login.err, tc.want) || login.pair != nil || login.user != nil {
					t.Fatalf("login returned pair=%v user=%v error=%v, want %v without credentials", login.pair != nil, login.user != nil, login.err, tc.want)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var count int
			if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = $1`, user.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("local login created %d sessions after its credentials were retired", count)
			}
		})
	}
}

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type pausedInvitePolicyUsers struct {
	*UserRepository
	checked chan struct{}
	resume  chan struct{}
}

func (r *pausedInvitePolicyUsers) CreateInvited(ctx context.Context, in models.CreateUserInput, code string, provision func(*models.User, pgx.Tx) error) (*models.User, error) {
	close(r.checked)
	select {
	case <-r.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return r.UserRepository.CreateInvited(ctx, in, code, provision)
}

func TestSignupRechecksLocalPolicyBeforeSpendingInviteDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	t.Cleanup(saveSettings(t, env.pool, "signup.enabled"))
	env.setSetting(t, "signup.enabled", "true")
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	creator := env.localAccount(t, "policy-owner", models.RoleAdmin)
	if err := NewUserRepository(env.pool).Update(ctx, creator.ID, models.UpdateUserInput{BreakGlass: new(true)}); err != nil {
		t.Fatal(err)
	}
	code := env.name("policy-invite")
	if _, err := env.pool.Exec(ctx, `INSERT INTO invite_codes (code,max_uses,created_by) VALUES ($1,1,$2)`, code, creator.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = env.pool.Exec(context.Background(), `DELETE FROM invite_codes WHERE code=$1`, code) })
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-signup-secret-0123456789", time.Minute, time.Hour), sessions, users, NewInviteCodeRepository(env.pool), dbSettings{env}, nil)
	paused := &pausedInvitePolicyUsers{UserRepository: users, checked: make(chan struct{}), resume: make(chan struct{})}
	svc.accounts = NewAccountProvisioner(paused, nil)
	name := env.name("late-signup")
	result := make(chan error, 1)
	go func() {
		_, _, err := svc.Signup(ctx, name, name+"@example.test", "correct horse battery", code, false, "", "test", "")
		result <- err
	}()
	select {
	case <-paused.checked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	close(paused.resume)
	var signupErr error
	select {
	case signupErr = <-result:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var uses int
	if err := env.pool.QueryRow(ctx, `SELECT use_count FROM invite_codes WHERE code=$1`, code).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	_, lookupErr := users.GetByUsername(ctx, name)
	if !errors.Is(signupErr, ErrLocalLoginDisabled) || uses != 0 || !IsNotFound(lookupErr) {
		t.Fatalf("signup error=%v invite uses=%d account lookup=%v, want disabled sign-in with an unspent invite and no account", signupErr, uses, lookupErr)
	}
}

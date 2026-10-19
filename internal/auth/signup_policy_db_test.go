package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Pause after the account and profile writes, while CreateInvited still owns
// the settings lock and has not committed either write or the invite use.
type pausedInviteCommitUsers struct {
	*UserRepository
	written chan struct{}
	resume  chan struct{}
}

func (r *pausedInviteCommitUsers) CreateInvited(ctx context.Context, in models.CreateUserInput, code string, provision func(*models.User, pgx.Tx) error) (*models.User, error) {
	return r.UserRepository.CreateInvited(ctx, in, code, func(user *models.User, tx pgx.Tx) error {
		if err := provision(user, tx); err != nil {
			return err
		}
		close(r.written)
		select {
		case <-r.resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

func TestSignupPolicySerializationDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	t.Cleanup(saveSettings(t, env.pool, "signup.enabled"))
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	creator := env.localAccount(t, "signup-policy-owner", models.RoleAdmin)
	peerConfig := env.pool.Config()
	peerConfig.ConnConfig.RuntimeParams["application_name"] = env.name("signup-policy-peer")
	peer, err := pgxpool.NewWithConfig(ctx, peerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	node := func(pool *pgxpool.Pool) *Service {
		users, sessions := NewUserRepository(pool), NewSessionRepository(pool)
		return NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-signup-policy-secret", time.Minute, time.Hour), sessions, users, NewInviteCodeRepository(pool), catalog.NewServerSettingsRepo(pool), pgstore.NewPostgresProvider(pool))
	}
	a, b := node(env.pool), node(peer)
	settings := catalog.NewServerSettingsRepo(peer)
	setEnabled := func(t *testing.T, enabled string) {
		t.Helper()
		if err := settings.Set(ctx, "signup.enabled", enabled); err != nil {
			t.Fatal(err)
		}
	}
	invite := func(t *testing.T, label string) string {
		t.Helper()
		code := env.name(label)
		if _, err := env.pool.Exec(ctx, `INSERT INTO invite_codes (code,max_uses,created_by) VALUES ($1,1,$2)`, code, creator.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM invite_codes WHERE code=$1`, code)
		})
		return code
	}
	signup := func(svc *Service, name, code string) error {
		_, _, err := svc.Signup(ctx, name, name+"@example.test", "correct horse battery", code, true, "Home", "test", "")
		return err
	}
	wait := func(t *testing.T, signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	result := func(t *testing.T, done <-chan error) error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return ctx.Err()
		}
	}
	check := func(t *testing.T, name, code string, want int) {
		t.Helper()
		var accounts, profiles, uses int
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username=$1`, name).Scan(&accounts); err != nil {
			t.Fatal(err)
		}
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM user_profiles p JOIN users u ON u.id=p.user_id WHERE u.username=$1`, name).Scan(&profiles); err != nil {
			t.Fatal(err)
		}
		if err := env.pool.QueryRow(ctx, `SELECT use_count FROM invite_codes WHERE code=$1`, code).Scan(&uses); err != nil {
			t.Fatal(err)
		}
		t.Logf("accounts=%d profiles=%d invite uses=%d, want each %d", accounts, profiles, uses, want)
		if accounts != want || profiles != want || uses != want {
			t.Fatalf("accounts=%d profiles=%d invite uses=%d, want each %d", accounts, profiles, uses, want)
		}
	}

	t.Run("disable commits before signup transaction", func(t *testing.T) {
		setEnabled(t, "true")
		code, name := invite(t, "disable-first"), env.name("refused-signup")
		paused := &pausedInvitePolicyUsers{UserRepository: a.users, checked: make(chan struct{}), resume: make(chan struct{})}
		a.accounts = NewAccountProvisioner(paused, pgstore.NewPostgresProvider(env.pool))
		done := make(chan error, 1)
		go func() { done <- signup(a, name, code) }()
		wait(t, paused.checked)
		setEnabled(t, "false")
		close(paused.resume)
		if err := result(t, done); !errors.Is(err, ErrSignupDisabled) {
			t.Errorf("in-flight signup = %v, want ErrSignupDisabled", err)
		}
		check(t, name, code, 0)
		if err := signup(b, name, code); !errors.Is(err, ErrSignupDisabled) {
			t.Fatalf("peer signup = %v, want ErrSignupDisabled", err)
		}
		check(t, name, code, 0)
	})

	t.Run("signup commits before disable", func(t *testing.T) {
		setEnabled(t, "true")
		code, name := invite(t, "signup-first"), env.name("committed-signup")
		paused := &pausedInviteCommitUsers{UserRepository: a.users, written: make(chan struct{}), resume: make(chan struct{})}
		a.accounts = NewAccountProvisioner(paused, pgstore.NewPostgresProvider(env.pool))
		done := make(chan error, 1)
		go func() { done <- signup(a, name, code) }()
		wait(t, paused.written)
		disabled := make(chan error, 1)
		go func() { disabled <- settings.Set(ctx, "signup.enabled", "false") }()
		// Observe the other replica waiting on the advisory lock. Merely
		// starting its goroutine does not establish the transaction order.
		for {
			var waiting bool
			if err := env.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND application_name=$1
				AND wait_event_type='Lock' AND wait_event='advisory')`, peerConfig.ConnConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
		check(t, name, code, 0)
		close(paused.resume)
		if err := result(t, done); err != nil {
			t.Fatalf("signup whose commit wins = %v", err)
		}
		if err := result(t, disabled); err != nil {
			t.Fatal(err)
		}
		check(t, name, code, 1)
		if err := signup(b, name, code); !errors.Is(err, ErrSignupDisabled) {
			t.Fatalf("retry after disable = %v, want ErrSignupDisabled", err)
		}
		setEnabled(t, "true")
		if err := signup(b, env.name("second-signup"), code); !errors.Is(err, ErrInviteCodeExhausted) {
			t.Fatalf("spent invite on peer = %v, want ErrInviteCodeExhausted", err)
		}
		check(t, name, code, 1)
		if _, err := b.users.GetByUsername(ctx, env.name("second-signup")); !IsNotFound(err) {
			t.Fatalf("exhausted invite created a second account: %v", err)
		}
	})
}

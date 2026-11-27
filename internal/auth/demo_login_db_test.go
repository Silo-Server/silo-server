package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

type dbDemoSettings struct {
	pool *pgxpool.Pool
	err  error
}

func (s *dbDemoSettings) Get(ctx context.Context, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func openDemoLoginDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(saveSettings(t, pool, demomode.SettingKey, demomode.UsernameKey))
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id IN (SELECT id FROM users WHERE username LIKE $1)`, "demologin-"+suffix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, "demologin-"+suffix+"%")
	})
	return pool, suffix
}

func setDemoSettingDB(t *testing.T, pool *pgxpool.Pool, key, value string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatal(err)
	}
}

func deleteDemoSettingDB(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `DELETE FROM server_settings WHERE key = $1`, key); err != nil {
		t.Fatal(err)
	}
}

func insertDemoTestUser(t *testing.T, pool *pgxpool.Pool, username, role string, enabled, pwChange bool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO users (username, email, password_hash, role, enabled, password_change_required)
		VALUES ($1, $2, 'synthetic-hash', $3, $4, $5) RETURNING id`,
		username, username+"@example.test", role, enabled, pwChange).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDemoLoginEligibilityDB(t *testing.T) {
	pool, suffix := openDemoLoginDB(t)
	ctx := t.Context()
	users := NewUserRepository(pool)
	sessions := NewSessionRepository(pool)
	jwt := NewJWTService("demo-login-test-secret-0123456789", 15*time.Minute, 30*24*time.Hour)
	settings := &dbDemoSettings{pool: pool}
	svc := NewService(NewLocalProvider(users, sessions), jwt, sessions, users, nil, settings, nil)

	eligibleName := fmt.Sprintf("demologin-%s-ok", suffix)
	adminName := fmt.Sprintf("demologin-%s-admin", suffix)
	disabledName := fmt.Sprintf("demologin-%s-disabled", suffix)
	tempPwName := fmt.Sprintf("demologin-%s-temppw", suffix)
	eligibleID := insertDemoTestUser(t, pool, eligibleName, models.RoleUser, true, false)
	insertDemoTestUser(t, pool, adminName, models.RoleAdmin, true, false)
	insertDemoTestUser(t, pool, disabledName, models.RoleUser, false, false)
	insertDemoTestUser(t, pool, tempPwName, models.RoleUser, true, true)

	t.Run("disabled and misconfigured states return ErrDemoLoginDisabled", func(t *testing.T) {
		deleteDemoSettingDB(t, pool, demomode.SettingKey)
		deleteDemoSettingDB(t, pool, demomode.UsernameKey)
		if _, _, err := svc.DemoLogin(ctx, "Roku", "192.0.2.1"); !errors.Is(err, ErrDemoLoginDisabled) {
			t.Fatalf("unset settings err = %v, want ErrDemoLoginDisabled", err)
		}

		setDemoSettingDB(t, pool, demomode.SettingKey, "false")
		setDemoSettingDB(t, pool, demomode.UsernameKey, eligibleName)
		if _, _, err := svc.DemoLogin(ctx, "Roku", "192.0.2.1"); !errors.Is(err, ErrDemoLoginDisabled) {
			t.Fatalf("demo.enabled=false err = %v, want ErrDemoLoginDisabled", err)
		}

		setDemoSettingDB(t, pool, demomode.SettingKey, demomode.OnValue)
		for _, badName := range []string{"", "   ", "demologin-" + suffix + "-missing", adminName, disabledName, tempPwName} {
			setDemoSettingDB(t, pool, demomode.UsernameKey, badName)
			if _, _, err := svc.DemoLogin(ctx, "Roku", "192.0.2.1"); !errors.Is(err, ErrDemoLoginDisabled) {
				t.Fatalf("username %q err = %v, want ErrDemoLoginDisabled", badName, err)
			}
		}
	})

	t.Run("settings read error fails closed without ErrDemoLoginDisabled", func(t *testing.T) {
		settings.err = errors.New("settings store unreachable")
		defer func() { settings.err = nil }()
		_, _, err := svc.DemoLogin(ctx, "Roku", "192.0.2.1")
		if err == nil || errors.Is(err, ErrDemoLoginDisabled) {
			t.Fatalf("store error = %v, want non-nil non-ErrDemoLoginDisabled", err)
		}
	})

	t.Run("eligible account opens bounded demo-purpose session", func(t *testing.T) {
		setDemoSettingDB(t, pool, demomode.SettingKey, demomode.OnValue)
		setDemoSettingDB(t, pool, demomode.UsernameKey, strings.ToUpper(eligibleName))

		pair, user, err := svc.DemoLogin(ctx, "Roku Ultra", "192.0.2.5")
		if err != nil {
			t.Fatalf("DemoLogin() error = %v", err)
		}
		if user.ID != eligibleID || pair.SessionID == "" {
			t.Fatalf("got user=%d session=%q, want user=%d non-empty session", user.ID, pair.SessionID, eligibleID)
		}

		claims, err := jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatalf("ValidateToken(access) error = %v", err)
		}
		if !claims.IsDemo() || claims.IsOwnLoginSession() {
			t.Fatalf("claims IsDemo=%v IsOwnLoginSession=%v, want true/false", claims.IsDemo(), claims.IsOwnLoginSession())
		}

		row, err := sessions.GetByID(ctx, pair.SessionID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if !row.IsDemo() || row.AbsoluteExpiresAt == nil {
			t.Fatalf("row purpose=%q abs=%v, want demo + non-nil absolute_expires_at", row.Purpose, row.AbsoluteExpiresAt)
		}
		if row.ExpiresAt.After(row.AbsoluteExpiresAt.Add(time.Second)) {
			t.Fatalf("expires_at %v exceeds absolute_expires_at %v", row.ExpiresAt, *row.AbsoluteExpiresAt)
		}

		authority, active, err := sessions.ActiveSession(ctx, pair.SessionID, eligibleID)
		if err != nil || !active || !authority.IsDemo() {
			t.Fatalf("ActiveSession = (%+v, %v, %v), want active demo", authority, active, err)
		}
		limited, err := svc.SessionLimitedToItself(ctx, pair.SessionID)
		if err != nil || !limited {
			t.Fatalf("SessionLimitedToItself = (%v, %v), want true/nil", limited, err)
		}
	})
}

func requireDemoSessionRevokedDB(t *testing.T, sessions *SessionRepository, svc *Service, pair *TokenPair, userID int, label string) {
	t.Helper()
	ctx := t.Context()
	if _, active, err := sessions.ActiveSession(ctx, pair.SessionID, userID); err != nil || active {
		t.Fatalf("%s: ActiveSession = (%v, %v), want false/nil", label, active, err)
	}
	if _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("%s: Refresh err = %v, want ErrSessionRevoked", label, err)
	}
}

func TestDemoLoginPromotionAndRevocationDB(t *testing.T) {
	pool, suffix := openDemoLoginDB(t)
	ctx := t.Context()
	users := NewUserRepository(pool)
	sessions := NewSessionRepository(pool)
	jwt := NewJWTService("demo-login-revocation-secret-012345", 15*time.Minute, 30*24*time.Hour)
	settings := &dbDemoSettings{pool: pool}
	svc := NewService(NewLocalProvider(users, sessions), jwt, sessions, users, nil, settings, nil)

	userAName := "demologin-" + suffix + "-a"
	userBName := "demologin-" + suffix + "-b"
	userAID := insertDemoTestUser(t, pool, userAName, models.RoleUser, true, false)
	insertDemoTestUser(t, pool, userBName, models.RoleUser, true, false)

	openSession := func(username string) *TokenPair {
		t.Helper()
		setDemoSettingDB(t, pool, demomode.SettingKey, demomode.OnValue)
		setDemoSettingDB(t, pool, demomode.UsernameKey, username)
		pair, _, err := svc.DemoLogin(ctx, "Roku", "192.0.2.9")
		if err != nil {
			t.Fatalf("DemoLogin(%q) error = %v", username, err)
		}
		return pair
	}

	t.Run("refresh preserves demo purpose and caps at absolute_expires_at", func(t *testing.T) {
		pair := openSession(userAName)
		refreshed, err := svc.Refresh(ctx, pair.RefreshToken)
		if err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
		claims, err := jwt.ValidateToken(refreshed.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if !claims.IsDemo() || claims.IsOwnLoginSession() {
			t.Fatalf("refreshed claims IsDemo=%v IsOwnLoginSession=%v", claims.IsDemo(), claims.IsOwnLoginSession())
		}
		row, err := sessions.GetByID(ctx, pair.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if row.AbsoluteExpiresAt == nil || row.ExpiresAt.After(row.AbsoluteExpiresAt.Add(time.Second)) {
			t.Fatalf("refreshed expires_at %v exceeded absolute_expires_at %v", row.ExpiresAt, row.AbsoluteExpiresAt)
		}
		settings.err = errors.New("settings unreachable")
		_, err = svc.Refresh(ctx, refreshed.RefreshToken)
		settings.err = nil
		if !errors.Is(err, ErrSessionCheckUnavailable) {
			t.Fatalf("Refresh during settings outage err = %v, want ErrSessionCheckUnavailable", err)
		}
	})

	t.Run("off then on revokes and never revives old demo sessions", func(t *testing.T) {
		pair := openSession(userAName)
		setDemoSettingDB(t, pool, demomode.SettingKey, "false")
		requireDemoSessionRevokedDB(t, sessions, svc, pair, userAID, "after disable")
		setDemoSettingDB(t, pool, demomode.SettingKey, demomode.OnValue)
		requireDemoSessionRevokedDB(t, sessions, svc, pair, userAID, "after re-enable")
	})

	t.Run("retargeting A to B and back to A never revives A sessions", func(t *testing.T) {
		pair := openSession(userAName)
		setDemoSettingDB(t, pool, demomode.UsernameKey, userBName)
		requireDemoSessionRevokedDB(t, sessions, svc, pair, userAID, "after retarget")
		setDemoSettingDB(t, pool, demomode.UsernameKey, userAName)
		requireDemoSessionRevokedDB(t, sessions, svc, pair, userAID, "after retarget back")
	})

	t.Run("deleting demo.username revokes active demo sessions", func(t *testing.T) {
		pair := openSession(userAName)
		deleteDemoSettingDB(t, pool, demomode.UsernameKey)
		requireDemoSessionRevokedDB(t, sessions, svc, pair, userAID, "after delete demo.username")
	})

	t.Run("promoting demo account to admin revokes demo sessions and refuses refresh", func(t *testing.T) {
		pair := openSession(userAName)
		adminRole := models.RoleAdmin
		if err := users.Update(ctx, userAID, models.UpdateUserInput{Role: &adminRole}); err != nil {
			t.Fatalf("promote user: %v", err)
		}
		requireDemoSessionRevokedDB(t, sessions, svc, pair, userAID, "after promotion")
	})
}

func TestDemoMigrationDownWithLiveDemoRowDB(t *testing.T) {
	pool, suffix := openDemoLoginDB(t)
	ctx := t.Context()

	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "sql", "20261127021500_add_demo_session_purpose.sql"))
	if err != nil {
		t.Fatal(err)
	}
	_, downSQL, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		t.Fatal("migration missing -- +goose Down")
	}

	userID := insertDemoTestUser(t, pool, fmt.Sprintf("demologin-%s-down", suffix), models.RoleUser, true, false)
	sessionID := uuid.NewString()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_sessions (id, user_id, device_name, expires_at, purpose, absolute_expires_at)
		VALUES ($1, $2, 'Roku', NOW() + INTERVAL '24 hours', 'demo', NOW() + INTERVAL '24 hours')`,
		sessionID, userID); err != nil {
		t.Fatalf("insert demo row: %v", err)
	}
	if _, err := tx.Exec(ctx, downSQL); err != nil {
		t.Fatalf("down migration failed with live demo row: %v", err)
	}

	var purpose string
	var revoked bool
	if err := tx.QueryRow(ctx, `SELECT purpose, revoked_at IS NOT NULL FROM auth_sessions WHERE id = $1`, sessionID).
		Scan(&purpose, &revoked); err != nil {
		t.Fatal(err)
	}
	if purpose != models.SessionPurposeAccount || !revoked {
		t.Fatalf("after down migration: purpose=%q revoked=%v, want account/true", purpose, revoked)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_sessions (id, user_id, device_name, expires_at, purpose, absolute_expires_at)
		VALUES ($1, $2, 'Roku', NOW() + INTERVAL '24 hours', 'demo', NOW() + INTERVAL '24 hours')`,
		uuid.NewString(), userID); err == nil {
		t.Fatal("restored constraint still allowed inserting purpose='demo'")
	}
}

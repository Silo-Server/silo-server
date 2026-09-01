package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type authorityFixture struct {
	pool                  *pgxpool.Pool
	users                 *UserRepository
	sessions              *SessionRepository
	account               *models.User
	other                 *models.User
	ownedSessionID        string
	impersonatedSessionID string
	compatToken           string
}

func newAuthorityFixture(t *testing.T) *authorityFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	users := NewUserRepository(pool)
	account, err := users.Create(t.Context(), models.CreateUserInput{
		Username: "authority-account-" + suffix,
		Email:    "authority-account-" + suffix + "@example.invalid",
		Password: "initial-password",
		Role:     models.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create authority account: %v", err)
	}
	other, err := users.Create(t.Context(), models.CreateUserInput{
		Username: "authority-other-" + suffix,
		Email:    "authority-other-" + suffix + "@example.invalid",
		Password: "initial-password",
		Role:     models.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create other account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = ANY($1)`, []int{account.ID, other.ID})
	})

	sessions := NewSessionRepository(pool)
	ownedSessionID := uuid.NewString()
	if err := sessions.Create(t.Context(), models.AuthSession{
		ID:           ownedSessionID,
		UserID:       account.ID,
		DeviceName:   "authority-test-owner",
		ExpiresAt:    time.Now().Add(time.Hour),
		AuthRevision: account.AuthRevision,
	}); err != nil {
		t.Fatalf("create owned session: %v", err)
	}
	impersonatedSessionID := uuid.NewString()
	if err := sessions.Create(t.Context(), models.AuthSession{
		ID:                       impersonatedSessionID,
		UserID:                   other.ID,
		DeviceName:               "authority-test-impersonation",
		ExpiresAt:                time.Now().Add(time.Hour),
		AuthRevision:             other.AuthRevision,
		ImpersonatorUserID:       &account.ID,
		ImpersonatorAuthRevision: &account.AuthRevision,
	}); err != nil {
		t.Fatalf("create impersonation session: %v", err)
	}

	compatToken := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO jellycompat_sessions (
			token, username, account_username, profile_id, profile_name, pseudo_user_id,
			streamapp_user_id, streamapp_session_id, auth_revision,
			streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry,
			created_at, expires_at
		) VALUES ($1, $2, $2, $3, $2, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		compatToken,
		account.Username,
		"profile-1",
		uuid.New(),
		account.ID,
		ownedSessionID,
		account.AuthRevision,
		"test-access-token",
		"test-refresh-token",
		time.Now().Add(time.Hour),
		time.Now(),
		time.Now().Add(time.Hour),
	); err != nil {
		t.Fatalf("create compat session: %v", err)
	}

	return &authorityFixture{
		pool:                  pool,
		users:                 users,
		sessions:              sessions,
		account:               account,
		other:                 other,
		ownedSessionID:        ownedSessionID,
		impersonatedSessionID: impersonatedSessionID,
		compatToken:           compatToken,
	}
}

func TestUpdateAndRevokeAuthorityInvalidatesEverySessionSurface(t *testing.T) {
	tests := []struct {
		name   string
		input  func() models.UpdateUserInput
		verify func(*testing.T, *models.User)
	}{
		{
			name: "role downgrade",
			input: func() models.UpdateUserInput {
				return models.UpdateUserInput{Role: new(models.RoleUser)}
			},
			verify: func(t *testing.T, user *models.User) {
				t.Helper()
				if user.Role != models.RoleUser {
					t.Fatalf("role = %q, want %q", user.Role, models.RoleUser)
				}
			},
		},
		{
			name: "disable",
			input: func() models.UpdateUserInput {
				return models.UpdateUserInput{Enabled: new(false)}
			},
			verify: func(t *testing.T, user *models.User) {
				t.Helper()
				if user.Enabled {
					t.Fatal("account remains enabled")
				}
			},
		},
		{
			name: "password reset",
			input: func() models.UpdateUserInput {
				return models.UpdateUserInput{Password: new("replacement-password")}
			},
			verify: func(t *testing.T, user *models.User) {
				t.Helper()
				if !CheckPassword(user, "replacement-password") {
					t.Fatal("replacement password was not committed")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newAuthorityFixture(t)
			oldRevision := fixture.account.AuthRevision
			jwt := NewJWTService("authority-test-secret", time.Hour, time.Hour)
			refreshToken, err := jwt.GenerateRefreshToken(fixture.account.ID, fixture.account.Role, fixture.ownedSessionID)
			if err != nil {
				t.Fatalf("generate refresh token: %v", err)
			}

			if err := fixture.users.UpdateAndRevokeAuthority(t.Context(), fixture.account.ID, tt.input()); err != nil {
				t.Fatalf("update and revoke authority: %v", err)
			}

			updated, err := fixture.users.GetByID(t.Context(), fixture.account.ID)
			if err != nil {
				t.Fatalf("load updated account: %v", err)
			}
			tt.verify(t, updated)
			if updated.AuthRevision != oldRevision+1 {
				t.Fatalf("auth revision = %d, want %d", updated.AuthRevision, oldRevision+1)
			}
			for _, sessionID := range []string{fixture.ownedSessionID, fixture.impersonatedSessionID} {
				valid, err := fixture.sessions.IsValid(t.Context(), sessionID)
				if err != nil {
					t.Fatalf("validate session %s: %v", sessionID, err)
				}
				if valid {
					t.Fatalf("session %s remains valid", sessionID)
				}
			}
			var compatCount int
			if err := fixture.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM jellycompat_sessions WHERE token = $1`, fixture.compatToken).Scan(&compatCount); err != nil {
				t.Fatalf("count compat sessions: %v", err)
			}
			if compatCount != 0 {
				t.Fatalf("compat session count = %d, want 0", compatCount)
			}

			service := NewService(nil, jwt, fixture.sessions, fixture.users, nil, nil, nil)
			if _, err := service.Refresh(t.Context(), refreshToken); !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("refresh error = %v, want ErrSessionRevoked", err)
			}

			staleSessionID := uuid.NewString()
			if err := fixture.sessions.Create(t.Context(), models.AuthSession{
				ID:           staleSessionID,
				UserID:       fixture.account.ID,
				ExpiresAt:    time.Now().Add(time.Hour),
				AuthRevision: oldRevision,
			}); err != nil {
				t.Fatalf("insert simulated in-flight login session: %v", err)
			}
			valid, err := fixture.sessions.IsValid(t.Context(), staleSessionID)
			if err != nil {
				t.Fatalf("validate simulated in-flight login: %v", err)
			}
			if valid {
				t.Fatal("session inserted from a stale authentication snapshot is valid")
			}
		})
	}
}

func TestUpdateAndRevokeAuthorityRollsBackMutationWhenRevocationFails(t *testing.T) {
	fixture := newAuthorityFixture(t)
	oldRevision := fixture.account.AuthRevision
	functionName := pgx.Identifier{"fail_authority_revoke_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	triggerName := pgx.Identifier{"fail_authority_revoke_trigger_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()

	if _, err := fixture.pool.Exec(t.Context(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected authority revocation failure';
		END;
		$$`, functionName)); err != nil {
		t.Fatalf("create failure function: %v", err)
	}
	if _, err := fixture.pool.Exec(t.Context(), fmt.Sprintf(`
		CREATE TRIGGER %s
		BEFORE UPDATE OF revoked_at ON auth_sessions
		FOR EACH ROW WHEN (OLD.user_id = %d)
		EXECUTE FUNCTION %s()`, triggerName, fixture.account.ID, functionName)); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON auth_sessions`, triggerName))
		_, _ = fixture.pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	err := fixture.users.UpdateAndRevokeAuthority(
		t.Context(),
		fixture.account.ID,
		models.UpdateUserInput{Role: new(models.RoleUser)},
	)
	if err == nil || !strings.Contains(err.Error(), "injected authority revocation failure") {
		t.Fatalf("update error = %v, want injected revocation failure", err)
	}

	unchanged, err := fixture.users.GetByID(t.Context(), fixture.account.ID)
	if err != nil {
		t.Fatalf("load rolled-back account: %v", err)
	}
	if unchanged.Role != models.RoleAdmin || unchanged.AuthRevision != oldRevision {
		t.Fatalf("rolled-back account role/revision = %q/%d, want %q/%d", unchanged.Role, unchanged.AuthRevision, models.RoleAdmin, oldRevision)
	}
	valid, err := fixture.sessions.IsValid(t.Context(), fixture.ownedSessionID)
	if err != nil {
		t.Fatalf("validate rolled-back session: %v", err)
	}
	if !valid {
		t.Fatal("session was revoked despite transaction rollback")
	}
	var compatCount int
	if err := fixture.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM jellycompat_sessions WHERE token = $1`, fixture.compatToken).Scan(&compatCount); err != nil {
		t.Fatalf("count compat sessions: %v", err)
	}
	if compatCount != 1 {
		t.Fatalf("compat session count = %d, want 1 after rollback", compatCount)
	}
}

func TestDeleteAndRevokeAuthorityCoversOwnedAndImpersonatedCredentials(t *testing.T) {
	fixture := newAuthorityFixture(t)
	apiKey := "sa_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := fixture.pool.Exec(t.Context(), `
		INSERT INTO api_keys (user_id, label, api_key)
		VALUES ($1, 'authority deletion test', $2)`, fixture.account.ID, apiKey); err != nil {
		t.Fatalf("create API key: %v", err)
	}

	if err := fixture.users.DeleteAndRevokeAuthority(t.Context(), fixture.account.ID); err != nil {
		t.Fatalf("delete and revoke authority: %v", err)
	}
	if _, err := fixture.users.GetByID(t.Context(), fixture.account.ID); !IsNotFound(err) {
		t.Fatalf("deleted account lookup error = %v, want ErrNotFound", err)
	}
	var ownedCount, compatCount, apiKeyCount int
	if err := fixture.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM auth_sessions WHERE id = $1`, fixture.ownedSessionID).Scan(&ownedCount); err != nil {
		t.Fatalf("count owned sessions: %v", err)
	}
	if err := fixture.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM jellycompat_sessions WHERE token = $1`, fixture.compatToken).Scan(&compatCount); err != nil {
		t.Fatalf("count compat sessions: %v", err)
	}
	if err := fixture.pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM api_keys WHERE api_key = $1`, apiKey).Scan(&apiKeyCount); err != nil {
		t.Fatalf("count API keys: %v", err)
	}
	if ownedCount != 0 || compatCount != 0 || apiKeyCount != 0 {
		t.Fatalf("owned credential counts after delete = native:%d compat:%d api_key:%d, want all zero", ownedCount, compatCount, apiKeyCount)
	}
	valid, err := fixture.sessions.IsValid(t.Context(), fixture.impersonatedSessionID)
	if err != nil {
		t.Fatalf("validate impersonated session: %v", err)
	}
	if valid {
		t.Fatal("session created by the deleted impersonator remains valid")
	}
}

func TestRevokedSourceSessionCannotMintNewCredentials(t *testing.T) {
	fixture := newAuthorityFixture(t)
	jwt := NewJWTService("authority-test-secret", time.Hour, time.Hour)
	device := NewDeviceLoginService(fixture.pool, fixture.users, jwt, fixture.sessions, nil, nil)
	started, err := device.Start(t.Context(), DeviceLoginStartInput{
		DeviceName:    "authority-review-device",
		ClientPurpose: DeviceLoginPurposeLogin,
	})
	if err != nil {
		t.Fatalf("start device login: %v", err)
	}
	if err := device.Approve(
		t.Context(),
		DeviceLoginLookupInput{UserCode: started.UserCode},
		DeviceLoginApproval{UserID: fixture.account.ID, SessionID: fixture.ownedSessionID},
	); err != nil {
		t.Fatalf("approve device login: %v", err)
	}

	target, err := fixture.users.Create(t.Context(), models.CreateUserInput{
		Username: "authority-target-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Email:    "authority-target-" + strings.ReplaceAll(uuid.NewString(), "-", "") + "@example.invalid",
		Password: "initial-password",
		Role:     models.RoleUser,
	})
	if err != nil {
		t.Fatalf("create impersonation target: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, target.ID)
	})

	if err := fixture.users.UpdateAndRevokeAuthority(
		t.Context(),
		fixture.account.ID,
		models.UpdateUserInput{Password: new("replacement-password")},
	); err != nil {
		t.Fatalf("reset source account authority: %v", err)
	}

	if result, err := device.Poll(t.Context(), started.DeviceCode); !errors.Is(err, ErrDeviceLoginApproverInvalid) || result != nil {
		t.Fatalf("poll after source revocation = (%+v, %v), want ErrDeviceLoginApproverInvalid", result, err)
	}

	apiKeys := NewAPIKeyRepository(fixture.pool)
	if key, err := apiKeys.CreateAuthorized(
		t.Context(),
		fixture.other.ID,
		"stale-authority-key",
		nil,
		SessionAuthorization{UserID: fixture.account.ID, SessionID: fixture.ownedSessionID, RequireAdmin: true},
	); !errors.Is(err, ErrSessionNotFound) || key != nil {
		t.Fatalf("API key creation after source revocation = (%+v, %v), want ErrSessionNotFound", key, err)
	}

	service := NewService(nil, jwt, fixture.sessions, fixture.users, nil, nil, nil)
	claims := &Claims{
		UserID:    fixture.account.ID,
		Role:      models.RoleAdmin,
		SessionID: fixture.ownedSessionID,
		TokenType: TokenTypeAccess,
	}
	pair, _, _, err := service.StartImpersonation(
		WithClaims(t.Context(), claims),
		fixture.account.ID,
		target.ID,
		"stale-authority-impersonation",
		"",
	)
	if !errors.Is(err, ErrImpersonationNotAllowed) || pair != nil {
		t.Fatalf("impersonation after source revocation = (%+v, %v), want ErrImpersonationNotAllowed", pair, err)
	}
}

func TestImpersonatedSessionCannotApproveDeviceLogin(t *testing.T) {
	fixture := newAuthorityFixture(t)
	device := NewDeviceLoginService(
		fixture.pool,
		fixture.users,
		NewJWTService("authority-test-secret", time.Hour, time.Hour),
		fixture.sessions,
		nil,
		nil,
	)
	started, err := device.Start(t.Context(), DeviceLoginStartInput{
		DeviceName:    "impersonated-approval-device",
		ClientPurpose: DeviceLoginPurposeLogin,
	})
	if err != nil {
		t.Fatalf("start device login: %v", err)
	}
	if err := device.Approve(
		t.Context(),
		DeviceLoginLookupInput{UserCode: started.UserCode},
		DeviceLoginApproval{UserID: fixture.other.ID, SessionID: fixture.impersonatedSessionID},
	); !errors.Is(err, ErrDeviceLoginApproverInvalid) {
		t.Fatalf("impersonated approval error = %v, want ErrDeviceLoginApproverInvalid", err)
	}
}

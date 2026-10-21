package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestDeviceApprovalInheritsSessionProviderChainDB: the handlers pass the
// approving login session's claims to the device service, so a device
// approved from a session opened through an external provider gets a
// session carrying that identity and provider_since, which refresh then
// re-checks. Both the login approval and the remote-playback handoff do.
func TestDeviceApprovalInheritsSessionProviderChainDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("devchain%d", time.Now().UnixNano())

	var installationID int
	if err := pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/device-chain-test', true, 'manual', 'plugin') RETURNING id`, "device-chain-"+suffix).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', false)`,
		installationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM device_login_requests WHERE device_name LIKE $1`, "%"+suffix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM plugin_installations WHERE id = $1`, installationID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE username LIKE $1 OR email LIKE $1`, "%"+suffix+"%")
	})

	users := auth.NewUserRepository(pool)
	stores := pgstore.NewPostgresProvider(pool)
	resolver := auth.NewAccountResolver(pool, auth.NewAccountProvisioner(users, stores), nil)
	user, identityID, err := resolver.Resolve(ctx, auth.ResolveInput{InstallationID: installationID, AutoProvision: true,
		Identity: auth.ExternalIdentity{Subject: "https://id.example.test|" + suffix, Issuer: "https://id.example.test",
			Username: suffix, Email: suffix + "@example.test"}})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	store, err := stores.ForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("profiles = %+v, %v", profiles, err)
	}

	sessions := auth.NewSessionRepository(pool)
	since := time.Now().Add(-48 * time.Hour).Truncate(time.Microsecond)
	approver := models.AuthSession{ID: uuid.New().String(), UserID: user.ID, DeviceName: "approver-" + suffix,
		ExpiresAt: time.Now().Add(24 * time.Hour), IdentityID: &identityID, ProviderSince: &since}
	if err := sessions.Create(ctx, approver); err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService("device-chain-test-jwt-secret", time.Hour, 30*24*time.Hour)
	devices := auth.NewDeviceLoginService(pool, users, jwt, sessions, stores, access.NewProfileTokenService("device-chain-profile-secret", time.Hour))
	h := NewAuthHandler(nil, jwt, devices)
	approverCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: user.ID, Role: user.Role, SessionID: approver.ID, TokenType: auth.TokenTypeAccess})

	for name, approve := range map[string]struct {
		purpose string
		run     func(auth.DeviceLoginLookupInput) error
	}{
		"login": {"", func(input auth.DeviceLoginLookupInput) error {
			_, err := h.ApproveDeviceLogin(approverCtx, input, user.ID)
			return err
		}},
		"handoff": {auth.DeviceLoginPurposeRemote, func(input auth.DeviceLoginLookupInput) error {
			_, err := h.ApproveDeviceHandoff(approverCtx, input, user.ID, profiles[0].ID)
			return err
		}},
	} {
		start, err := devices.Start(ctx, auth.DeviceLoginStartInput{DeviceName: name + "-" + suffix, BaseURL: "https://media.example.test/",
			ClientPurpose: approve.purpose, Temporary: approve.purpose != ""})
		if err != nil {
			t.Fatalf("%s: start: %v", name, err)
		}
		if err := approve.run(auth.DeviceLoginLookupInput{UserCode: start.UserCode}); err != nil {
			t.Fatalf("%s: approve: %v", name, err)
		}
		poll, err := devices.Poll(ctx, start.DeviceCode)
		if err != nil || poll.TokenPair == nil {
			t.Fatalf("%s: poll = %+v, %v", name, poll, err)
		}
		claims, err := jwt.ValidateToken(poll.TokenPair.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		device, err := sessions.GetByID(ctx, claims.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if device.IdentityID == nil || *device.IdentityID != identityID || device.ProviderSince == nil || !device.ProviderSince.Equal(since) {
			t.Fatalf("%s: device session chain = identity %v since %v, want identity %d since %v",
				name, device.IdentityID, device.ProviderSince, identityID, since)
		}
	}
}

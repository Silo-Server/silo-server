package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// riskAccount seeds an enabled account; password gives it a usable local
// password.
func (e *externalSignInEnv) riskAccount(t *testing.T, label, role string, password, breakGlass bool) int {
	t.Helper()
	var hash *string
	if password {
		hash = new(externalSignInPasswordHash)
	}
	var id int
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, local_password_login_enabled, break_glass)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, e.name(label), e.name(label)+"@example.test", hash, role, password, breakGlass).Scan(&id); err != nil {
		t.Fatalf("seed %s: %v", label, err)
	}
	return id
}

// riskKey seeds a live key: a device key with status, or a person key when
// status is "". identityID makes it provider-bound.
func (e *externalSignInEnv) riskKey(t *testing.T, userID int, status string, identityID *int64) {
	t.Helper()
	public := make([]byte, 65)
	if _, err := rand.Read(public); err != nil {
		t.Fatal(err)
	}
	kid := hex.EncodeToString(public[:16])
	var err error
	if status == "" {
		_, err = e.pool.Exec(t.Context(), `INSERT INTO auth_person_keys (id, user_id, public_key, key_id, identity_id, provider_bound, enrolled_via)
			VALUES ($1, $2, $3, $4, $5, $6, 'session')`, uuid.NewString(), userID, public, kid, identityID, identityID != nil)
	} else {
		_, err = e.pool.Exec(t.Context(), `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, status, enrolled_via, identity_id, provider_bound)
			VALUES ($1, $2, $3, $4, 'device', 'Phone', $5, 'session', $6, $7)`, uuid.NewString(), userID, public, kid, status, identityID, identityID != nil)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// riskIdentity links an identity at installationID to the account.
func (e *externalSignInEnv) riskIdentity(t *testing.T, userID, installationID int, label string) int64 {
	t.Helper()
	var id int64
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id, issuer, linked_at)
		VALUES ($1, $2, $3, 'https://idp.example.test', NOW()) RETURNING id`, installationID, e.name(label), userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// riskProvider seeds an enabled installation whose auth_provider.v1
// capability declares modes (a JSON array), with its sign-in binding on.
func (e *externalSignInEnv) riskProvider(t *testing.T, label, modes string) int {
	t.Helper()
	var id int
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/risk-test', true, 'manual', 'plugin') RETURNING id`, e.name("risk-"+label)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'sign-in', jsonb_build_object('auth_modes', $2::jsonb))`, id, modes); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'sign-in', true)`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func listsAccount(accounts []AccountAtRisk, id int) bool {
	return slices.ContainsFunc(accounts, func(a AccountAtRisk) bool { return a.ID == id })
}

// Each change lists exactly the accounts it would leave with no way in, or
// with keys as their only way in: a key that works now and no password or
// provider identity that does.
func TestAccountsAtRiskDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, config.ServerPublicURLSettingKey))
	// Providers whose sign-in binding is on, of each kind. env's own
	// installation has its binding off.
	directory := env.riskProvider(t, "directory", `["password"]`)
	oidc := env.riskProvider(t, "oidc", `["oauth2"]`)
	network := env.riskProvider(t, "network", `["network"]`)

	type listed struct{ appKeysOff, localOff bool }
	for i, tc := range []struct {
		name    string
		localOn bool
		seed    func(t *testing.T, id int)
		role    string
		pw, bg  bool
		// publicURL is server.public_url; OAuth sign-in needs one.
		publicURL string
		want      listed
	}{
		{name: "passwordless with a device key", localOn: true, want: listed{true, true},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "passwordless with only a person key", localOn: true, want: listed{true, true},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, "", nil) }},
		{name: "admin with only a person key: a new device waits", localOn: true, role: models.RoleAdmin,
			seed: func(t *testing.T, id int) { env.riskKey(t, id, "", nil) }},
		{name: "only a waiting device key", localOn: true,
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusPending, nil) }},
		{name: "a password", localOn: true, pw: true,
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "a password only", localOn: true, pw: true, seed: func(*testing.T, int) {}},
		{name: "a provider only", localOn: true, seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, directory, "risk-provider-only")
		}},
		{name: "a password and a key bound to a provider that signs people in", localOn: true, pw: true, seed: func(t *testing.T, id int) {
			identity := env.riskIdentity(t, id, directory, "risk-pw-bound-usable")
			env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
		}},
		// Local passwords off would leave its key, which needs no local
		// password, as its only way in.
		{name: "a password and a key bound to a provider that can't sign people in", localOn: true, pw: true, want: listed{false, true},
			seed: func(t *testing.T, id int) {
				identity := env.riskIdentity(t, id, oidc, "risk-pw-bound-unusable")
				env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
			}},
		{name: "an administrator with a password and a key", localOn: true, role: models.RoleAdmin, pw: true,
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "a directory identity", localOn: true, seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, directory, "risk-directory-identity")
			env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
		}},
		{name: "an OAuth identity with a public URL", localOn: true, publicURL: "https://media.example.test/", seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, oidc, "risk-oidc-url")
			env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
		}},
		{name: "an OAuth identity, no public URL: OAuth signs nobody in", localOn: true, want: listed{true, true}, seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, oidc, "risk-oidc-nourl")
			env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
		}},
		{name: "a network identity: signs in only on its network", localOn: true, publicURL: "https://media.example.test", want: listed{true, true},
			seed: func(t *testing.T, id int) {
				env.riskIdentity(t, id, network, "risk-network")
				env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
			}},
		{name: "provider-bound key, its provider's sign-in off", localOn: true, want: listed{true, true}, seed: func(t *testing.T, id int) {
			identity := env.riskIdentity(t, id, env.installationID, "risk-off")
			env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
		}},
		{name: "local passwords already off: its key doesn't work now", want: listed{false, true},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "break-glass with local passwords off", role: models.RoleAdmin, bg: true, want: listed{true, true},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "break-glass with a password", localOn: true, role: models.RoleAdmin, pw: true, bg: true,
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := fmt.Sprintf("risk-%d", i)
			env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, map[bool]string{true: "true", false: "false"}[tc.localOn])
			env.setSetting(t, config.ServerPublicURLSettingKey, tc.publicURL)
			role := tc.role
			if role == "" {
				role = models.RoleUser
			}
			id := env.riskAccount(t, label, role, tc.pw, tc.bg)
			tc.seed(t, id)
			appKeys, err := AccountsAtRisk(ctx, env.pool, ChangeAppKeysOff)
			if err != nil {
				t.Fatal(err)
			}
			local, err := AccountsAtRisk(ctx, env.pool, ChangeLocalPasswordsOff)
			if err != nil {
				t.Fatal(err)
			}
			if got := (listed{listsAccount(appKeys, id), listsAccount(local, id)}); got != tc.want {
				t.Fatalf("listed = %+v, want %+v", got, tc.want)
			}
			if _, err := env.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Turning local passwords off is refused while an account holds keys and has
// no usable password and no provider identity, and the refusal names it and
// offers only linking a provider. Setting break-glass on such an account
// while local passwords are off is refused too: its keys would start to
// work as its only way in.
func TestLocalLoginOffRefusesKeysOnlyDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	env.addUsableProvider(t)
	ctx := t.Context()
	env.riskAccount(t, "keysonly-glass", models.RoleAdmin, true, true)
	keysOnly := env.riskAccount(t, "keysonly-member", models.RoleUser, false, false)
	env.riskKey(t, keysOnly, models.DeviceKeyStatusActive, nil)

	err := CheckLocalLoginSettingChange(ctx, env.pool, LocalLoginSettings{LocalPasswordLogin: "true"}, LocalLoginSettings{LocalPasswordLogin: "false"})
	risk, ok := errors.AsType[*AccountsAtRiskError](err)
	if !ok || !errors.Is(err, ErrLastSignInMethod) || !listsAccount(risk.Accounts, keysOnly) {
		t.Fatalf("turning local passwords off = %v, want the keys-only account named", err)
	}
	detail := risk.Detail()
	if !strings.Contains(detail, env.name("keysonly-member")) || !strings.Contains(detail, "sign-in provider") ||
		strings.Contains(detail, "Give each a password") {
		t.Fatalf("detail = %q", detail)
	}
	// A password clears the account from the list.
	if _, err := env.pool.Exec(ctx, `UPDATE users SET password_hash = $2, local_password_login_enabled = true WHERE id = $1`,
		keysOnly, externalSignInPasswordHash); err != nil {
		t.Fatal(err)
	}
	if err := CheckLocalLoginSettingChange(ctx, env.pool, LocalLoginSettings{LocalPasswordLogin: "true"}, LocalLoginSettings{LocalPasswordLogin: "false"}); listsAccountErr(err, keysOnly) {
		t.Fatalf("with a password = %v", err)
	}

	// The reverse edge, with local passwords off.
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "false")
	admin := env.riskAccount(t, "keysonly-admin", models.RoleAdmin, false, false)
	env.riskKey(t, admin, models.DeviceKeyStatusActive, nil)
	users := NewUserRepository(env.pool)
	current, err := users.GetByID(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		input models.UpdateUserInput
		want  error
	}{
		"set break-glass":              {models.UpdateUserInput{BreakGlass: new(true)}, ErrKeysOnlyWayIn},
		"set break-glass and password": {models.UpdateUserInput{BreakGlass: new(true), Password: new("pw"), LocalPasswordLoginEnabled: new(true)}, nil},
		"unrelated edit, not set":      {models.UpdateUserInput{MaxProfiles: models.SetValue(3)}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
				return EnsureKeysNotOnlyWayInAfterAdminChange(ctx, tx, current, &tc.input)
			})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func listsAccountErr(err error, id int) bool {
	risk, ok := errors.AsType[*AccountsAtRiskError](err)
	return ok && listsAccount(risk.Accounts, id)
}

// A person can't unlink their only provider account while they hold keys
// and have no password: keys don't count as a way in there (unlinking
// revokes the keys bound to it, and local keys need local passwords).
func TestUnlinkOnlyProviderWithKeysRefusedDB(t *testing.T) {
	env := newKeyEnv(t, "unlinkkeys")
	ctx := t.Context()
	env.enrollFromProvider(t)
	if err := NewIdentityService(env.pool).UnlinkOwn(ctx, env.user.ID, env.identityID); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("unlinking the only provider account = %v, want ErrLastSignInMethod", err)
	}
}

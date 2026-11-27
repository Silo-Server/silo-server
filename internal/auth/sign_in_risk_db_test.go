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

// The truth table for the three sign-in writes, one row per kind of account:
// what turning app keys off, turning local passwords off and removing the
// public URL each do to it. App keys off and the public URL either leave the
// account alone or refuse the write naming it. Local passwords off leaves it
// alone, warns (it loses password sign-in, and the write is allowed), or is
// blocked by it (it would be left with keys as its only way in, or none, and
// the write is refused). The local-passwords answer is read from
// listAdminLocalPasswordOffAccounts' groups, and the refusal's list must name
// exactly the accounts in its blocking group.
// The outcomes a truth-table row expects of each write.
const (
	truthAlone  = "unaffected"
	truthWarned = "warned"
	truthBlocks = "blocks"
)

// truthOutcome is what the three writes do to one account.
type truthOutcome struct {
	appKeysOff bool
	localOff   string
	urlRemoved bool
}

// truthRow is one kind of account in TestAccountsAtRiskDB.
type truthRow struct {
	name    string
	localOn bool
	seed    func(t *testing.T, id int)
	role    string
	pw, bg  bool
	// publicURL is server.public_url; OAuth sign-in needs one.
	publicURL string
	disabled  bool
	want      truthOutcome
}

func TestAccountsAtRiskDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, env.pool, config.ServerPublicURLSettingKey))
	// Providers whose sign-in binding is on, of each kind. env's own
	// installation has its binding off.
	directory := env.riskProvider(t, "directory", `["password"]`)
	oidc := env.riskProvider(t, "oidc", `["oauth2"]`)
	network := env.riskProvider(t, "network", `["network"]`)
	identities := NewIdentityService(env.pool)
	for i, tc := range truthRows(env, directory, oidc, network) {

		t.Run(tc.name, func(t *testing.T) {
			label := fmt.Sprintf("risk-%d", i)
			local := map[bool]string{true: "true", false: "false"}[tc.localOn]
			env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, local)
			env.setSetting(t, config.ServerPublicURLSettingKey, tc.publicURL)
			role := tc.role
			if role == "" {
				role = models.RoleUser
			}
			id := env.riskAccount(t, label, role, tc.pw, tc.bg)
			tc.seed(t, id)
			if tc.disabled {
				if _, err := env.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, id); err != nil {
					t.Fatal(err)
				}
			}
			got := observeTruth(t, env, identities, id, local, tc.publicURL)
			if got != tc.want {
				t.Fatalf("outcome = %+v, want %+v", got, tc.want)
			}
			if _, err := env.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// observeTruth reads what the three writes would do to account id: the app
// keys refusal's list, the local-passwords list's groups (checked against
// the refusal's own list), and a public URL removal's refusal when one is
// set.
func observeTruth(t *testing.T, env *externalSignInEnv, identities *IdentityService, id int, local, publicURL string) truthOutcome {
	t.Helper()
	ctx := t.Context()
	appKeys, err := AccountsAtRisk(ctx, env.pool, ChangeAppKeysOff)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := AccountsAtRisk(ctx, env.pool, ChangeLocalPasswordsOff)
	if err != nil {
		t.Fatal(err)
	}
	group := localPasswordOffGroupOf(t, identities, id)
	if blocking := group == LocalPasswordOffBlocking; listsAccount(refused, id) != blocking {
		t.Fatalf("the refusal names it = %v, but its group is %q: the page and the refusal disagree", !blocking, group)
	}
	got := truthOutcome{appKeysOff: listsAccount(appKeys, id), localOff: map[string]string{
		"": truthAlone, LocalPasswordOffEmailMatch: truthWarned, LocalPasswordOffAdminLink: truthWarned, LocalPasswordOffBlocking: truthBlocks,
	}[group]}
	if publicURL != "" {
		err := pgx.BeginFunc(ctx, env.pool, func(tx pgx.Tx) error {
			return CheckPublicURLChange(ctx, tx, SignInSettingsOf(local, publicURL), SignInSettingsOf(local, ""))
		})
		got.urlRemoved = listsAccountErr(err, id)
	}
	return got
}

// truthRows are the kinds of account TestAccountsAtRiskDB seeds, with what
// each write does to them.
func truthRows(env *externalSignInEnv, directory, oidc, network int) []truthRow {
	const (
		alone  = truthAlone
		warned = truthWarned
		blocks = truthBlocks
	)
	type outcome = truthOutcome
	return []truthRow{
		{name: "passwordless with a device key", localOn: true, want: outcome{true, blocks, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "passwordless with only a person key", localOn: true, want: outcome{true, blocks, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, "", nil) }},
		{name: "admin with only a person key: a new device waits", localOn: true, role: models.RoleAdmin, want: outcome{false, alone, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, "", nil) }},
		{name: "only a waiting device key", localOn: true, want: outcome{false, alone, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusPending, nil) }},
		// It loses its password and its local key with it, as an account
		// with only a password always has: warned, not refused.
		{name: "a password and a local key", localOn: true, pw: true, want: outcome{false, warned, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "a password only", localOn: true, pw: true, want: outcome{false, warned, false}, seed: func(*testing.T, int) {}},
		{name: "a provider only", localOn: true, want: outcome{false, alone, false}, seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, directory, "risk-provider-only")
		}},
		{name: "an OAuth provider only, with a public URL", localOn: true, publicURL: "https://media.example.test/", want: outcome{false, alone, true},
			seed: func(t *testing.T, id int) { env.riskIdentity(t, id, oidc, "risk-oidc-only") }},
		{name: "a password and a key bound to a provider that signs people in", localOn: true, pw: true, want: outcome{false, alone, false},
			seed: func(t *testing.T, id int) {
				identity := env.riskIdentity(t, id, directory, "risk-pw-bound-usable")
				env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
			}},
		// Local passwords off would leave its key, which needs no local
		// password, as its only way in: main's list warned it, and it blocks.
		{name: "a password and a key bound to a provider that can't sign people in", localOn: true, pw: true, want: outcome{false, blocks, false},
			seed: func(t *testing.T, id int) {
				identity := env.riskIdentity(t, id, oidc, "risk-pw-bound-unusable")
				env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
			}},
		{name: "keys only, bound to a provider that signs people in", localOn: true, want: outcome{false, alone, false},
			seed: func(t *testing.T, id int) {
				identity := env.riskIdentity(t, id, directory, "risk-keys-bound-usable")
				env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
			}},
		{name: "an administrator with a password and a key", localOn: true, role: models.RoleAdmin, pw: true, want: outcome{false, warned, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "a directory identity", localOn: true, want: outcome{false, alone, false}, seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, directory, "risk-directory-identity")
			env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
		}},
		{name: "an OAuth identity with a public URL", localOn: true, publicURL: "https://media.example.test/", want: outcome{false, alone, true},
			seed: func(t *testing.T, id int) {
				env.riskIdentity(t, id, oidc, "risk-oidc-url")
				env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
			}},
		{name: "an OAuth identity, no public URL: OAuth signs nobody in", localOn: true, want: outcome{true, blocks, false}, seed: func(t *testing.T, id int) {
			env.riskIdentity(t, id, oidc, "risk-oidc-nourl")
			env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
		}},
		{name: "a network identity: signs in only on its network", localOn: true, publicURL: "https://media.example.test", want: outcome{true, blocks, false},
			seed: func(t *testing.T, id int) {
				env.riskIdentity(t, id, network, "risk-network")
				env.riskKey(t, id, models.DeviceKeyStatusActive, nil)
			}},
		{name: "provider-bound key, its provider's sign-in off", localOn: true, want: outcome{true, blocks, false}, seed: func(t *testing.T, id int) {
			identity := env.riskIdentity(t, id, env.installationID, "risk-off")
			env.riskKey(t, id, models.DeviceKeyStatusActive, &identity)
		}},
		{name: "local passwords already off: its key doesn't work now", want: outcome{false, blocks, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "break-glass with local passwords off", role: models.RoleAdmin, bg: true, want: outcome{true, blocks, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "break-glass with a password", localOn: true, role: models.RoleAdmin, pw: true, bg: true, want: outcome{false, alone, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
		{name: "disabled, with no way in but a key", localOn: true, disabled: true, want: outcome{false, alone, false},
			seed: func(t *testing.T, id int) { env.riskKey(t, id, models.DeviceKeyStatusActive, nil) }},
	}
}

// localPasswordOffGroupOf answers which group of
// listAdminLocalPasswordOffAccounts holds the account, or "" for none.
func localPasswordOffGroupOf(t *testing.T, identities *IdentityService, id int) string {
	t.Helper()
	for _, group := range []string{LocalPasswordOffEmailMatch, LocalPasswordOffAdminLink, LocalPasswordOffBlocking} {
		query := LocalPasswordOffQuery{Group: group, Limit: 500}
		for {
			page, err := identities.LocalPasswordOffAccounts(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range page.Accounts {
				if a.ID == id {
					return group
				}
			}
			if !page.More || len(page.Accounts) == 0 {
				break
			}
			last := page.Accounts[len(page.Accounts)-1]
			query.AfterUsername, query.AfterID = last.Username, last.ID
		}
	}
	return ""
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

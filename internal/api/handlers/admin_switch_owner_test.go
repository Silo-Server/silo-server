package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/serversettings"
)

// restoreSetting saves key and restores it when t ends.
func (f *switchWriteFixture) restoreSetting(t *testing.T, key string) {
	t.Helper()
	saved := storedSettingValue(t, f.pool, key)
	t.Cleanup(func() {
		if saved == nil {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
		} else {
			_, _ = f.pool.Exec(context.Background(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, *saved)
		}
	})
}

func (f *switchWriteFixture) store(t *testing.T, key, value string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatal(err)
	}
}

// oauthProvider seeds an enabled installation whose sign-in binding is an
// OAuth one, and removes it when t ends.
func (f *switchWriteFixture) oauthProvider(t *testing.T) int {
	t.Helper()
	var id int
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/oauth', true, 'manual', 'plugin') RETURNING id`, "oauth-"+f.suffix).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id) })
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'oidc', '{"auth_modes": ["oauth2"]}')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO plugin_auth_bindings (plugin_installation_id, capability_id, enabled) VALUES ($1, 'oidc', true)`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// The server's public address is a setting an OAuth provider depends on, so
// no write that changes it may leave an account with keys as its only way in
// or with none: the app keys check judges the address the same write leaves,
// and a write that only changes the address is refused while an account
// would lose the only provider it can sign in with and has no password. A
// move to another valid address is saved.
func TestPublicURLWritesKeepAWayInDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	existing, err := h.AccountsAtRisk(ctx, auth.ChangeAppKeysOff)
	if err != nil {
		t.Fatalf("reading the accounts at risk: %v", err)
	}
	if len(existing) > 0 {
		t.Skipf("%d accounts that sign in only with keys already in the database", len(existing))
	}
	f.restoreSetting(t, config.ServerPublicURLSettingKey)
	provider := f.oauthProvider(t)
	const address = "https://media.example.test"
	for i, tc := range []struct {
		name          string
		storedAddress string
		keysOff       bool // app keys stored off
		write         map[string]string
		refused       bool
	}{
		{name: "keys off and the address cleared together", storedAddress: address, refused: true,
			write: map[string]string{config.AuthAppKeySignInSettingKey: "false", config.ServerPublicURLSettingKey: ""}},
		{name: "keys off and an address supplied together",
			write: map[string]string{config.AuthAppKeySignInSettingKey: "false", config.ServerPublicURLSettingKey: address}},
		{name: "keys on, the address cleared", storedAddress: address, refused: true,
			write: map[string]string{config.ServerPublicURLSettingKey: ""}},
		{name: "keys already off, the address cleared", storedAddress: address, keysOff: true, refused: true,
			write: map[string]string{config.ServerPublicURLSettingKey: ""}},
		{name: "the address moved to another valid one", storedAddress: address,
			write: map[string]string{config.ServerPublicURLSettingKey: "https://silo.example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`,
				append(switchSettingKeys, config.ServerPublicURLSettingKey)); err != nil {
				t.Fatal(err)
			}
			if tc.storedAddress != "" {
				f.store(t, config.ServerPublicURLSettingKey, tc.storedAddress)
			}
			if tc.keysOff {
				f.store(t, config.AuthAppKeySignInSettingKey, "false")
			}
			// An account with no password, a live key, and an identity at
			// the OAuth provider.
			label := "oauthkeys" + strconv.Itoa(i)
			userID := f.account(t, label, false)
			f.deviceKey(t, userID, "device", "app_code")
			if _, err := f.pool.Exec(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id, issuer, linked_at)
				VALUES ($1, $2, $3, 'https://idp.example.test', NOW())`, provider, label+f.suffix, userID); err != nil {
				t.Fatal(err)
			}
			_, err := h.UpdateAdminSettings(ctx, tc.write, nil)
			if !tc.refused {
				if err != nil {
					t.Fatalf("write = %v, want it saved", err)
				}
				return
			}
			requireAdminError(t, err, 409, "last_sign_in_method")
			if !strings.Contains(err.Error(), label+"-"+f.suffix) {
				t.Fatalf("refusal %v does not name the account", err)
			}
		})
	}
}

// While the Owner holds a live key session, only the Owner may turn app keys
// off, or demo mode on, through the settings write: the sweep would end the
// Owner's sessions, which no other administrator may do (owner_protected).
// The sequence: a non-Owner administrator turns local passwords off, then
// app keys off.
func TestSwitchOffKeepsOwnerProtectionDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	f.restoreSetting(t, config.AuthLocalPasswordLoginSettingKey)
	// The one Owner: an existing one steps aside for the test.
	var formerOwner *int
	_ = f.pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&formerOwner)
	if formerOwner != nil {
		if _, err := f.pool.Exec(ctx, `UPDATE users SET is_owner = false WHERE id = $1`, *formerOwner); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `UPDATE users SET is_owner = true WHERE id = $1`, *formerOwner)
		})
	}
	admin := func(label string, owner, breakGlass bool) int {
		id := f.account(t, label, true)
		if _, err := f.pool.Exec(ctx, `UPDATE users SET role = 'admin', is_owner = $2, break_glass = $3 WHERE id = $1`, id, owner, breakGlass); err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := admin("owner", true, false)
	admin("glass", false, true)
	other := admin("other", false, false)
	ownerKey := f.deviceKey(t, owner, "device", "session")
	as := func(id int) context.Context {
		return apimw.SetClaims(ctx, &auth.Claims{UserID: id, Role: "admin", SessionID: uuid.NewString(), TokenType: auth.TokenTypeAccess})
	}
	for _, off := range []struct{ name, key, value string }{
		{"app keys off", config.AuthAppKeySignInSettingKey, "false"},
		{"demo mode on", demomode.SettingKey, demomode.OnValue},
	} {
		for _, tc := range []struct {
			name         string
			actor        *int
			ownerSession bool
			refused      bool
		}{
			{name: "by another administrator", actor: &other, ownerSession: true, refused: true},
			{name: "by the Owner", actor: &owner, ownerSession: true},
			{name: "by another administrator, the Owner holding no key session", actor: &other},
		} {
			t.Run(off.name+" "+tc.name, func(t *testing.T) {
				if _, err := f.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`,
					append(switchSettingKeys, config.AuthLocalPasswordLoginSettingKey)); err != nil {
					t.Fatal(err)
				}
				var session string
				if tc.ownerSession {
					id := uuid.NewString()
					if _, err := f.pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
						VALUES ($1, $2, 'phone', NOW() + interval '1 hour', 'app_key', $3)`, id, owner, ownerKey); err != nil {
						t.Fatal(err)
					}
					session = id
				}
				// Step 1: local passwords off, by the other administrator.
				if _, err := h.UpdateAdminSettings(as(other), map[string]string{config.AuthLocalPasswordLoginSettingKey: "false"}, nil); err != nil {
					t.Fatalf("local passwords off = %v", err)
				}
				// Step 2: the switch off.
				_, err := h.UpdateAdminSettings(as(*tc.actor), map[string]string{off.key: off.value}, nil)
				if tc.refused {
					requireAdminError(t, err, 403, codeOwnerProtected)
					if !f.live(t, session) {
						t.Fatal("a refused write ended the Owner's session")
					}
					return
				}
				if err != nil {
					t.Fatalf("switch off = %v, want it saved", err)
				}
				if session != "" && f.live(t, session) {
					t.Fatal("the Owner's key session is live after the Owner turned the switch off")
				}
			})
		}
	}
}

// Turning local passwords off judges the state it leaves: an account with a
// password and a key bound to a provider that can't sign people in (an
// OAuth provider on a server with no public address) would keep only its
// key, which needs no local password, so the write is refused naming it.
func TestLocalPasswordsOffJudgesTheStateLeftDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	f.restoreSetting(t, config.AuthLocalPasswordLoginSettingKey)
	f.restoreSetting(t, config.ServerPublicURLSettingKey)
	if _, err := f.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`,
		[]string{config.AuthLocalPasswordLoginSettingKey, config.ServerPublicURLSettingKey}); err != nil {
		t.Fatal(err)
	}
	glass := f.account(t, "glass-local", true)
	if _, err := f.pool.Exec(ctx, `UPDATE users SET role = 'admin', break_glass = true WHERE id = $1`, glass); err != nil {
		t.Fatal(err)
	}
	provider := f.oauthProvider(t)
	userID := f.account(t, "pwbound", true)
	var identity int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id, issuer, linked_at)
		VALUES ($1, $2, $3, 'https://idp.example.test', NOW()) RETURNING id`, provider, "pwbound"+f.suffix, userID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	key := f.deviceKey(t, userID, "device", "session")
	if _, err := f.pool.Exec(ctx, `UPDATE auth_device_keys SET identity_id = $2, provider_bound = true WHERE id = $1`, key, identity); err != nil {
		t.Fatal(err)
	}
	_, err := h.UpdateAdminSettings(ctx, map[string]string{config.AuthLocalPasswordLoginSettingKey: "false"}, nil)
	requireAdminError(t, err, 409, "last_sign_in_method")
	if !strings.Contains(err.Error(), "pwbound-"+f.suffix) {
		t.Fatalf("refusal %v does not name the account", err)
	}
}

// blockedBy waits until a backend of this database waits on a lock the
// backend holder holds, and answers its pid; a party that answers on
// returned first fails the test at once with its answer.
func (f *switchWriteFixture) blockedBy(t *testing.T, holder uint32, returned <-chan error) uint32 {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var pid uint32
		err := f.pool.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity
			WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`, holder).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing waited on backend %d", holder)
		}
		select {
		case err := <-returned:
			t.Fatalf("a party returned before it waited: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Moving ownership and turning app keys off read ownership under one lock,
// so in either order the Owner check sees ownership before or after the
// move, never in between, and neither deadlocks.
func TestOwnershipMoveAgainstSwitchOffDB(t *testing.T) {
	f := newSwitchWriteFixture(t)
	ctx := t.Context()
	h := &AdminHandler{SettingsRepo: serversettings.NewServerSettingsRepo(f.pool), pool: f.pool}
	var formerOwner *int
	_ = f.pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&formerOwner)
	if formerOwner != nil {
		if _, err := f.pool.Exec(ctx, `UPDATE users SET is_owner = false WHERE id = $1`, *formerOwner); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `UPDATE users SET is_owner = true WHERE id = $1`, *formerOwner)
		})
	}
	users := auth.NewUserRepository(f.pool)
	for i, transferFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "the move first", false: "the switch-off first"}[transferFirst], func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, switchSettingKeys); err != nil {
				t.Fatal(err)
			}
			admin := func(label string, owner bool) int {
				id := f.account(t, label+strconv.Itoa(i), true)
				if _, err := f.pool.Exec(ctx, `UPDATE users SET role = 'admin', is_owner = $2 WHERE id = $1`, id, owner); err != nil {
					t.Fatal(err)
				}
				return id
			}
			owner, next, other := admin("from", true), admin("to", false), admin("other", false)
			t.Cleanup(func() {
				_, _ = f.pool.Exec(context.Background(), `UPDATE users SET is_owner = false WHERE id = ANY($1)`, []int{owner, next})
			})
			ownerKey := f.deviceKey(t, owner, "device", "session")
			session := uuid.NewString()
			if _, err := f.pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
				VALUES ($1, $2, 'phone', NOW() + interval '1 hour', 'app_key', $3)`, session, owner, ownerKey); err != nil {
				t.Fatal(err)
			}
			hold, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
			deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			move := func() error { return users.TransferOwnership(deadline, owner, next) }
			// The move first: it holds the ownership lock and waits on the
			// new Owner's row, and the switch-off (by another administrator)
			// waits on the ownership lock; afterwards the new Owner holds no
			// key session, so it is allowed. The switch-off first (by the
			// Owner): it holds the ownership lock and waits on the Owner's
			// key session, and the move waits on the ownership lock.
			actor, row := other, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`
			holdID := any(next)
			if !transferFirst {
				actor, row, holdID = owner, `SELECT 1 FROM auth_sessions WHERE id = $1 FOR UPDATE`, session
			}
			if _, err := hold.Exec(ctx, row, holdID); err != nil {
				t.Fatal(err)
			}
			switchOff := func() error {
				_, err := h.UpdateAdminSettings(apimw.SetClaims(deadline, &auth.Claims{UserID: actor, Role: "admin", TokenType: auth.TokenTypeAccess}),
					map[string]string{config.AuthAppKeySignInSettingKey: "false"}, nil)
				return err
			}
			first, second := move, switchOff
			if !transferFirst {
				first, second = switchOff, move
			}
			var holder uint32
			if err := hold.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
				t.Fatal(err)
			}
			// The first party waits on the held row, the second on the
			// first (the ownership lock it holds).
			done := make(chan error, 2)
			go func() { done <- first() }()
			firstPID := f.blockedBy(t, holder, done)
			go func() { done <- second() }()
			f.blockedBy(t, firstPID, done)
			if err := hold.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := <-done; err != nil {
					t.Fatalf("one side failed: %v", err)
				}
			}
			if f.live(t, session) {
				t.Fatal("the key session is live after the switch went off")
			}
		})
	}
}

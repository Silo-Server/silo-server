package auth

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// A key an account creates for itself exists only while the server allows
// user keys, carries user scopes only, and may bind only a profile of its own
// account. The binding then narrows what a profile lists and revokes, and
// deleting the profile deletes its keys.
func TestCreatePersonalAPIKeyDB(t *testing.T) {
	d := newAuditActionsDB(t)
	ctx := t.Context()
	t.Cleanup(saveSettings(t, d.pool, config.AuthUserAPIKeysSettingKey))
	setUserKeys := func(value string) {
		t.Helper()
		if _, err := d.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthUserAPIKeysSettingKey, value); err != nil {
			t.Fatal(err)
		}
	}
	account, other := d.account(t, "user", false), d.account(t, "user", false)
	for user, profile := range map[int]string{account: "kid-" + d.suffix, other: "other-" + d.suffix} {
		if _, err := d.pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'Kid')`, profile, user); err != nil {
			t.Fatal(err)
		}
	}
	kid, foreign := "kid-"+d.suffix, "other-"+d.suffix
	watch := []string{ScopeWatchRead}
	acting := APIKeyActingProfile{ID: kid}

	setUserKeys("false")
	if _, err := d.keys.CreatePersonal(ctx, account, acting, kid, "Sync", watch); !errors.Is(err, ErrUserAPIKeysDisabled) {
		t.Fatalf("setting off: %v", err)
	}
	setUserKeys("true")
	for name, scopes := range map[string][]string{"unscoped": nil, "admin scope": {ScopeAdminUsers}, "mixed": {ScopeWatchRead, ScopeAdminMonitorRead}} {
		if _, err := d.keys.CreatePersonal(ctx, account, acting, kid, "Sync", scopes); !errors.Is(err, ErrAPIKeyUserScopesRequired) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := d.keys.CreatePersonal(ctx, account, acting, foreign, "Sync", watch); !errors.Is(err, ErrAPIKeyProfileNotFound) {
		t.Fatalf("another account's profile: %v", err)
	}

	// A PIN change after the profile was verified ends its proof.
	if _, err := d.keys.CreatePersonal(ctx, account, APIKeyActingProfile{ID: kid, PINRevision: 9}, kid, "Sync", watch); !errors.Is(err, ErrAPIKeyActingProfileChanged) {
		t.Fatalf("stale PIN revision: %v", err)
	}

	bound, err := d.keys.CreatePersonal(ctx, account, acting, kid, "Sync", watch)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := d.keys.CreatePersonal(ctx, account, acting, "", "Everything", watch)
	if err != nil {
		t.Fatal(err)
	}
	read, err := d.keys.GetByKey(ctx, bound.Key)
	if err != nil || read.ProfileID != kid {
		t.Fatalf("authentication read lost the binding: %+v %v", read, err)
	}
	if read, err := d.keys.GetByKey(ctx, wide.Key); err != nil || read.ProfileID != "" {
		t.Fatalf("account key gained a binding: %+v %v", read, err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE api_keys SET profile_id = NULL WHERE id = $1`, bound.ID); err == nil {
		t.Fatal("a bound key was unbound in place")
	}

	rows, _, err := d.keys.ListByUserProfilePage(ctx, account, kid, nil, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != bound.ID {
		t.Fatalf("profile listing = %+v, %v", rows, err)
	}
	if err := d.keys.DeleteForProfile(ctx, wide.ID, account, kid); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("a profile revoked a key that is not its own: %v", err)
	}

	if _, err := d.pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1 AND id = $2`, account, kid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.keys.GetByKey(ctx, bound.Key); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("key outlived its profile: %v", err)
	}
	if _, err := d.keys.GetByKey(ctx, wide.Key); err != nil {
		t.Fatalf("account key did not survive a profile deletion: %v", err)
	}
}

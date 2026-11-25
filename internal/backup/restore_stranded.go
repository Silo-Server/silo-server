package backup

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// strandedSentence heads the list of accounts a full restore leaves with no
// way to sign in.
const strandedSentence = "These accounts have no way to sign in on the restored server; give each a password or link a sign-in provider account for it:"

// strandedAccounts selects the enabled accounts that hold an app key but
// have no way in under the two switches ($1 auth.app_key_sign_in, $2
// auth.local_password_login): no sign-in link, no password that signs in,
// and no key that does. It mirrors internal/auth, so the two can be
// compared:
//   - a password signs in when the account has one and its own local
//     sign-in is on (checkLocalPassword), and the server allows local
//     passwords or the account is break-glass (localPasswordPolicy);
//   - a key that is not provider-bound signs in while key sign-in is on,
//     under the same localPasswordPolicy (keySignInPolicy): an active device
//     key opens a session, and so does a person key introducing a device,
//     except on an administrator's account, where the new key waits for
//     approval;
//   - a provider-bound key needs its sign-in link, and an account with a
//     link is not listed (the restore has revoked the keys whose link is
//     gone). A network provider's refusal (networkRefused) needs a link too.
const strandedAccounts = `
SELECT format('%s (account %s)', coalesce(u.username::text, u.email::text, ''), u.id)
FROM users u
WHERE u.enabled
	AND (EXISTS (SELECT 1 FROM auth_person_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL)
		OR EXISTS (SELECT 1 FROM auth_device_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL))
	AND NOT EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = u.id)
	AND NOT (coalesce(u.password_hash, '') <> '' AND u.local_password_login_enabled AND ($2 OR u.break_glass))
	AND NOT ($1 AND ($2 OR u.break_glass) AND (
		EXISTS (SELECT 1 FROM auth_device_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL
			AND k.status = 'active' AND NOT k.provider_bound)
		OR (u.role <> 'admin' AND EXISTS (SELECT 1 FROM auth_person_keys k WHERE k.user_id = u.id
			AND k.revoked_at IS NULL AND NOT k.provider_bound))))
ORDER BY u.id`

// StrandedAccounts lists the accounts a restored database leaves with no
// way to sign in (strandedAccounts), by name and id. While both switches
// are on every listed key signs in, so it lists none; a backup that
// predates app keys has none to list.
func (l PGLoader) StrandedAccounts(ctx context.Context, dbURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var keys bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.auth_person_keys') IS NOT NULL AND to_regclass('public.auth_device_keys') IS NOT NULL`).Scan(&keys); err != nil || !keys {
		return nil, err
	}
	settings, err := settingRows(ctx, conn, []string{config.AuthAppKeySignInSettingKey, config.AuthLocalPasswordLoginSettingKey})
	if err != nil {
		return nil, err
	}
	// As internal/auth reads them: a value that doesn't parse is the
	// default, on.
	on := func(key string) bool {
		v, err := strconv.ParseBool(strings.TrimSpace(settings[key]))
		return err != nil || v
	}
	keySignIn, localPasswords := on(config.AuthAppKeySignInSettingKey), on(config.AuthLocalPasswordLoginSettingKey)
	if keySignIn && localPasswords {
		return nil, nil
	}
	return collectStrings(ctx, conn, strandedAccounts, keySignIn, localPasswords)
}

// reportStranded prints the accounts StrandedAccounts lists, or nothing. It
// changes nothing and never fails the restore: an owner fixes each account
// after Silo starts.
func reportStranded(ctx context.Context, loader DatabaseLoader, dbURL string, out io.Writer) {
	stranded, err := loader.StrandedAccounts(ctx, dbURL)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Could not list the accounts left with no way to sign in: %v\n", err)
		return
	}
	if len(stranded) > 0 {
		_, _ = fmt.Fprintf(out, "%s %s.\n", strandedSentence, strings.Join(stranded, ", "))
	}
}

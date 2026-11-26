package backup

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
)

// strandedSentence follows the count of accounts a full restore leaves with
// no way to sign in, and heads their names.
const strandedSentence = "have no way to sign in on the restored server; give each a password or link a sign-in provider account for it:"

// strandedAccounts selects the enabled accounts the restored database
// leaves with no way in under its two switches: no provider identity it can
// sign in with, no password that signs in, and no key that does
// (auth.AccountHasNoWayInSQL, the rule the sign-in switch checks use), with
// $1 auth.local_password_login and $2 the app key switch. Keys the restore
// revoked or left out are revoked or absent in the restored rows, so they
// count as gone. A database without the key tables (a backup from before
// them) uses the same rule without keys, and takes only $1.
func strandedAccounts(keyTables bool) string {
	noWayIn := auth.AccountHasNoWayInWithoutKeysSQL("$1")
	if keyTables {
		noWayIn = auth.AccountHasNoWayInSQL("$2", "$1")
	}
	return `SELECT format('%s (account %s)', coalesce(u.username::text, u.email::text, ''), u.id)
		FROM users u WHERE u.enabled AND ` + noWayIn + ` ORDER BY u.id`
}

// StrandedAccounts lists by name and id every enabled account the restored
// database leaves with no way to sign in (strandedAccounts), or none.
func (l PGLoader) StrandedAccounts(ctx context.Context, dbURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	settings, err := settingRows(ctx, conn, []string{config.AuthAppKeySignInSettingKey, config.AuthLocalPasswordLoginSettingKey, demomode.SettingKey})
	if err != nil {
		return nil, err
	}
	// As internal/auth reads them: a value that doesn't parse is the
	// default, on, and app keys are off in demo mode (auth.AppKeySwitchOn).
	local, err := strconv.ParseBool(strings.TrimSpace(settings[config.AuthLocalPasswordLoginSettingKey]))
	args := []any{err != nil || local}
	person, err := tableExists(ctx, conn, tableAuthPersonKeys)
	if err != nil {
		return nil, err
	}
	device, err := tableExists(ctx, conn, tableAuthDeviceKeys)
	if err != nil {
		return nil, err
	}
	if person && device {
		args = append(args, auth.AppKeySwitchOn(strings.TrimSpace(settings[config.AuthAppKeySignInSettingKey]), settings[demomode.SettingKey]))
	}
	return collectStrings(ctx, conn, strandedAccounts(person && device), args...)
}

// reportStranded prints how many accounts StrandedAccounts lists and the
// first maxListedAccounts of them (listNames), or nothing. It changes nothing and
// never fails the restore: an owner fixes each account after Silo starts.
func reportStranded(ctx context.Context, loader DatabaseLoader, dbURL string, out io.Writer) {
	stranded, err := loader.StrandedAccounts(ctx, dbURL)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Could not list the accounts left with no way to sign in: %v\n", err)
		return
	}
	if len(stranded) > 0 {
		_, _ = fmt.Fprintf(out, "%d accounts %s %s.\n", len(stranded), strandedSentence, listNames(stranded))
	}
}

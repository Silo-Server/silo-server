package backup

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AppKeyCarry is what a restore does with the backup's app keys.
type AppKeyCarry int

const (
	// AppKeysLeftOut deletes them: a separate restore's copy, or a full
	// restore that could not carry the live server's credentials, keeps none.
	AppKeysLeftOut AppKeyCarry = iota
	// AppKeysKept keeps those the live server lacks (restoredStay): a full
	// restore onto the deployment the backup came from, or onto a server
	// that takes its identity.
	AppKeysKept
	// AppKeysRevoked keeps them revoked, so their public keys stay enrolled
	// once: a full restore of another deployment's backup onto a server that
	// keeps its own identity, which the keys' signed requests do not name.
	AppKeysRevoked
)

// carriedFor is t as a restore that does keys with app keys carries it: one
// that keeps no app keys deletes a restoredStay table's restored rows too.
func (t secretTable) carriedFor(keys AppKeyCarry) secretTable {
	if keys != AppKeysLeftOut || !t.carry.restoredStay {
		return t
	}
	spec := *t.carry
	spec.restoredStay = false
	t.carry = &spec
	return t
}

// deleteUncarried deletes the restored credentials that are not the same
// as a live one the carry writes (owned), none with restoredStay, and
// returns how many.
func (t secretTable) deleteUncarried(ctx context.Context, tx pgx.Tx, columns []string, rows, owned, live string) (int64, error) {
	c := t.carry
	if c.restoredStay {
		return 0, nil
	}
	same := slices.DeleteFunc(slices.Clone(c.same), func(col string) bool { return !slices.Contains(columns, col) })
	tag, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s t WHERE %s AND NOT EXISTS (
		SELECT 1 FROM %s WHERE (%s) AND (%s) = (%s))`,
		t.name, c.credential, rows, owned, prefixed("r.", same), prefixed("t.", same)), live)
	return tag.RowsAffected(), err
}

// userRestored is the SQL condition, over the live row r, that its account
// is in the restored data. Unlike ownerCarried it ignores authority: a live
// key row is always written, so its public key stays enrolled, and
// appKeySettle revokes the keys enrolled since the backup on an account the
// backup gives more authority instead.
const userRestored = `EXISTS (SELECT 1 FROM users u WHERE u.id = r.user_id)`

// SettledAppKeys is what a full restore did with app keys.
type SettledAppKeys struct {
	// Live counts the live server's key rows written into the restored
	// database.
	Live int
	// Restored counts the backup's key rows the live server lacks, kept.
	Restored int
	// Dropped names the backup's keys whose public key a live key of
	// another account holds, left out so a public key is enrolled once.
	Dropped []string
	// Revoked names the keys the restore revoked, and why.
	Revoked []string
	// OtherServer counts the backup's keys revoked because this server
	// keeps its own identity (AppKeysRevoked).
	OtherServer int
	// Kept is true when the restored database keeps app key rows: a full
	// restore whose backup has the key tables and that did not fall back to
	// leaving every credential out.
	Kept bool
}

// appKeySettle settles app keys around the carry of the two key tables: a
// step before the person keys and one after the device keys. It does
// nothing when the restore keeps no key rows, or when the backup's schema
// predates the key tables.
type appKeySettle struct {
	// present is true when the restored schema has both key tables, and on
	// when the restore also keeps their rows.
	present, on bool
	keys        AppKeyCarry
	// person and device are the live rows of the two tables, as JSON.
	person, device string
}

func newAppKeySettle(tables []secretTable, live []string, keys AppKeyCarry) appKeySettle {
	s := appKeySettle{keys: keys}
	found := 0
	for i, t := range tables {
		switch t.name {
		case tableAuthPersonKeys:
			s.person, found = live[i], found+1
		case tableAuthDeviceKeys:
			s.device, found = live[i], found+1
		}
	}
	s.present = found == 2
	s.on = keys != AppKeysLeftOut && s.present
	return s
}

// appKeyTables are the two key tables and what the restore output calls a
// row of each, over the alias k.
var appKeyTables = []struct{ name, describe string }{
	{tableAuthPersonKeys, `format('app person key %s (account %s)', k.id, k.user_id)`},
	{tableAuthDeviceKeys, `format('app device key %s %L (account %s)', k.id, k.name, k.user_id)`},
}

// before runs ahead of the person keys' carry. It records the live rows the
// carry will write (live_app_keys), deletes each restored row whose public
// key or key id a live row with another id holds, so the live row wins it,
// then records the restored rows left (restored_app_keys). Last it revokes,
// for now, each restored person key a live one replaces or that would sit
// beside an account's live person key, so the upsert never breaks the
// one-live-person-key index; the upsert then writes each live row's own
// revoked_at.
func (s appKeySettle) before(ctx context.Context, tx pgx.Tx, table secretTable, result *CarriedCredentials) error {
	if !s.on || table.name != tableAuthPersonKeys {
		return nil
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE live_app_keys ON COMMIT DROP AS
		SELECT r.id, r.public_key, r.key_id FROM json_populate_recordset(NULL::auth_person_keys, $1::json) r WHERE `+userRestored+`
		UNION ALL
		SELECT r.id, r.public_key, r.key_id FROM json_populate_recordset(NULL::auth_device_keys, $2::json) r WHERE `+userRestored,
		s.person, s.device); err != nil {
		return err
	}
	for _, t := range appKeyTables {
		dropped, err := collectStrings(ctx, tx, fmt.Sprintf(`WITH gone AS (
			DELETE FROM %s k USING live_app_keys l
			WHERE l.id <> k.id AND (l.public_key = k.public_key OR l.key_id = k.key_id)
			RETURNING k.*)
			SELECT %s FROM gone k`, t.name, t.describe))
		if err != nil {
			return err
		}
		result.AppKeys.Dropped = append(result.AppKeys.Dropped, dropped...)
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE restored_app_keys ON COMMIT DROP AS
		SELECT id FROM auth_person_keys UNION ALL SELECT id FROM auth_device_keys`); err != nil {
		return err
	}
	staged, err := collectStrings(ctx, tx, `WITH staged AS (
		UPDATE auth_person_keys k SET revoked_at = now()
		WHERE k.revoked_at IS NULL AND EXISTS (
			SELECT 1 FROM json_populate_recordset(NULL::auth_person_keys, $1::json) r
			WHERE `+userRestored+` AND r.user_id = k.user_id AND (r.id = k.id OR r.revoked_at IS NULL))
		RETURNING k.*)
		SELECT `+appKeyTables[0].describe+` FROM staged k
		WHERE NOT EXISTS (SELECT 1 FROM live_app_keys l WHERE l.id = k.id)`, s.person)
	if err != nil {
		return err
	}
	for _, key := range staged {
		result.AppKeys.Revoked = append(result.AppKeys.Revoked, key+": its account has another person key here")
	}
	return nil
}

// appKeyRevokes are the keys after reports revoked, each a condition over
// the key row k, and why. None deletes a row, so a revoked public key can
// never be enrolled again.
var appKeyRevokes = []struct {
	where, why string
	deviceOnly bool
}{
	// A key enrolled since the backup held only the account's live
	// authority, as a promotion revokes keys; the keys the backup holds
	// held the backup's and keep their live state.
	{`k.id NOT IN (SELECT id FROM restored_app_keys) AND NOT EXISTS (SELECT 1 FROM carried_owners o WHERE o.id = k.user_id)`,
		"enrolled since the backup, which gives its account more authority than here", false},
	// An identity id can name another identity once a restore moves the
	// sequence back, so a key whose identity is gone is revoked now rather
	// than at its next use.
	{`k.provider_bound AND NOT EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.id = k.identity_id AND i.user_id = k.user_id)`,
		"its sign-in link is not in the restored data", false},
	// Only device keys wait. The live server's waiting keys stay waiting.
	{`k.status = 'pending' AND k.id NOT IN (SELECT id FROM live_app_keys)`,
		"it was waiting for approval when the backup was taken", true},
}

// after runs once the device keys are carried: it revokes the keys
// appKeyRevokes names, or with AppKeysRevoked every key, and counts what
// the restore kept.
func (s appKeySettle) after(ctx context.Context, tx pgx.Tx, table secretTable, result *CarriedCredentials) error {
	if !s.on || table.name != tableAuthDeviceKeys {
		return nil
	}
	if s.keys == AppKeysRevoked {
		for _, t := range appKeyTables {
			tag, err := tx.Exec(ctx, `UPDATE `+t.name+` SET revoked_at = now() WHERE revoked_at IS NULL`)
			if err != nil {
				return err
			}
			result.AppKeys.OtherServer += int(tag.RowsAffected())
		}
	}
	for _, revoke := range appKeyRevokes {
		for _, t := range appKeyTables {
			if revoke.deviceOnly && t.name != tableAuthDeviceKeys {
				continue
			}
			revoked, err := collectStrings(ctx, tx, fmt.Sprintf(`WITH revoked AS (
				UPDATE %s k SET revoked_at = now() WHERE k.revoked_at IS NULL AND %s RETURNING k.*)
				SELECT %s FROM revoked k`, t.name, revoke.where, t.describe))
			if err != nil {
				return err
			}
			for _, key := range revoked {
				result.AppKeys.Revoked = append(result.AppKeys.Revoked, key+": "+revoke.why)
			}
		}
	}
	result.AppKeys.Live = result.Kept[tableAuthPersonKeys] + result.Kept[tableAuthDeviceKeys]
	return tx.QueryRow(ctx, `SELECT count(*) FROM restored_app_keys k
		WHERE NOT EXISTS (SELECT 1 FROM live_app_keys l WHERE l.id = k.id)`).Scan(&result.AppKeys.Restored)
}

// printAppKeys says what the restore did with app keys: a separate restore
// leaves them out; a full restore keeps the live server's and the backup's
// others, less those it dropped or revoked.
func printAppKeys(out io.Writer, keys SettledAppKeys, liveURL string, carry AppKeyCarry) {
	switch {
	case carry == AppKeysLeftOut:
		_, _ = fmt.Fprintln(out, "Left out the backup's app keys: each app signs in another way and enrolls a new key.")
		return
	case keys.OtherServer > 0:
		_, _ = fmt.Fprintf(out, "Revoked the backup's %s: they name the server the backup came from, and this server keeps its own identity.\n",
			counted(keys.OtherServer, "app key", "app keys"))
	case keys.Live+keys.Restored+len(keys.Dropped) == 0:
		return
	case liveURL == "":
		_, _ = fmt.Fprintf(out, "Kept the backup's app keys (%d).\n", keys.Restored)
	default:
		_, _ = fmt.Fprintf(out, "App keys: kept this server's where it has them (%d) and the backup's otherwise (%d).\n", keys.Live, keys.Restored)
	}
	if len(keys.Dropped) > 0 {
		_, _ = fmt.Fprintf(out, "Left out %s of the backup's whose public key another account holds here: %s.\n",
			counted(len(keys.Dropped), "app key", "app keys"), strings.Join(keys.Dropped, ", "))
	}
	if len(keys.Revoked) > 0 {
		_, _ = fmt.Fprintf(out, "Revoked %s; their devices sign in another way and enroll a new key: %s.\n",
			counted(len(keys.Revoked), "app key", "app keys"), strings.Join(keys.Revoked, "; "))
	}
}

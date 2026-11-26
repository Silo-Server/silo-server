package profileedits

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// The item fields a profile set carries, as edit rows name them. A setting
// is FieldSettingsPrefix plus its key; FieldRemoval is the profile's removal.
const (
	FieldName               = "name"
	FieldPicture            = "picture"
	FieldPIN                = "pin"
	FieldIsChild            = "is_child"
	FieldMaxContentRating   = "max_content_rating"
	FieldMaxAdvisoryAge     = "max_advisory_age"
	FieldRequireAdvisoryAge = "require_advisory_age"
	FieldRequestsBlocked    = "requests_blocked"
	FieldSettingsPrefix     = "settings."
	FieldRemoval            = ""
)

// Executor runs the recorder's SQL: the caller's transaction, or a pool.
type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Edit is one row of profile_set_edits.
type Edit struct {
	SyncID      string
	Field       string
	By          string
	ActorUserID int
	At          time.Time
	Revision    int64
	// ReplacedOwner marks the person's edit of a field the owner had set.
	ReplacedOwner bool
	// SettledAt is when an accepted set settled the row, which keeps it as
	// history only; nil while it is open.
	SettledAt *time.Time
	// ProfileName is the removed profile's name, on a removal row.
	ProfileName string
}

// Open reports a row a set still has to see: not settled.
func (e Edit) Open() bool { return e.SettledAt == nil }

// Mark reports an open edit of the person's own.
func (e Edit) Mark() bool { return e.Open() && e.By == ByAccount }

// Override reports an owner's edit of a field.
func (e Edit) Override() bool { return e.By == ByOwner && e.Field != FieldRemoval }

// Hidden reports an owner's removal of a profile.
func (e Edit) Hidden() bool { return e.By == ByOwner && e.Field == FieldRemoval }

// RecordedSetting reports whether a write of key at profile scope is an
// edit a set carries: a key the contract stores per profile on the server,
// not one marked server_local.
func RecordedSetting(key string) bool {
	contract, err := settingscontract.Load()
	if err != nil {
		return false
	}
	def, ok := contract.Lookup(key)
	return ok && def.IsRemote() && def.AllowsScope(settingscontract.ScopeProfile) && !def.ServerLocal
}

// recordFieldsSQL writes one row per field of a profile with a sync ID; a
// profile without one is not in a set and records nothing. A later edit
// replaces the row whoever made it, with a new revision. The person's edit
// over the owner's row, or over their own open edit that replaced one, keeps
// replaced_owner.
//
// It key-share locks the profile row it reads. A write that took no lock of
// its own (a reset that found nothing to delete) would otherwise read the
// profile from its snapshot while an account deletion or a set that removes
// the profile is deleting it, and insert a row for a profile and account
// that are gone. The lock waits for such a delete to commit and then finds
// no profile; a delete that comes second waits for this one, and clears the
// row it inserted. A key-share lock does not conflict with the no-key-update
// and update locks profile saves and the set take before recording.
const recordFieldsSQL = `
	INSERT INTO profile_set_edits AS e (user_id, sync_id, field, by, actor_user_id)
	SELECT $1, p.sync_id, f.field, $4, $5
	FROM user_profiles p CROSS JOIN unnest($3::text[]) WITH ORDINALITY AS f(field, n)
	WHERE p.user_id = $1 AND p.id = $2 AND p.sync_id IS NOT NULL
	ORDER BY f.n
	FOR KEY SHARE OF p
	ON CONFLICT (user_id, sync_id, field) DO UPDATE SET
		by = excluded.by, actor_user_id = excluded.actor_user_id, at = excluded.at,
		revision = excluded.revision, profile_name = NULL, settled_at = NULL,
		replaced_owner = excluded.by = 'account'
			AND (e.by = 'owner' OR (e.replaced_owner AND e.settled_at IS NULL))
	RETURNING e.revision`

// RecordFields records fields of account's profile profileID as edited by
// ctx's actor. It runs in the caller's transaction, after the write, and
// locks only edit rows: the last rows any transaction locks.
func RecordFields(ctx context.Context, db Executor, account int, profileID string, fields []string) error {
	actor, ok := ActorFor(ctx, account)
	if !ok || len(fields) == 0 {
		return nil
	}
	sorted := slices.Clone(fields)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	rows, err := db.Query(ctx, recordFieldsSQL, account, profileID, sorted, actor.By, actor.UserID)
	if err != nil {
		return fmt.Errorf("recording an edit of profile %s: %w", profileID, err)
	}
	revisions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("recording an edit of profile %s: %w", profileID, err)
	}
	if len(revisions) > 0 {
		FromContext(ctx).noteAccount(account, revisions...)
	}
	return nil
}

// RecordSetting records a change of a profile-scope setting. Keys a set
// does not carry record nothing. A key the settings contract mirrors to
// another (settingscontract.MirrorKey: two spellings of one preference) is
// recorded with its mirror: they are one preference, and a set leaves the
// pair alone while either is the owner's, so a change of either moves both
// to the one who made it, even when the other's value stays the same.
func RecordSetting(ctx context.Context, db Executor, account int, profileID, key string) error {
	if !RecordedSetting(key) {
		return nil
	}
	fields := []string{FieldSettingsPrefix + key}
	if mirror, ok := settingscontract.MirrorKey(key); ok && RecordedSetting(mirror) {
		fields = append(fields, FieldSettingsPrefix+mirror)
	}
	return RecordFields(ctx, db, account, profileID, fields)
}

// RecordRemoval records that the profile with syncID, named name, was
// deleted: its field rows go, and one removal row stays. The caller has just
// deleted the profile in its transaction, so it holds the profile row's lock:
// an account deletion's cascade waits for it to commit and then clears this
// row, and one that went first leaves no profile for the caller to delete. An owner's removal
// keeps the set from bringing the profile back on this server.
func RecordRemoval(ctx context.Context, db Executor, account int, syncID, name string) error {
	actor, ok := ActorFor(ctx, account)
	if !ok || syncID == "" {
		return nil
	}
	if _, err := db.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1 AND sync_id = $2::uuid AND field <> ''`,
		account, syncID); err != nil {
		return fmt.Errorf("recording a profile removal: %w", err)
	}
	var revision int64
	if err := db.QueryRow(ctx, `
		INSERT INTO profile_set_edits AS e (user_id, sync_id, field, by, actor_user_id, profile_name)
		VALUES ($1, $2::uuid, '', $3, $4, $5)
		ON CONFLICT (user_id, sync_id, field) DO UPDATE SET
			by = excluded.by, actor_user_id = excluded.actor_user_id, at = excluded.at,
			revision = excluded.revision, profile_name = excluded.profile_name,
			replaced_owner = false, settled_at = NULL
		RETURNING e.revision`,
		account, syncID, actor.By, actor.UserID, name).Scan(&revision); err != nil {
		return fmt.Errorf("recording a profile removal: %w", err)
	}
	FromContext(ctx).noteAccount(account, revision)
	return nil
}

// List reads every edit row of account, ordered by sync ID and field.
func List(ctx context.Context, db Executor, account int) ([]Edit, error) {
	rows, err := db.Query(ctx, `
		SELECT sync_id::text, field, by, COALESCE(actor_user_id, 0), at, revision, replaced_owner,
		       settled_at, COALESCE(profile_name, '')
		FROM profile_set_edits WHERE user_id = $1
		ORDER BY sync_id, field`, account)
	if err != nil {
		return nil, fmt.Errorf("reading profile edits: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Edit, error) {
		var e Edit
		err := row.Scan(&e.SyncID, &e.Field, &e.By, &e.ActorUserID, &e.At, &e.Revision, &e.ReplacedOwner,
			&e.SettledAt, &e.ProfileName)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading profile edits: %w", err)
	}
	return out, nil
}

// Settle runs when a set is accepted, last in its transaction. It settles
// exactly the marks the set's token covered (matched by revision, so a mark
// written since is kept): it deletes them, except a mark that replaced an
// owner's row, which it keeps stamped as history. It deletes every field
// row of the profiles the set removed.
func Settle(ctx context.Context, db Executor, account int, seen []Edit, removed []string) error {
	syncIDs, fields, revisions := make([]string, 0, len(seen)), make([]string, 0, len(seen)), make([]int64, 0, len(seen))
	for _, e := range seen {
		if e.Mark() {
			syncIDs, fields, revisions = append(syncIDs, e.SyncID), append(fields, e.Field), append(revisions, e.Revision)
		}
	}
	if len(syncIDs) > 0 {
		if _, err := db.Exec(ctx, `
			WITH seen AS (
				SELECT * FROM unnest($2::text[], $3::text[], $4::bigint[]) AS s(sync_id, field, revision)
			), kept AS (
				UPDATE profile_set_edits e SET settled_at = now()
				FROM seen s
				WHERE e.user_id = $1 AND e.sync_id::text = s.sync_id AND e.field = s.field AND e.revision = s.revision
				  AND e.by = 'account' AND e.settled_at IS NULL AND e.replaced_owner
			)
			DELETE FROM profile_set_edits e USING seen s
			WHERE e.user_id = $1 AND e.sync_id::text = s.sync_id AND e.field = s.field AND e.revision = s.revision
			  AND e.by = 'account' AND e.settled_at IS NULL AND NOT e.replaced_owner`,
			account, syncIDs, fields, revisions); err != nil {
			return fmt.Errorf("settling profile edits: %w", err)
		}
	}
	if len(removed) > 0 {
		if _, err := db.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1 AND sync_id::text = ANY($2::text[]) AND field <> ''`,
			account, removed); err != nil {
			return fmt.Errorf("clearing edits of removed profiles: %w", err)
		}
	}
	return nil
}

// ErrOwnerRowsInTheWay refuses to move a profile's edits onto a sync ID that
// carries an owner's rows: a removal the owner made there must keep hiding it.
var ErrOwnerRowsInTheWay = errors.New("the owner's edits are recorded under that sync ID")

// Move moves the edit rows of the profile with sync ID from to sync ID to,
// when a set gives the profile a new ID: an owner's override and the
// person's marks follow the profile. Rows of the person's own already under
// to name a profile that no longer exists (to was free to take) and give
// way; an owner's row there refuses the move.
func Move(ctx context.Context, db Executor, account int, from, to string) error {
	if from == to {
		return nil
	}
	var owner bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM profile_set_edits WHERE user_id = $1 AND sync_id = $2::uuid AND by = 'owner')`,
		account, to).Scan(&owner); err != nil {
		return fmt.Errorf("moving profile edits: %w", err)
	}
	if owner {
		return ErrOwnerRowsInTheWay
	}
	if _, err := db.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1 AND sync_id = $2::uuid`, account, to); err != nil {
		return fmt.Errorf("moving profile edits: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE profile_set_edits SET sync_id = $3::uuid WHERE user_id = $1 AND sync_id = $2::uuid`,
		account, from, to); err != nil {
		return fmt.Errorf("moving profile edits: %w", err)
	}
	return nil
}

// ClearOwner deletes the owner's rows of fields on the profile with syncID
// (FieldRemoval for a removal) and reports how many it deleted. A row the
// person has since replaced is theirs, and stays. It locks edit rows, so it
// runs last in its transaction.
func ClearOwner(ctx context.Context, db Executor, account int, syncID string, fields []string) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1 AND sync_id = $2::uuid AND field = ANY($3::text[]) AND by = 'owner'`,
		account, syncID, fields)
	if err != nil {
		return 0, fmt.Errorf("clearing the owner's edits: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DismissSettled deletes the settled record of field on the profile with
// syncID and reports whether there was one. An open row is the person's
// edit a set has still to see, and stays.
func DismissSettled(ctx context.Context, db Executor, account int, syncID, field string) (bool, error) {
	tag, err := db.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1 AND sync_id = $2::uuid AND field = $3 AND by = 'account' AND settled_at IS NOT NULL`,
		account, syncID, field)
	if err != nil {
		return false, fmt.Errorf("dismissing a settled edit: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAccount deletes every edit row of account. Account deletion calls it
// after deleting the account, in the same transaction.
func DeleteAccount(ctx context.Context, db Executor, account int) error {
	if _, err := db.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1`, account); err != nil {
		return fmt.Errorf("deleting profile edits of account %d: %w", account, err)
	}
	return nil
}

// tokenPrefix versions the token's derivation.
const tokenPrefix = "e1."

// Token names the person's open edits: "" when there are none, otherwise
// "e1." and 32 characters of a SHA-256 over each mark's sync ID, field and
// revision. The revision moves on every recorded edit, so an edit undone by
// another edit still gives a new token. Owner rows and settled rows are left
// out.
func Token(edits []Edit) string {
	marks := make([]Edit, 0, len(edits))
	for _, e := range edits {
		if e.Mark() {
			marks = append(marks, e)
		}
	}
	if len(marks) == 0 {
		return ""
	}
	slices.SortFunc(marks, func(a, b Edit) int {
		if c := strings.Compare(a.SyncID, b.SyncID); c != 0 {
			return c
		}
		return strings.Compare(a.Field, b.Field)
	})
	h := sha256.New()
	for _, e := range marks {
		h.Write([]byte(strings.ToLower(e.SyncID)))
		h.Write([]byte{0})
		h.Write([]byte(e.Field))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(e.Revision, 10)))
		h.Write([]byte{'\n'})
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(h.Sum(nil))[:32]
}

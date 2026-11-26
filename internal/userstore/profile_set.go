package userstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/profileedits"
)

// ErrProfileSetUnsupported is returned by a store that cannot apply a
// profile set in one transaction: the SQLite backend keeps profiles in a
// per-account file while other profile data lives in PostgreSQL.
var ErrProfileSetUnsupported = errors.New("this user store cannot apply a profile set")

// ErrProfileSetAccountGone is a profile set whose account was deleted before
// or while the set was applied.
var ErrProfileSetAccountGone = errors.New("the account no longer exists")

// ProfileSetState is the account's profile set as stored: the version and
// content hash of the last set it accepted.
type ProfileSetState struct {
	// Held is false on an account that never took a set.
	Held       bool
	Version    int64
	SHA256     []byte
	AcceptedAt time.Time
	// Role and Enabled are the account's, read with the state, so a
	// transaction can re-check them under its lock.
	Role    string
	Enabled bool
}

// ProfileSetRow is a household profile with its place in the set.
type ProfileSetRow struct {
	Profile
	// SyncPosition is the profile's place in the set's order; meaningful only
	// while SyncID is set.
	SyncPosition int
	// SyncApplied is the document of values the set last wrote to the
	// profile, nil when it has none.
	SyncApplied json.RawMessage
}

// ProfileSetSnapshot is everything the profile set reads of one account:
// its state, every profile in creation order, and every profile's
// profile-scope setting rows (profile ID, then key, to the stored value).
type ProfileSetSnapshot struct {
	State    ProfileSetState
	Rows     []ProfileSetRow
	Settings map[string]map[string]json.RawMessage
	// Edits is every row of the account's edits made on this server
	// outside a set, ordered by sync ID and field.
	Edits []profileedits.Edit
}

// ProfileSetStore applies a profile set. The PostgreSQL store implements it.
type ProfileSetStore interface { //nolint:iface // implemented by pgstore and its decorators, asserted by the profile handlers
	// ReadProfileSet reads the snapshot without taking any lock, from one
	// consistent view of the database.
	ReadProfileSet(ctx context.Context) (ProfileSetSnapshot, error)
	// WithProfileSetTransaction runs fn holding every lock a profile set
	// needs. inSet is the set's sync IDs: the profiles that hold a sync ID
	// outside it (other than the primary) are the ones the set removes, and
	// their collections are locked before any profile row. The account row
	// is locked before both.
	WithProfileSetTransaction(ctx context.Context, inSet []string, fn func(ProfileSetWriter) error) error
}

// ProfileSetWriter is the profile set's transaction.
type ProfileSetWriter interface {
	PreferenceSettingsWriter
	AuditedProfileWriter
	// Snapshot reads the snapshot under the transaction's locks.
	Snapshot(ctx context.Context) (ProfileSetSnapshot, error)
	// Removable is the profiles the transaction locked for removal: those
	// holding a sync ID outside the set, other than the primary, in ID order.
	Removable() []string
	// PutSettingValueIfChanged writes value unless the stored value equals
	// it, and reports whether it wrote.
	PutSettingValueIfChanged(ctx context.Context, id SettingIdentity, value json.RawMessage) (bool, error)
	// SetSyncFields writes the profile's sync ID and position when either
	// differs, and reports whether it wrote. It leaves updated_at alone.
	SetSyncFields(ctx context.Context, profileID, syncID string, position int) (bool, error)
	// SetSyncApplied writes the profile's applied document when it differs,
	// and reports whether it wrote. It leaves updated_at alone.
	SetSyncApplied(ctx context.Context, profileID string, doc json.RawMessage) (bool, error)
	// DeleteProfile deletes one profile and its rows as a profile delete
	// does, returning its deleted collections' IDs. The profile must be one
	// of Removable.
	DeleteProfile(ctx context.Context, profileID string) ([]string, error)
	// Accept stores the set's version and content hash as the account's,
	// stamped now.
	Accept(ctx context.Context, version int64, sha256 []byte) error
	// SettleEdits settles the marks an accepted set saw (exactly those rows,
	// by revision) and drops the field rows of the profiles it removed,
	// by sync ID (profileedits.Settle). It locks edit rows, so it runs last.
	SettleEdits(ctx context.Context, seen []profileedits.Edit, removed []string) error
	// MoveEdits moves the edit rows of the profile with sync ID from to the
	// ID to the set gave it (profileedits.Move). It locks edit rows, so it
	// runs last.
	MoveEdits(ctx context.Context, from, to string) error
	// ClearOwnerEdits deletes the owner's rows of fields on the profile with
	// syncID and reports how many it deleted (profileedits.ClearOwner). It
	// locks edit rows, so it runs last.
	ClearOwnerEdits(ctx context.Context, syncID string, fields []string) (int64, error)
	// DismissSettledEdit deletes the settled record of field on the profile
	// with syncID (profileedits.DismissSettled). It locks edit rows, so it
	// runs last.
	DismissSettledEdit(ctx context.Context, syncID, field string) (bool, error)
}

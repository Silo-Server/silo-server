package userstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// ErrInvalidSettingIdentity is returned when a setting identity does not match
// the columns its scope requires. Both backends validate through
// SettingIdentity.Validate, so a request rejected by one is rejected by the
// other with the same reason.
var ErrInvalidSettingIdentity = errors.New("invalid setting identity")

// ErrInvalidSettingValue is returned when a stored value is not well-formed
// JSON. The store checks only structural validity: whether a value satisfies its
// definition is settingscontract.ValidateValue's job, and that is the single
// validation path.
var ErrInvalidSettingValue = errors.New("invalid setting value")

// SettingIdentity addresses exactly one canonical setting row: the key plus the
// context columns its scope requires.
//
// Only the fields belonging to Scope are meaningful; Validate enforces that and
// rejects anything else, so an identity that reaches SQL always matches the
// table's CHECK constraints.
type SettingIdentity struct {
	Key   string
	Scope settingscontract.Scope

	ProfileID string
	DeviceID  string
	LibraryID int
	SeriesID  string
}

// Validate reports whether the identity is addressable. It mirrors the scope
// CHECK constraint on user_setting_values so an invalid identity is rejected
// before it reaches either backend rather than surfacing as a driver error whose
// text differs between them.
//
// It also rejects ids that are not in canonical (trimmed) form rather than
// merely non-empty after trimming: the write path binds these fields verbatim,
// while resolution queries bind their trimmed forms, so a value written under
// " p1 " would validate, persist, and then never be found by a resolution for
// p1 — a silently orphaned row.
func (id SettingIdentity) Validate() error {
	for field, value := range map[string]string{
		"key":        id.Key,
		"profile id": id.ProfileID,
		"device id":  id.DeviceID,
		"series id":  id.SeriesID,
	} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: %s %q has surrounding whitespace",
				ErrInvalidSettingIdentity, field, value)
		}
	}
	if id.Key == "" {
		return fmt.Errorf("%w: key is required", ErrInvalidSettingIdentity)
	}
	if !id.Scope.IsRemote() {
		return fmt.Errorf("%w: %q is not a remote scope", ErrInvalidSettingIdentity, id.Scope)
	}

	needProfile := id.Scope != settingscontract.ScopeAccount
	if needProfile && id.ProfileID == "" {
		return fmt.Errorf("%w: scope %q requires a profile id", ErrInvalidSettingIdentity, id.Scope)
	}
	if !needProfile && id.ProfileID != "" {
		return fmt.Errorf("%w: scope %q must not carry a profile id", ErrInvalidSettingIdentity, id.Scope)
	}

	wantDevice := id.Scope == settingscontract.ScopeProfileDevice
	if wantDevice && id.DeviceID == "" {
		return fmt.Errorf("%w: scope %q requires a device id", ErrInvalidSettingIdentity, id.Scope)
	}
	if !wantDevice && id.DeviceID != "" {
		return fmt.Errorf("%w: scope %q must not carry a device id", ErrInvalidSettingIdentity, id.Scope)
	}

	wantLibrary := id.Scope == settingscontract.ScopeProfileLibrary
	if wantLibrary && id.LibraryID <= 0 {
		return fmt.Errorf("%w: scope %q requires a library id", ErrInvalidSettingIdentity, id.Scope)
	}
	if !wantLibrary && id.LibraryID != 0 {
		return fmt.Errorf("%w: scope %q must not carry a library id", ErrInvalidSettingIdentity, id.Scope)
	}

	wantSeries := id.Scope == settingscontract.ScopeProfileSeries
	if wantSeries && id.SeriesID == "" {
		return fmt.Errorf("%w: scope %q requires a series id", ErrInvalidSettingIdentity, id.Scope)
	}
	if !wantSeries && id.SeriesID != "" {
		return fmt.Errorf("%w: scope %q must not carry a series id", ErrInvalidSettingIdentity, id.Scope)
	}
	return nil
}

// SettingValue is one explicit value stored at one scope. Unset is the absence
// of a row, which is distinct from false, 0, "" and JSON null.
type SettingValue struct {
	SettingIdentity

	// Value is the stored JSON. It is whatever settingscontract.NormalizeValue
	// produced; the store neither interprets nor re-normalizes it.
	Value json.RawMessage
	// Revision increments on every write to this row.
	Revision int64
	// CreatedAt and UpdatedAt are RFC3339 UTC timestamps.
	CreatedAt string
	UpdatedAt string
}

// SettingResolutionQuery describes one resolution request: the keys to resolve
// and every identity they may resolve against.
//
// It is deliberately shaped for the batched read. A season view resolving n
// items passes every library and series id in one query and the resolver ranks
// the returned candidate rows by each definition's resolution order in Go. Five
// sequential index lookups per key per item is a rejected implementation.
type SettingResolutionQuery struct {
	Keys []string

	// ProfileIDs are the profiles in play. Empty drops every profile-anchored
	// scope, leaving only account-scope candidates.
	//
	// Several ids is the household shape: GET /profiles serves a preference
	// block per profile, so it passes every profile once and the resolver ranks
	// each profile's candidates in Go. One read per profile is a rejected
	// implementation for the same reason one read per item is.
	ProfileIDs []string
	// DeviceID drops profile_device candidates when empty, which is what an
	// unidentified client (jellycompat's DisplayPreferences seed) needs.
	DeviceID string
	// LibraryIDs and SeriesIDs carry the content contexts of a batch. Empty
	// slices drop their scope from the candidate set.
	LibraryIDs []int
	SeriesIDs  []string
}

// Normalized returns the query with blanks removed and duplicates collapsed, in
// a stable order. Both backends bind the normalized form, so an empty or
// whitespace-only id never reaches SQL as a literal and the two backends issue
// the same predicate for the same request.
func (q SettingResolutionQuery) Normalized() SettingResolutionQuery {
	return SettingResolutionQuery{
		Keys:       compactStrings(q.Keys),
		ProfileIDs: compactStrings(q.ProfileIDs),
		DeviceID:   strings.TrimSpace(q.DeviceID),
		LibraryIDs: compactPositiveInts(q.LibraryIDs),
		SeriesIDs:  compactStrings(q.SeriesIDs),
	}
}

// SettingMutationRecord is the idempotency receipt for one mutation.
//
// The mutation endpoint treats a mutation_id as idempotent for at least 30 days:
// repeating the same id and body returns the prior Result, and reusing an id
// with different content is a mutation_id_conflict, which is what RequestHash
// distinguishes.
type SettingMutationRecord struct {
	MutationID  string
	RequestHash string
	// Result is the serialized per-mutation result returned to a repeat of the
	// same request.
	Result json.RawMessage
	// CreatedAt is set by the store when the record is inserted.
	CreatedAt time.Time
	// ExpiresAt bounds retention. It is not self-enforcing: a sweeper deletes
	// expired rows through DeleteExpiredSettingMutations.
	ExpiresAt time.Time
}

// Validate reports whether the record is storable.
func (r SettingMutationRecord) Validate() error {
	if strings.TrimSpace(r.MutationID) == "" {
		return fmt.Errorf("%w: mutation id is required", ErrInvalidSettingIdentity)
	}
	if strings.TrimSpace(r.RequestHash) == "" {
		return fmt.Errorf("%w: request hash is required", ErrInvalidSettingIdentity)
	}
	if r.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: expires_at is required", ErrInvalidSettingIdentity)
	}
	return ValidateSettingValueJSON(r.Result)
}

// ValidateSettingValueJSON checks that raw is a non-empty, well-formed JSON
// document. It is the only value check the store makes: the contract layer has
// already validated the value against its definition through
// settingscontract.NormalizeValue, and duplicating that here would be the second
// validator this contract exists to remove.
func ValidateSettingValueJSON(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: value is required", ErrInvalidSettingValue)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%w: value is not well-formed JSON", ErrInvalidSettingValue)
	}
	return nil
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func compactPositiveInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Ints(out)
	return out
}

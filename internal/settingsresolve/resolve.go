// Package settingsresolve turns stored setting values into effective ones.
//
// It is the single answer to "what is this setting, for this profile, on this
// device, for this content" — the mutation endpoint, the effective-values
// endpoint, playback, catalog, and the jellycompat DisplayPreferences seed all
// resolve through here. Before this package each of those carried its own
// ladder: internal/catalog/detail.go resolved subtitles across four levels by
// hand and audio across three, internal/api/handlers/settings.go had a
// two-level device/user resolution with a lazy write-back inside a GET, and
// jellycompat read profile columns directly. Those disagreed about precedence,
// which is the drift the contract exists to remove.
//
// The package deliberately holds no storage of its own. It takes candidate rows
// and a contract, and returns decisions.
package settingsresolve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Context is the identity a resolution happens against.
//
// Every field is optional and an absent one simply drops the scopes that need
// it: no DeviceID means no profile_device candidates, no SeriesIDs means no
// profile_series. That is what lets one code path serve an identified client,
// an anonymous jellycompat seed, and a batch spanning many series.
type Context struct {
	ProfileID string
	DeviceID  string
	// LibraryIDs and SeriesIDs are the content contexts in play. A batch
	// resolving a season passes every id once rather than resolving per item.
	LibraryIDs []int
	SeriesIDs  []string
}

// Constraints carries the policy inputs a definition's constrained_by may
// reference, keyed by policy_input name. A missing entry means the policy does
// not constrain that setting for this viewer.
//
// Values are compared through the definition's own value schema, so a ceiling
// on an ordered enum ranks by member order and a ceiling on a number compares
// numerically.
type Constraints map[string]json.RawMessage

// Source names where an effective value came from. It is the resolved scope, or
// ScopeDefault when nothing was stored.
type Source = settingscontract.Scope

// Effective is one resolved setting.
type Effective struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	// Source is the scope Value came from, or "default".
	Source Source `json:"source"`

	// StoredValue is what the user actually authored, present only when a
	// constraint changed the answer. A capped 4K preference must survive the
	// cap so it takes effect the day the cap lifts, so the stored value is
	// reported rather than overwritten.
	StoredValue json.RawMessage `json:"stored_value,omitempty"`
	// Constrained is set when policy narrowed Value away from StoredValue.
	Constrained bool `json:"constrained,omitempty"`
	// ConstraintKind names how it was narrowed, for client copy.
	ConstraintKind settingscontract.ConstraintKind `json:"constraint_kind,omitempty"`

	// RequestedValue, ConstrainedBy and PermittedValues are the public
	// constraint contract. RequestedValue is present only when policy changed
	// the answer; the other two describe an active policy input even when the
	// authored value already falls inside it.
	RequestedValue  json.RawMessage              `json:"requested_value,omitempty"`
	ConstrainedBy   *settingscontract.Constraint `json:"constrained_by,omitempty"`
	PermittedValues []json.RawMessage            `json:"permitted_values,omitempty"`

	// DefinitionRevision lets clients relate an answer to the manifest member
	// that defined it. UpdatedAt belongs to the winning stored row; defaults
	// have no update timestamp.
	DefinitionRevision int    `json:"definition_revision"`
	UpdatedAt          string `json:"updated_at,omitempty"`

	// Identity locates the row Value came from, so a client can offer "reset
	// this device's override" against the exact scope that holds it. Empty for
	// a default.
	Identity *userstore.SettingIdentity `json:"-"`
}

// Store is the read surface this package needs. It is satisfied by
// userstore.UserStore and by a fake in tests.
type Store interface {
	ListSettingValuesForResolution(
		ctx context.Context, query userstore.SettingResolutionQuery,
	) ([]userstore.SettingValue, error)
}

// Resolver resolves against one contract.
type Resolver struct {
	contract *settingscontract.Manifest
}

// New returns a Resolver over the given contract.
func New(contract *settingscontract.Manifest) *Resolver {
	return &Resolver{contract: contract}
}

// Resolve returns the effective value for each requested key.
//
// One batched store read regardless of how many keys, libraries, or series are
// in play; ranking happens here in Go. Unknown keys are omitted rather than
// erroring, so a newer client asking for a setting this server does not have
// gets a short answer instead of a failed request.
func (r *Resolver) Resolve(
	ctx context.Context,
	store Store,
	rc Context,
	keys []string,
	constraints Constraints,
) ([]Effective, error) {
	if r == nil || r.contract == nil {
		return nil, fmt.Errorf("settingsresolve: no contract")
	}

	known, defs := r.knownKeys(keys)
	if len(known) == 0 {
		return nil, nil
	}

	stored, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       known,
		ProfileIDs: nonEmpty(rc.ProfileID),
		DeviceID:   rc.DeviceID,
		LibraryIDs: rc.LibraryIDs,
		SeriesIDs:  rc.SeriesIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("settingsresolve: reading candidates: %w", err)
	}

	return r.rank(known, defs, stored, rc, constraints), nil
}

// ResolveContexts resolves the same keys for several content contexts using
// one candidate read. The contexts may vary by profile, library, or series,
// but they must use the same device because SettingResolutionQuery represents
// one requesting client device.
func (r *Resolver) ResolveContexts(
	ctx context.Context,
	store Store,
	contexts []Context,
	keys []string,
	constraints Constraints,
) ([][]Effective, error) {
	if r == nil || r.contract == nil {
		return nil, fmt.Errorf("settingsresolve: no contract")
	}
	if len(contexts) == 0 {
		return nil, nil
	}

	known, defs := r.knownKeys(keys)
	if len(known) == 0 {
		return make([][]Effective, len(contexts)), nil
	}

	deviceID := contexts[0].DeviceID
	query := userstore.SettingResolutionQuery{Keys: known, DeviceID: deviceID}
	for _, rc := range contexts {
		if rc.DeviceID != deviceID {
			return nil, fmt.Errorf("settingsresolve: batched contexts use different devices")
		}
		if rc.ProfileID != "" {
			query.ProfileIDs = append(query.ProfileIDs, rc.ProfileID)
		}
		query.LibraryIDs = append(query.LibraryIDs, rc.LibraryIDs...)
		query.SeriesIDs = append(query.SeriesIDs, rc.SeriesIDs...)
	}

	stored, err := store.ListSettingValuesForResolution(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("settingsresolve: reading candidates: %w", err)
	}

	out := make([][]Effective, len(contexts))
	for i, rc := range contexts {
		out[i] = r.rank(known, defs, stored, rc, constraints)
	}
	return out, nil
}

// ResolveProfiles resolves the same keys for several profiles in one store
// read, returning each profile's effective values keyed by profile id.
//
// It exists for the household shape Resolve cannot serve without a read per
// profile: GET /profiles serves a preference block for every profile on the
// account, and looping Resolve would turn one list request into n round trips.
// Ranking is per profile against the shared candidate set, so the answer for
// each id is identical to what a single-profile Resolve would return.
//
// Content contexts are deliberately absent: a profile list has no library or
// series in play, so only the account, profile, and profile_device scopes can
// contribute.
func (r *Resolver) ResolveProfiles(
	ctx context.Context,
	store Store,
	profileIDs []string,
	keys []string,
	constraints Constraints,
) (map[string][]Effective, error) {
	if r == nil || r.contract == nil {
		return nil, fmt.Errorf("settingsresolve: no contract")
	}
	if len(profileIDs) == 0 {
		return nil, nil
	}

	known, defs := r.knownKeys(keys)
	if len(known) == 0 {
		return nil, nil
	}

	stored, err := store.ListSettingValuesForResolution(ctx, userstore.SettingResolutionQuery{
		Keys:       known,
		ProfileIDs: profileIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("settingsresolve: reading candidates: %w", err)
	}

	out := make(map[string][]Effective, len(profileIDs))
	for _, profileID := range profileIDs {
		if _, seen := out[profileID]; seen {
			continue
		}
		out[profileID] = r.rank(known, defs, stored, Context{ProfileID: profileID}, constraints)
	}
	return out, nil
}

// knownKeys filters requested keys down to the remotely-stored definitions this
// contract declares, preserving request order and dropping duplicates.
func (r *Resolver) knownKeys(keys []string) ([]string, map[string]*settingscontract.Definition) {
	known := make([]string, 0, len(keys))
	defs := make(map[string]*settingscontract.Definition, len(keys))
	for _, key := range keys {
		def, ok := r.contract.Lookup(key)
		if !ok || def.Persistence != settingscontract.PersistenceRemote {
			// client_local settings never have server rows; asking for one is
			// not an error, it simply has no server answer.
			continue
		}
		if _, seen := defs[key]; seen {
			continue
		}
		defs[key] = def
		known = append(known, key)
	}
	return known, defs
}

// rank turns one candidate set into one context's effective values. Candidates
// that belong to another profile are filtered by pickForScope, which is what
// lets a batched read serve several profiles from the same rows.
func (r *Resolver) rank(
	known []string,
	defs map[string]*settingscontract.Definition,
	stored []userstore.SettingValue,
	rc Context,
	constraints Constraints,
) []Effective {
	byKey := make(map[string][]userstore.SettingValue, len(known))
	for _, row := range stored {
		byKey[row.Key] = append(byKey[row.Key], row)
	}

	out := make([]Effective, 0, len(known))
	for _, key := range known {
		out = append(out, r.resolveOne(defs[key], byKey[key], rc, constraints))
	}
	return out
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// resolveOne ranks one key's candidates by its declared resolution order.
func (r *Resolver) resolveOne(
	def *settingscontract.Definition,
	candidates []userstore.SettingValue,
	rc Context,
	constraints Constraints,
) Effective {
	eff := Effective{
		Key:    def.Key,
		Value:  append(json.RawMessage(nil), def.DefaultValue...),
		Source: settingscontract.ScopeDefault,
		// Definitions can change additively after their introduced_in marker.
		// The response therefore names the manifest revision that produced the
		// effective definition, not merely the revision that first added its key.
		DefinitionRevision: r.contract.Revision,
	}

	for _, scope := range def.ResolutionOrder {
		if scope == settingscontract.ScopeDefault {
			break
		}
		row, ok := pickForScope(scope, candidates, rc)
		if !ok {
			continue
		}
		eff.Value = append(json.RawMessage(nil), row.Value...)
		eff.Source = scope
		eff.UpdatedAt = row.UpdatedAt
		identity := row.SettingIdentity
		eff.Identity = &identity
		break
	}

	return applyConstraint(def, eff, constraints)
}

// pickForScope returns the candidate row for one scope.
//
// Library and series scopes can return several rows in a batch — one per
// library or series in the request. A batch spanning several libraries or
// series has no single right answer, so a caller wanting per-item values must
// resolve per item; when several rows match anyway, the tie is broken by
// ascending library id then series id, purely so two identical requests
// cannot disagree.
func pickForScope(
	scope settingscontract.Scope,
	candidates []userstore.SettingValue,
	rc Context,
) (userstore.SettingValue, bool) {
	matches := make([]userstore.SettingValue, 0, 2)
	for _, row := range candidates {
		if row.Scope != scope {
			continue
		}
		switch scope {
		case settingscontract.ScopeAccount:
			matches = append(matches, row)
		case settingscontract.ScopeProfile:
			if row.ProfileID == rc.ProfileID {
				matches = append(matches, row)
			}
		case settingscontract.ScopeProfileDevice:
			if row.ProfileID == rc.ProfileID && row.DeviceID == rc.DeviceID && rc.DeviceID != "" {
				matches = append(matches, row)
			}
		case settingscontract.ScopeProfileLibrary:
			if row.ProfileID == rc.ProfileID && containsInt(rc.LibraryIDs, row.LibraryID) {
				matches = append(matches, row)
			}
		case settingscontract.ScopeProfileSeries:
			if row.ProfileID == rc.ProfileID && containsString(rc.SeriesIDs, row.SeriesID) {
				matches = append(matches, row)
			}
		}
	}
	if len(matches) == 0 {
		return userstore.SettingValue{}, false
	}
	if len(matches) > 1 {
		// Deterministic rather than arbitrary: a batch spanning several
		// libraries or series has no single right answer, and the caller is
		// expected to resolve per item. Sorting means it at least cannot differ
		// between two identical requests.
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].LibraryID != matches[j].LibraryID {
				return matches[i].LibraryID < matches[j].LibraryID
			}
			return matches[i].SeriesID < matches[j].SeriesID
		})
	}
	return matches[0], true
}

// applyConstraint narrows an effective value to what policy permits.
//
// The stored value is never destroyed: a preference capped today must take
// effect the day the cap lifts, so the cap is reported alongside the authored
// value rather than replacing it.
func applyConstraint(
	def *settingscontract.Definition,
	eff Effective,
	constraints Constraints,
) Effective {
	if def.ConstrainedBy == nil || len(constraints) == 0 {
		return eff
	}
	limit, ok := constraints[def.ConstrainedBy.PolicyInput]
	if !ok || len(limit) == 0 {
		return eff
	}

	eff.ConstrainedBy = &settingscontract.Constraint{
		PolicyInput: def.ConstrainedBy.PolicyInput,
		Constraint:  def.ConstrainedBy.Constraint,
	}
	eff.PermittedValues = permittedValues(def, limit)

	narrow, changed := narrowValue(def, eff.Value, limit)
	if !changed {
		return eff
	}
	eff.StoredValue = eff.Value
	eff.RequestedValue = eff.Value
	eff.Value = narrow
	eff.Constrained = true
	eff.ConstraintKind = def.ConstrainedBy.Constraint
	return eff
}

// permittedValues returns the finite choices allowed by an active policy
// input. Numeric ceilings and floors do not have a finite option list, so they
// leave this absent and clients continue rendering their numeric control.
func permittedValues(def *settingscontract.Definition, limit json.RawMessage) []json.RawMessage {
	switch def.ConstrainedBy.Constraint {
	case settingscontract.ConstraintAllowlist:
		var allowed []json.RawMessage
		if err := json.Unmarshal(limit, &allowed); err != nil || len(allowed) == 0 {
			return nil
		}
		out := make([]json.RawMessage, 0, len(allowed))
		for _, value := range allowed {
			out = append(out, append(json.RawMessage(nil), value...))
		}
		return out

	case settingscontract.ConstraintLocked:
		return []json.RawMessage{append(json.RawMessage(nil), limit...)}

	case settingscontract.ConstraintCeiling, settingscontract.ConstraintFloor:
		if def.ValueSchema.Type != settingscontract.TypeEnum {
			return nil
		}
		out := make([]json.RawMessage, 0, len(def.ValueSchema.Values))
		for _, member := range def.ValueSchema.Values {
			raw, err := json.Marshal(member.Value)
			if err != nil {
				continue
			}
			comparison := def.ValueSchema.CompareValues(raw, limit)
			if def.ConstrainedBy.Constraint == settingscontract.ConstraintCeiling && comparison <= 0 ||
				def.ConstrainedBy.Constraint == settingscontract.ConstraintFloor && comparison >= 0 {
				out = append(out, raw)
			}
		}
		return out
	}
	return nil
}

// narrowValue applies one constraint kind, returning the permitted value and
// whether it differs from the stored one.
func narrowValue(
	def *settingscontract.Definition,
	value, limit json.RawMessage,
) (json.RawMessage, bool) {
	switch def.ConstrainedBy.Constraint {
	case settingscontract.ConstraintLocked:
		// The policy value replaces the user's outright.
		if bytes.Equal(bytes.TrimSpace(value), bytes.TrimSpace(limit)) {
			return value, false
		}
		return append(json.RawMessage(nil), limit...), true

	case settingscontract.ConstraintCeiling:
		// null on a nullable numeric means "no cap of my own", which is
		// unbounded above — exactly what a ceiling exists to bring down. It has
		// no rank, so CompareValues reports 0 and the value would otherwise
		// slip past the cap it most needs to obey.
		if isNull(value) && isNumeric(def) {
			return append(json.RawMessage(nil), limit...), true
		}
		if def.ValueSchema.CompareValues(value, limit) <= 0 {
			return value, false
		}
		return append(json.RawMessage(nil), limit...), true

	case settingscontract.ConstraintFloor:
		// The mirror of the above: unbounded above already satisfies any floor.
		if isNull(value) && isNumeric(def) {
			return value, false
		}
		if def.ValueSchema.CompareValues(value, limit) >= 0 {
			return value, false
		}
		return append(json.RawMessage(nil), limit...), true

	case settingscontract.ConstraintAllowlist:
		var allowed []json.RawMessage
		if err := json.Unmarshal(limit, &allowed); err != nil || len(allowed) == 0 {
			return value, false
		}
		trimmed := bytes.TrimSpace(value)
		for _, entry := range allowed {
			if bytes.Equal(bytes.TrimSpace(entry), trimmed) {
				return value, false
			}
		}
		// Falling back to the first allowed member rather than the default:
		// the default may itself be outside the allowlist, and an effective
		// value the policy forbids is the one thing this must never return.
		return append(json.RawMessage(nil), allowed[0]...), true
	}
	return value, false
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func isNumeric(def *settingscontract.Definition) bool {
	return def.ValueSchema.Type == settingscontract.TypeInteger ||
		def.ValueSchema.Type == settingscontract.TypeNumber
}

func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func containsString(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

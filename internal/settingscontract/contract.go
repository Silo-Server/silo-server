// Package settingscontract loads and validates the canonical cross-platform
// user settings manifest.
//
// The manifest in contracts/settings/v1 is the single source of truth for every
// production, user-facing setting: its key, value type, constraints, storage
// scopes, resolution order, default, and UX copy. The server embeds it, clients
// vendor a pinned copy and generate bindings from it, and no production setting
// may exist without an entry here.
//
// See docs/superpowers/specs/2026-07-10-cross-platform-user-settings-contract-design.md.
package settingscontract

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Scope identifies the storage identity a value is attached to.
type Scope string

const (
	ScopeAccount        Scope = "account"
	ScopeProfile        Scope = "profile"
	ScopeProfileDevice  Scope = "profile_device"
	ScopeProfileLibrary Scope = "profile_library"
	ScopeProfileSeries  Scope = "profile_series"
	// ScopeClientLocal is not addressable by the server values API. It exists so
	// client_local definitions can express a resolution order in the same shape
	// as remote ones.
	ScopeClientLocal Scope = "client_local"
	// ScopeDefault terminates every resolution order.
	ScopeDefault Scope = "default"
)

// remoteScopes are the scopes the server stores and authorizes.
var remoteScopes = map[Scope]struct{}{
	ScopeAccount:        {},
	ScopeProfile:        {},
	ScopeProfileDevice:  {},
	ScopeProfileLibrary: {},
	ScopeProfileSeries:  {},
}

// IsRemote reports whether the scope is stored and authorized by the server.
func (s Scope) IsRemote() bool {
	_, ok := remoteScopes[s]
	return ok
}

// Persistence declares who stores a setting's value.
type Persistence string

const (
	// PersistenceRemote values are stored by the server and roam per their scope.
	PersistenceRemote Persistence = "remote"
	// PersistenceClientLocal values are defined by the contract but stored only
	// by the client. They are never sent to the API.
	PersistenceClientLocal Persistence = "client_local"
)

// ValueType tags a value schema.
type ValueType string

const (
	TypeBoolean     ValueType = "boolean"
	TypeInteger     ValueType = "integer"
	TypeNumber      ValueType = "number"
	TypeString      ValueType = "string"
	TypeEnum        ValueType = "enum"
	TypeLanguageTag ValueType = "language_tag"
	TypeObject      ValueType = "object"
)

// ConstraintKind describes how a policy input narrows a setting.
type ConstraintKind string

const (
	// ConstraintCeiling caps the value at the policy input. Valid only on an
	// ordered enum or a numeric type.
	ConstraintCeiling ConstraintKind = "ceiling"
	// ConstraintFloor raises the value to the policy input. Same restriction.
	ConstraintFloor ConstraintKind = "floor"
	// ConstraintAllowlist restricts the value to a policy-supplied set.
	ConstraintAllowlist ConstraintKind = "allowlist"
	// ConstraintLocked prevents the user from authoring the setting at all.
	ConstraintLocked ConstraintKind = "locked"
)

// Manifest is the canonical settings contract.
type Manifest struct {
	// APIVersion identifies the settings protocol. It changes only for a change
	// no revision rule can express.
	APIVersion int `json:"api_version"`
	// Revision is bumped by every manifest PR and is what clients filter
	// definitions, scopes, enum members, and bounds against.
	Revision int `json:"revision"`
	// Definitions is ordered as authored; use Lookup for access by key.
	Definitions []Definition `json:"definitions"`

	byKey map[string]*Definition
}

// Definition is the canonical contract for one setting.
type Definition struct {
	Key             string          `json:"key"`
	IntroducedIn    int             `json:"introduced_in"`
	Persistence     Persistence     `json:"persistence"`
	AllowedScopes   []ScopeEntry    `json:"allowed_scopes"`
	ResolutionOrder []Scope         `json:"resolution_order"`
	ValueSchema     ValueSchema     `json:"value_schema"`
	DefaultValue    json.RawMessage `json:"default_value"`
	ConstrainedBy   *Constraint     `json:"constrained_by,omitempty"`
	Platforms       []string        `json:"platforms,omitempty"`
	Category        string          `json:"category"`
	Label           string          `json:"label"`
	Description     string          `json:"description"`
	Unit            string          `json:"unit,omitempty"`
	Control         string          `json:"recommended_control,omitempty"`
	Deprecated      bool            `json:"deprecated,omitempty"`
	// Notes is maintainer commentary. It is stripped from the public manifest.
	Notes string `json:"notes,omitempty"`
}

// ScopeEntry is a scope, optionally tagged with the revision that added it to
// its definition. It unmarshals from either a bare string or an object so the
// common case stays readable in the manifest.
type ScopeEntry struct {
	Scope        Scope `json:"scope"`
	IntroducedIn int   `json:"introduced_in,omitempty"`
}

// UnmarshalJSON accepts "profile" or {"scope":"profile","introduced_in":14}.
func (s *ScopeEntry) UnmarshalJSON(data []byte) error {
	var bare string
	if err := json.Unmarshal(data, &bare); err == nil {
		s.Scope = Scope(bare)
		s.IntroducedIn = 0
		return nil
	}
	var tagged struct {
		Scope        Scope `json:"scope"`
		IntroducedIn int   `json:"introduced_in"`
	}
	if err := json.Unmarshal(data, &tagged); err != nil {
		return fmt.Errorf("scope entry must be a string or an object: %w", err)
	}
	s.Scope = tagged.Scope
	s.IntroducedIn = tagged.IntroducedIn
	return nil
}

// MarshalJSON emits the bare string form when no revision tag is present, so
// round-tripping the manifest does not rewrite unrelated entries.
func (s ScopeEntry) MarshalJSON() ([]byte, error) {
	if s.IntroducedIn == 0 {
		return json.Marshal(string(s.Scope))
	}
	return json.Marshal(struct {
		Scope        Scope `json:"scope"`
		IntroducedIn int   `json:"introduced_in"`
	}{s.Scope, s.IntroducedIn})
}

// ValueSchema is the tagged type system for setting values. Only the fields
// belonging to Type are meaningful; Validate enforces that.
type ValueSchema struct {
	Type     ValueType `json:"type"`
	Nullable bool      `json:"nullable,omitempty"`

	// Numeric (integer, number).
	Minimum *Bound   `json:"minimum,omitempty"`
	Maximum *Bound   `json:"maximum,omitempty"`
	Step    *float64 `json:"step,omitempty"`

	// String.
	MinLength *int   `json:"min_length,omitempty"`
	MaxLength *int   `json:"max_length,omitempty"`
	Pattern   string `json:"pattern,omitempty"`

	// Enum.
	Values  []EnumMember `json:"values,omitempty"`
	Ordered bool         `json:"ordered,omitempty"`

	// Object.
	SchemaRef string `json:"schema_ref,omitempty"`

	// compiledPattern is Pattern, compiled once when the manifest loads.
	// ValidateValue runs per request, so it must not compile a regex per call.
	compiledPattern *regexp.Regexp
}

// Bound is a numeric limit together with every earlier value it has had.
//
// A widened bound cannot be represented as one scalar plus the revision that
// introduced it, because that discards the value it replaced. A client pinned
// to the newer revision talking to a server on the older one would then have no
// correct answer: honoring the new bound offers values the server rejects, and
// filtering the tagged bound out leaves the setting with no bound at all.
// Keeping the history lets AtRevision hand back the limit the peer actually
// enforces.
type Bound struct {
	// History is ordered oldest first. The last entry is the bound in force on
	// the manifest that declares it.
	History []BoundEntry
}

// BoundEntry is one value of a bound and the revision that introduced it. A
// zero IntroducedIn means the bound has held since its definition appeared.
type BoundEntry struct {
	Value        float64 `json:"value"`
	IntroducedIn int     `json:"introduced_in,omitempty"`
}

// UnmarshalJSON accepts 240 or
// [{"value":240},{"value":480,"introduced_in":3}]. The bare form is the common
// case — most bounds are never widened — and keeping it readable is worth the
// dual shape, the same trade ScopeEntry makes.
func (b *Bound) UnmarshalJSON(data []byte) error {
	var bare float64
	if err := json.Unmarshal(data, &bare); err == nil {
		b.History = []BoundEntry{{Value: bare}}
		return nil
	}
	var history []BoundEntry
	if err := json.Unmarshal(data, &history); err != nil {
		return fmt.Errorf("bound must be a number or an array of {value, introduced_in}: %w", err)
	}
	b.History = history
	return nil
}

// MarshalJSON emits the bare number when a bound has never been widened, so
// round-tripping the manifest does not rewrite untouched entries.
func (b Bound) MarshalJSON() ([]byte, error) {
	if len(b.History) == 1 && b.History[0].IntroducedIn == 0 {
		return json.Marshal(b.History[0].Value)
	}
	return json.Marshal(b.History)
}

// Current returns the bound in force on this manifest.
func (b *Bound) Current() (float64, bool) {
	if b == nil || len(b.History) == 0 {
		return 0, false
	}
	return b.History[len(b.History)-1].Value, true
}

// AtRevision returns the bound a peer at revision enforces: the newest entry
// introduced no later than that revision. Clients call this so they never offer
// a value the connected server will refuse.
func (b *Bound) AtRevision(revision int) (float64, bool) {
	if b == nil {
		return 0, false
	}
	var (
		value float64
		found bool
	)
	for _, entry := range b.History {
		if entry.IntroducedIn > revision {
			continue
		}
		value, found = entry.Value, true
	}
	return value, found
}

// EnumMember is one allowed enum value. Members are objects rather than bare
// strings so a member added after the definition can carry its own revision.
type EnumMember struct {
	Value        any    `json:"value"`
	Label        string `json:"label,omitempty"`
	IntroducedIn int    `json:"introduced_in,omitempty"`
	Deprecated   bool   `json:"deprecated,omitempty"`
}

// Constraint binds a definition to a policy input that narrows it at
// resolution time. A constraint filters what a preference does; it never
// validates what a preference is, so a mutation that exceeds a restriction is
// still stored.
type Constraint struct {
	PolicyInput string         `json:"policy_input"`
	Constraint  ConstraintKind `json:"constraint"`
}

// Lookup returns the definition for key.
func (m *Manifest) Lookup(key string) (*Definition, bool) {
	if m == nil || m.byKey == nil {
		return nil, false
	}
	def, ok := m.byKey[key]
	return def, ok
}

// Keys returns every definition key in manifest order.
func (m *Manifest) Keys() []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m.Definitions))
	for i := range m.Definitions {
		keys = append(keys, m.Definitions[i].Key)
	}
	return keys
}

// index builds the by-key lookup. It reports duplicate keys as an error rather
// than letting the last one silently win.
func (m *Manifest) index() error {
	m.byKey = make(map[string]*Definition, len(m.Definitions))
	for i := range m.Definitions {
		def := &m.Definitions[i]
		if _, exists := m.byKey[def.Key]; exists {
			return fmt.Errorf("duplicate setting key %q", def.Key)
		}
		m.byKey[def.Key] = def
	}
	return nil
}

// AllowsScope reports whether the definition may store a value at scope.
func (d *Definition) AllowsScope(scope Scope) bool {
	for _, entry := range d.AllowedScopes {
		if entry.Scope == scope {
			return true
		}
	}
	return false
}

// IsRemote reports whether the server stores this setting's values.
func (d *Definition) IsRemote() bool {
	return d.Persistence == PersistenceRemote
}

// VisibleAtRevision reports whether a client pinned to revision may surface this
// definition. A client hides definitions the connected server does not know.
func (d *Definition) VisibleAtRevision(revision int) bool {
	return d.IntroducedIn <= revision
}

// ScopesAtRevision returns the scopes valid against a peer at revision.
func (d *Definition) ScopesAtRevision(revision int) []Scope {
	scopes := make([]Scope, 0, len(d.AllowedScopes))
	for _, entry := range d.AllowedScopes {
		if entry.IntroducedIn > revision {
			continue
		}
		scopes = append(scopes, entry.Scope)
	}
	return scopes
}

// EnumValuesAtRevision returns the enum members valid against a peer at
// revision. Clients render from this so a newer client never offers a choice an
// older server will reject.
func (d *Definition) EnumValuesAtRevision(revision int) []EnumMember {
	if d.ValueSchema.Type != TypeEnum {
		return nil
	}
	members := make([]EnumMember, 0, len(d.ValueSchema.Values))
	for _, member := range d.ValueSchema.Values {
		if member.IntroducedIn > revision {
			continue
		}
		members = append(members, member)
	}
	return members
}

package apiv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Instant is the v2 wire form of a point in time: RFC 3339, UTC, exactly
// millisecond precision, Z suffix. A zero Instant is not a value: it is
// rejected on input and refused on output, so an unset time is either omitted
// (a pointer with omitempty) or an explicit null where the schema allows it.
type Instant struct {
	time.Time
}

// NewInstant converts a time to the wire representation.
func NewInstant(t time.Time) Instant { return Instant{Time: t.UTC().Truncate(time.Millisecond)} }

// instantLayout is RFC 3339 with exactly three fractional digits.
const instantLayout = "2006-01-02T15:04:05.000Z"

var errZeroInstant = errors.New("instant is zero")

// String renders the wire form.
func (i Instant) String() string { return i.Time.UTC().Format(instantLayout) }

// MarshalJSON refuses the zero value: a zero time on the wire is always a
// bug, and the alternative (1-1-1T00:00:00.000Z) is a silent one.
func (i Instant) MarshalJSON() ([]byte, error) {
	if i.IsZero() {
		return nil, errZeroInstant
	}
	return json.Marshal(i.String())
}

// UnmarshalJSON accepts any RFC 3339 instant and normalizes it to UTC
// milliseconds; the zero instant and non-strings are rejected.
func (i *Instant) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("expected an RFC 3339 instant string")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() {
		return errors.New("expected an RFC 3339 instant")
	}
	*i = NewInstant(t)
	return nil
}

// Schema describes Instant as a date-time string with the zero instant
// excluded, so Huma's schema validation reports a zero value at the field
// (body.when) before the decoder ever sees it.
func (Instant) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Format: "date-time", Pattern: nonZeroInstantPattern, PatternDescription: "a non-zero RFC 3339 instant", Description: "RFC 3339 instant in UTC with millisecond precision"}
}

// nonZeroInstantPattern rejects the zero instant (year 0001) as text; the
// full syntax check is the date-time format.
const nonZeroInstantPattern = `^(?:[1-9][0-9]{3}|0[1-9][0-9]{2}|00[1-9][0-9]|000[2-9])-`

// NullableInstant is the explicit-null form: a JSON null is a documented
// "known to have no value", distinct from omission.
type NullableInstant struct {
	Valid bool
	Time  Instant
}

// UnmarshalJSON accepts null or an instant.
func (n *NullableInstant) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*n = NullableInstant{}
		return nil
	}
	var i Instant
	if err := i.UnmarshalJSON(b); err != nil {
		return err
	}
	*n = NullableInstant{Valid: true, Time: i}
	return nil
}

// MarshalJSON renders null or the instant.
func (n NullableInstant) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return n.Time.MarshalJSON()
}

// Schema describes the field as a nullable date-time.
func (NullableInstant) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Format: "date-time", Pattern: nonZeroInstantPattern, PatternDescription: "a non-zero RFC 3339 instant", Nullable: true, Description: "RFC 3339 instant in UTC with millisecond precision, or null"}
}

// ID is an opaque string identifier. Numeric database keys cross the wire as
// strings; clients never parse or order them.
type ID string

// IDFromInt renders an internal integer key as an opaque ID.
func IDFromInt(v int64) ID { return ID(fmt.Sprint(v)) }

// Schema describes ID as an opaque string.
func (ID) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, MinLength: ptr(1), Description: "Opaque identifier"}
}

func ptr[T any](v T) *T { return &v }

// intOfID recovers an internal integer key from an opaque ID.
func intOfID(id ID) (int, error) { return strconv.Atoi(string(id)) }

// Patch is the presence-aware PATCH transport for one field: omitted
// (unchanged), explicit null (clear, only where the schema is nullable), or
// a value. Go pointers and omitempty cannot express the three states.
type Patch[T any] struct {
	// Present is true when the field appeared in the document at all.
	Present bool
	// Null is true when the field appeared with the value null.
	Null bool
	// Value is set when Present && !Null.
	Value T
}

// UnmarshalJSON is only called when the field is present.
func (p *Patch[T]) UnmarshalJSON(b []byte) error {
	p.Present = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		p.Null = true
		var zero T
		p.Value = zero
		return nil
	}
	p.Null = false
	return json.Unmarshal(b, &p.Value)
}

// MarshalJSON renders the field for echo tests; an absent field marshals as
// null, which callers avoid by using omitzero.
func (p Patch[T]) MarshalJSON() ([]byte, error) {
	if !p.Present || p.Null {
		return []byte("null"), nil
	}
	return json.Marshal(p.Value)
}

// IsZero lets `json:",omitzero"` drop an absent field.
func (p Patch[T]) IsZero() bool { return !p.Present }

// Schema describes the field as a nullable value of T.
func (Patch[T]) Schema(r huma.Registry) *huma.Schema {
	var v T
	s := r.Schema(reflect.TypeOf(v), true, "")
	if s.Ref != "" {
		return &huma.Schema{OneOf: []*huma.Schema{s, {Type: "null"}}}
	}
	out := *s
	out.Nullable = true
	return &out
}

// PageInfo is the cursor state a paginated collection returns.
type PageInfo struct {
	NextCursor string `json:"next_cursor,omitempty" doc:"Opaque cursor for the next page; absent on the last page"`
	HasMore    bool   `json:"has_more" doc:"Whether a next page exists"`
}

// Collection is the shared collection envelope. Items is never null: use
// NewCollection, which initializes it, rather than a zero literal.
type Collection[T any] struct {
	Items []T       `json:"items" doc:"The page's items; empty, never null"`
	Page  *PageInfo `json:"page,omitempty" doc:"Cursor state; absent for bounded unpaginated collections"`
}

// NewCollection builds an envelope with a non-nil Items slice.
func NewCollection[T any](items []T) Collection[T] {
	if items == nil {
		items = []T{}
	}
	return Collection[T]{Items: items}
}

// Paginated builds a paginated envelope.
func Paginated[T any](items []T, next string) Collection[T] {
	c := NewCollection(items)
	c.Page = &PageInfo{NextCursor: next, HasMore: next != ""}
	return c
}

// NonNil returns a non-nil slice for a response field.
func NonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// NonNilMap returns a non-nil map for a response field.
func NonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// Limit defaults and bounds (docs/architecture/api-contract.md, "Query and
// pagination conventions").
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// LimitParam is the shared `limit` query parameter. Embed it in an input
// struct; Huma enforces the bound with 422.
type LimitParam struct {
	Limit int `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Page size; default 50, maximum 200"`
}

// SortField is one parsed sort term.
type SortField struct {
	Field string
	Desc  bool
}

// ParseSort parses the `sort=field,-other` grammar against an operation's
// allowlist. An unknown or duplicated field is a validation failure (422).
func ParseSort(raw string, allowed []string) ([]SortField, *Problem) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	allow := map[string]bool{}
	for _, a := range allowed {
		allow[a] = true
	}
	seen := map[string]bool{}
	var out []SortField
	for _, term := range strings.Split(raw, ",") {
		field := strings.TrimSpace(term)
		desc := false
		if strings.HasPrefix(field, "-") {
			desc = true
			field = field[1:]
		}
		if field == "" || !allow[field] || seen[field] {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "query.sort", Code: "invalid_sort_field", Detail: "sort names a field this operation does not sort by, or names one twice"})
		}
		seen[field] = true
		out = append(out, SortField{Field: field, Desc: desc})
	}
	return out, nil
}

// idsOfInts renders integer keys as IDs, keeping nil (inherit) distinct from
// empty (none).
func idsOfInts(ints []int) []ID {
	if ints == nil {
		return nil
	}
	out := make([]ID, 0, len(ints))
	for _, v := range ints {
		out = append(out, IDFromInt(int64(v)))
	}
	return out
}

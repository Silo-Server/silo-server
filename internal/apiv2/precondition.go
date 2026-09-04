package apiv2

import (
	"errors"
	"hash/fnv"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// Optimistic concurrency on the wire (docs/architecture/api-contract.md,
// "Optimistic concurrency"): RFC 9110 entity tags, the If-Match /
// If-None-Match list grammar, strong and weak comparison, and the mapping of
// each precondition outcome onto the problem catalog. Silo renders only
// strong tags; the parser accepts weak ones because clients may send them.
//
// A guarded handler loads the resource first (a missing or hidden resource is
// 404 before any precondition), evaluates If-Match with EvaluateIfMatch, then
// performs the storage compare-and-update; a version mismatch there surfaces
// as ErrStaleVersion and maps to the same 412 through StaleVersionProblem.

// EntityTag is a parsed RFC 9110 entity-tag: the opaque text between the
// quotes and whether the W/ weakness prefix was present.
type EntityTag struct {
	Opaque string
	Weak   bool
}

// IsZero reports whether the tag is the zero value (no tag at all). An empty
// opaque-tag ("") is a valid, non-zero tag only when it was parsed; Silo never
// renders one.
func (t EntityTag) IsZero() bool { return t.Opaque == "" && !t.Weak }

// String renders the tag in wire form: `"opaque"` or `W/"opaque"`.
func (t EntityTag) String() string {
	if t.Weak {
		return `W/"` + t.Opaque + `"`
	}
	return `"` + t.Opaque + `"`
}

// RenderETag produces the strong, quoted validator for one row version of one
// representation scope. The scope keeps differently redacted editor
// representations of the same row from sharing a validator. The text is
// opaque to clients: it is a stable encoding, not the decimal version.
func RenderETag(version int64, scope string) EntityTag {
	h := fnv.New64a()
	_, _ = h.Write([]byte(scope))
	sum := h.Sum64()
	prefix := strconv.FormatUint(sum&0xffffffff, 16)
	for len(prefix) < 8 {
		prefix = "0" + prefix
	}
	// XOR with the scope hash is a bijection in version for a fixed scope, so
	// two versions of one scope never collide, and the text is not the
	// version in plain decimal.
	return EntityTag{Opaque: prefix + "." + strconv.FormatUint(uint64(version)^sum, 36)}
}

// ErrMalformedEntityTag is the parse failure ParseEntityTag and ParseETagList
// return; callers map it to 400 malformed_request without echoing the value.
var ErrMalformedEntityTag = errors.New("apiv2: malformed entity-tag")

// ParseEntityTag parses one entity-tag (RFC 9110 8.8.3):
//
//	entity-tag = [ weak ] opaque-tag
//	weak       = %s"W/"
//	opaque-tag = DQUOTE *etagc DQUOTE
//	etagc      = %x21 / %x23-7E / obs-text
//
// It accepts no surrounding whitespace; the list parser strips OWS.
func ParseEntityTag(s string) (EntityTag, error) {
	var t EntityTag
	if strings.HasPrefix(s, "W/") {
		t.Weak = true
		s = s[2:]
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return EntityTag{}, ErrMalformedEntityTag
	}
	inner := s[1 : len(s)-1]
	for i := 0; i < len(inner); i++ {
		if !isETagChar(inner[i]) {
			return EntityTag{}, ErrMalformedEntityTag
		}
	}
	t.Opaque = inner
	return t, nil
}

// isETagChar reports whether b is etagc: 0x21, 0x23-0x7E, or obs-text
// (0x80-0xFF). It excludes DQUOTE, controls, SP and HTAB. The comma is etagc,
// so a list cannot be split on commas; ParseETagList scans quoted elements.
func isETagChar(b byte) bool {
	return b == 0x21 || (b >= 0x23 && b <= 0x7E) || b >= 0x80
}

// ParseETagList parses an If-Match or If-None-Match field value:
//
//	If-Match = "*" / #entity-tag
//
// following the #-list rule of RFC 9110 5.6.1: elements separated by commas
// with optional whitespace, empty elements tolerated. It returns star=true
// for the bare wildcard; "*" among other elements is malformed. A field
// whose elements are all empty yields an empty list and no error. Multiple
// header lines must already be joined with commas: net/http keeps them as
// separate values and Huma binds the input from Header.Get (first line
// only), so the v2 router's joinPreconditionFields middleware folds repeated
// If-Match / If-None-Match lines into one value before the input is bound.
func ParseETagList(field string) (tags []EntityTag, star bool, err error) {
	if strings.Trim(field, " \t") == "*" {
		return nil, true, nil
	}
	i := 0
	for i < len(field) {
		// OWS, then either a separator (an empty element) or an entity-tag.
		for i < len(field) && (field[i] == ' ' || field[i] == '\t') {
			i++
		}
		if i == len(field) {
			break
		}
		if field[i] == ',' {
			i++
			continue
		}
		start := i
		if strings.HasPrefix(field[i:], "W/") {
			i += 2
		}
		if i == len(field) || field[i] != '"' {
			return nil, false, ErrMalformedEntityTag
		}
		i++
		for i < len(field) && field[i] != '"' {
			i++
		}
		if i == len(field) {
			return nil, false, ErrMalformedEntityTag
		}
		i++ // closing DQUOTE
		t, perr := ParseEntityTag(field[start:i])
		if perr != nil {
			return nil, false, perr
		}
		tags = append(tags, t)
		// After an element: OWS, then a comma or the end of the field.
		for i < len(field) && (field[i] == ' ' || field[i] == '\t') {
			i++
		}
		if i < len(field) {
			if field[i] != ',' {
				return nil, false, ErrMalformedEntityTag
			}
			i++
		}
	}
	return tags, false, nil
}

// StrongMatch is RFC 9110 8.8.3.2 strong comparison: both tags are strong
// and their opaque text is byte-equal.
func StrongMatch(a, b EntityTag) bool {
	return !a.Weak && !b.Weak && a.Opaque == b.Opaque
}

// WeakMatch is RFC 9110 8.8.3.2 weak comparison: the opaque text is
// byte-equal regardless of weakness.
func WeakMatch(a, b EntityTag) bool {
	return a.Opaque == b.Opaque
}

const (
	ifMatchField     = "If-Match"
	ifNoneMatchField = "If-None-Match"
	etagField        = "ETag"
)

// EvaluateIfMatch decides a guarded mutation's precondition once the resource
// is known to exist. It returns nil when the field is "*" or lists a strong
// tag equal to current; 428 precondition_required when the field is absent;
// 412 precondition_failed carrying the current ETag when no tag matches (a
// weak tag never matches); 400 malformed_request when the field does not
// parse. The rejected value is never echoed.
func EvaluateIfMatch(ifMatch string, current EntityTag) *Problem {
	if ifMatch == "" {
		return NewProblem(TypePreconditionRequired,
			"This operation requires the If-Match header field carrying the resource's current ETag.")
	}
	tags, star, err := ParseETagList(ifMatch)
	if err != nil {
		return malformedETagList(ifMatchField)
	}
	if star {
		return nil
	}
	for _, t := range tags {
		if StrongMatch(t, current) {
			return nil
		}
	}
	return StaleVersionProblem(current)
}

// EvaluateIfNoneMatch evaluates a conditional read. It reports matched=true
// when the field is "*" or any listed tag weakly matches current, in which
// case the handler answers 304 (see NotModified). A field that does not
// parse is 400 malformed_request; an absent field matches nothing.
func EvaluateIfNoneMatch(ifNoneMatch string, current EntityTag) (matched bool, p *Problem) {
	if ifNoneMatch == "" {
		return false, nil
	}
	tags, star, err := ParseETagList(ifNoneMatch)
	if err != nil {
		return false, malformedETagList(ifNoneMatchField)
	}
	if star {
		return true, nil
	}
	for _, t := range tags {
		if WeakMatch(t, current) {
			return true, nil
		}
	}
	return false, nil
}

// EvaluateCreateOnly evaluates If-None-Match on a write with a
// client-selected resource ID. existing is nil when no resource is stored at
// that ID. "*" against an existing resource, or any weak match against it,
// is 412 precondition_failed with the existing ETag; an absent field is nil;
// a field that does not parse is 400 malformed_request.
func EvaluateCreateOnly(ifNoneMatch string, existing *EntityTag) *Problem {
	if ifNoneMatch == "" {
		return nil
	}
	tags, star, err := ParseETagList(ifNoneMatch)
	if err != nil {
		return malformedETagList(ifNoneMatchField)
	}
	if existing == nil {
		return nil
	}
	if star {
		return NewProblem(TypePreconditionFailed, "A resource already exists at this identifier.").
			WithHeader(etagField, existing.String())
	}
	for _, t := range tags {
		if WeakMatch(t, *existing) {
			return NewProblem(TypePreconditionFailed, "A resource already exists at this identifier.").
				WithHeader(etagField, existing.String())
		}
	}
	return nil
}

func malformedETagList(field string) *Problem {
	return NewProblem(TypeMalformedRequest, "The "+field+" header field is not a valid entity-tag list.").
		WithErrors(ProblemError{Location: "header." + field, Code: "invalid_entity_tag",
			Detail: "expected \"*\" or a comma-separated list of quoted entity-tags"})
}

// ErrStaleVersion is what a storage compare-and-update returns when the row
// version it was told to expect is no longer current. The handler maps it
// with StaleVersionProblem; it never reaches the wire as a 500.
var ErrStaleVersion = errors.New("apiv2: row version is stale")

// StaleVersionProblem is the 412 precondition_failed a stale If-Match or a
// lost compare-and-update race produces. current is the ETag the caller may
// see; pass the zero value to omit the header when the caller may not.
func StaleVersionProblem(current EntityTag) *Problem {
	p := NewProblem(TypePreconditionFailed,
		"The resource changed since the version named by If-Match; fetch it again and retry with the current ETag.")
	if !current.IsZero() {
		p = p.WithHeader(etagField, current.String())
	}
	return p
}

// NotModified turns a conditional read's output into the 304 answer: Status
// becomes 304, the ETag header field the current tag, and the body stays
// zero; Huma writes no body on 304 and the listener drops Content-Type. The
// output shape (a direct int Status and a string `header:"ETag"`) is what
// Register requires of a Conditional operation, so a type that reaches here
// has both.
func NotModified[O any](out *O, current EntityTag) *O {
	v := reflect.ValueOf(out).Elem()
	v.FieldByName("Status").SetInt(http.StatusNotModified)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if strings.EqualFold(t.Field(i).Tag.Get("header"), etagField) {
			v.Field(i).SetString(current.String())
		}
	}
	return out
}

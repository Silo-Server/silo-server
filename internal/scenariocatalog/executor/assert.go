package executor

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// Body kinds and the predicate ops the engine special-cases by name.
const (
	bodyKindEmpty = "empty"
	bodyKindAny   = "any"
	bodyKindText  = "text"

	opEmpty     = "empty"
	opNonEmpty  = "non_empty"
	opLength    = "length"
	opMinLength = "min_length"
	opMaxLength = "max_length"
	opEvery     = "every"
	opNone      = "none"
	opSorted    = "sorted"
	opUniqueBy  = "unique_by"
)

// response is what the assertion engine sees: status, headers, the raw body,
// and the decoded body when the scenario declares it JSON.
type response struct {
	Status  int
	Headers http.Header
	Raw     []byte
	Doc     any
	IsJSON  bool
}

// check evaluates every assertion and returns each failure as one line.
func check(exp scenariocatalog.Expect, resp response) []string {
	var failures []string
	if resp.Status != exp.Status {
		failures = append(failures, fmt.Sprintf("status = %d, want %d", resp.Status, exp.Status))
	}
	for _, h := range exp.Headers {
		if msg := checkHeader(h, resp.Headers); msg != "" {
			failures = append(failures, msg)
		}
	}
	switch exp.BodyKind {
	case bodyKindEmpty:
		if len(resp.Raw) != 0 {
			failures = append(failures, fmt.Sprintf("body = %d bytes, want empty", len(resp.Raw)))
		}
		return failures
	case bodyKindAny:
		return failures
	case bodyKindText:
		// A text body is the raw bytes at pointer "", even when they happen
		// to parse as JSON; an empty body is not a text body.
		if len(resp.Raw) == 0 {
			failures = append(failures, "body is empty, want text")
			return failures
		}
		resp.Doc = string(resp.Raw)
	default:
		// json (the default): the body must decode, or every scenario that
		// carries no body predicate would accept HTML or an empty body.
		if !resp.IsJSON {
			failures = append(failures, fmt.Sprintf("body is not JSON (%d bytes), want a JSON body", len(resp.Raw)))
			return failures
		}
	}
	sized := sizedPointers(exp.Body)
	for _, a := range exp.Body {
		if msg := checkBody(a, resp.Doc, sized[a.Pointer]); msg != "" {
			failures = append(failures, msg)
		}
	}
	return failures
}

// sizedPointers collects the pointers whose array size the scenario pins
// with its own predicate. The collection predicates (every, none, sorted,
// unique_by) are vacuous on short arrays and only accept one when the
// scenario says that size is the point.
func sizedPointers(body []scenariocatalog.BodyAssertion) map[string]bool {
	out := map[string]bool{}
	for _, a := range body {
		switch a.Op {
		case opEmpty, opLength, opMinLength, opNonEmpty:
			out[a.Pointer] = true
		}
	}
	return out
}

// minElements is the smallest array a collection predicate says anything
// about: one element for every/none, two for an ordering or uniqueness.
func minElements(op string) int {
	if op == opEvery || op == opNone {
		return 1
	}
	return 2
}

func checkHeader(h scenariocatalog.HeaderAssertion, headers http.Header) string {
	values := headers.Values(h.Name)
	got := ""
	if len(values) > 0 {
		got = values[0]
	}
	switch h.Op {
	case "exists":
		if len(values) == 0 {
			return fmt.Sprintf("header %s: absent, want present", h.Name)
		}
	case "absent":
		if len(values) != 0 {
			return fmt.Sprintf("header %s = %q, want absent", h.Name, got)
		}
	case "equals":
		if got != h.Value {
			return fmt.Sprintf("header %s = %q, want %q", h.Name, got, h.Value)
		}
	case "contains":
		if !strings.Contains(got, h.Value) {
			return fmt.Sprintf("header %s = %q, want it to contain %q", h.Name, got, h.Value)
		}
	case "matches":
		re, err := regexp.Compile(h.Value)
		if err != nil {
			return fmt.Sprintf("header %s: bad pattern %q: %v", h.Name, h.Value, err)
		}
		if !re.MatchString(got) {
			return fmt.Sprintf("header %s = %q, want match %q", h.Name, got, h.Value)
		}
	default:
		return fmt.Sprintf("header %s: unknown op %q", h.Name, h.Op)
	}
	return ""
}

// checkBody evaluates one predicate. sized says the scenario pins the size
// of the array at the pointer, which lets a collection predicate accept an
// array too short to test anything.
func checkBody(a scenariocatalog.BodyAssertion, doc any, sized bool) string {
	label := "body" + a.Pointer
	if a.Why != "" {
		label += " (" + a.Why + ")"
	}
	got, found := resolvePointer(doc, a.Pointer)
	switch a.Op {
	case opEvery, opNone, opSorted, opUniqueBy:
		if arr, ok := got.([]any); found && ok && len(arr) < minElements(a.Op) && !sized {
			return fmt.Sprintf("%s: %s over %d element(s) is vacuous; pin the size with empty/length/min_length on the same pointer if that is intended", label, a.Op, len(arr))
		}
	}
	var want any
	if len(a.Value) > 0 {
		if err := json.Unmarshal(a.Value, &want); err != nil {
			return fmt.Sprintf("%s: bad expected value: %v", label, err)
		}
	}
	switch a.Op {
	case "exists":
		if !found {
			return fmt.Sprintf("%s: absent, want present", label)
		}
	case "absent":
		if found {
			return fmt.Sprintf("%s = %s, want absent", label, short(got))
		}
	case "is_null":
		if !found || got != nil {
			return fmt.Sprintf("%s = %s, want null", label, short(got))
		}
	case "not_null":
		if !found || got == nil {
			return fmt.Sprintf("%s: null or absent, want a value", label)
		}
	case "equals":
		if !found || !reflect.DeepEqual(got, want) {
			return fmt.Sprintf("%s = %s, want %s", label, short(got), short(want))
		}
	case "not_equals":
		if found && reflect.DeepEqual(got, want) {
			return fmt.Sprintf("%s = %s, want anything else", label, short(got))
		}
	case "type":
		if !found {
			return fmt.Sprintf("%s: absent, want type %v", label, want)
		}
		if t := jsonType(got); t != want && (want != "number" || t != "integer") {
			return fmt.Sprintf("%s: type %s, want %v", label, t, want)
		}
	case opEmpty:
		if !found {
			return fmt.Sprintf("%s: absent, want empty collection", label)
		}
		if n, ok := length(got); !ok || n != 0 {
			return fmt.Sprintf("%s = %s, want empty", label, short(got))
		}
	case opNonEmpty:
		if n, ok := length(got); !found || !ok || n == 0 {
			return fmt.Sprintf("%s = %s, want non-empty", label, short(got))
		}
	case opLength, opMinLength, opMaxLength:
		n, ok := length(got)
		wantN, isNum := want.(float64)
		if !found || !ok || !isNum {
			return fmt.Sprintf("%s = %s, cannot take its length", label, short(got))
		}
		switch a.Op {
		case opLength:
			if float64(n) != wantN {
				return fmt.Sprintf("%s: length %d, want %v", label, n, wantN)
			}
		case opMinLength:
			if float64(n) < wantN {
				return fmt.Sprintf("%s: length %d, want at least %v", label, n, wantN)
			}
		case opMaxLength:
			if float64(n) > wantN {
				return fmt.Sprintf("%s: length %d, want at most %v", label, n, wantN)
			}
		}
	case "matches":
		s, ok := got.(string)
		pat, _ := want.(string)
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Sprintf("%s: bad pattern %q: %v", label, pat, err)
		}
		if !found || !ok || !re.MatchString(s) {
			return fmt.Sprintf("%s = %s, want match %q", label, short(got), pat)
		}
	case "one_of":
		options, ok := want.([]any)
		if !ok {
			return fmt.Sprintf("%s: one_of needs an array", label)
		}
		for _, o := range options {
			if found && reflect.DeepEqual(got, o) {
				return ""
			}
		}
		return fmt.Sprintf("%s = %s, want one of %s", label, short(got), short(want))
	case "contains":
		if !found || !containsValue(got, want) {
			return fmt.Sprintf("%s = %s, want it to contain %s", label, short(got), short(want))
		}
	case "gte", "lte":
		cmp, ok := compare(got, want)
		if !found || !ok {
			return fmt.Sprintf("%s = %s, not comparable with %s", label, short(got), short(want))
		}
		if a.Op == "gte" && cmp < 0 {
			return fmt.Sprintf("%s = %s, want >= %s", label, short(got), short(want))
		}
		if a.Op == "lte" && cmp > 0 {
			return fmt.Sprintf("%s = %s, want <= %s", label, short(got), short(want))
		}
	case "rfc3339":
		s, ok := got.(string)
		if !found || !ok {
			return fmt.Sprintf("%s = %s, want an RFC 3339 string", label, short(got))
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			return fmt.Sprintf("%s = %q, not RFC 3339: %v", label, s, err)
		}
	case "keys_equal", "keys_include":
		obj, ok := got.(map[string]any)
		if !found || !ok {
			return fmt.Sprintf("%s = %s, want an object", label, short(got))
		}
		wantKeys := stringList(want)
		gotKeys := make([]string, 0, len(obj))
		for k := range obj {
			gotKeys = append(gotKeys, k)
		}
		sort.Strings(gotKeys)
		sort.Strings(wantKeys)
		if a.Op == "keys_equal" {
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				return fmt.Sprintf("%s: keys %v, want exactly %v", label, gotKeys, wantKeys)
			}
		} else {
			for _, k := range wantKeys {
				if _, ok := obj[k]; !ok {
					return fmt.Sprintf("%s: keys %v, missing %q", label, gotKeys, k)
				}
			}
		}
	case opSorted:
		return checkSorted(label, got, found, want)
	case opNone:
		arr, ok := got.([]any)
		if !found || !ok {
			return fmt.Sprintf("%s = %s, want an array", label, short(got))
		}
		var inner scenariocatalog.BodyAssertion
		if err := json.Unmarshal(a.Value, &inner); err != nil {
			return fmt.Sprintf("%s: bad nested assertion: %v", label, err)
		}
		for i, el := range arr {
			if msg := checkBody(inner, el, false); msg == "" {
				return fmt.Sprintf("%s[%d] = %s satisfies %s at %s, want no element to", label, i, short(el), inner.Op, "body"+inner.Pointer)
			}
		}
	case opUniqueBy:
		arr, ok := got.([]any)
		by, _ := want.(string)
		if !found || !ok {
			return fmt.Sprintf("%s = %s, want an array", label, short(got))
		}
		seen := map[string]int{}
		for i, el := range arr {
			v, _ := resolvePointer(el, by)
			key := short(v)
			if prev, dup := seen[key]; dup {
				return fmt.Sprintf("%s: elements %d and %d share %s=%s", label, prev, i, by, key)
			}
			seen[key] = i
		}
	case opEvery:
		arr, ok := got.([]any)
		if !found || !ok {
			return fmt.Sprintf("%s = %s, want an array", label, short(got))
		}
		var inner scenariocatalog.BodyAssertion
		if err := json.Unmarshal(a.Value, &inner); err != nil {
			return fmt.Sprintf("%s: bad nested assertion: %v", label, err)
		}
		for i, el := range arr {
			if msg := checkBody(inner, el, false); msg != "" {
				return fmt.Sprintf("%s[%d]: %s", label, i, msg)
			}
		}
	default:
		return fmt.Sprintf("%s: unknown op %q", label, a.Op)
	}
	return ""
}

type sortSpec struct {
	By            string `json:"by"`
	Direction     string `json:"direction"`
	ThenBy        string `json:"then_by"`
	ThenDirection string `json:"then_direction"`
	Nulls         string `json:"nulls"`
}

func checkSorted(label string, got any, found bool, want any) string {
	arr, ok := got.([]any)
	if !found || !ok {
		return fmt.Sprintf("%s = %s, want an array", label, short(got))
	}
	raw, _ := json.Marshal(want)
	var spec sortSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Sprintf("%s: bad sort spec: %v", label, err)
	}
	if spec.ThenDirection == "" {
		spec.ThenDirection = "asc"
	}
	nullsSeen := false
	for i := 1; i < len(arr); i++ {
		prev, _ := resolvePointer(arr[i-1], spec.By)
		cur, _ := resolvePointer(arr[i], spec.By)
		if prev == nil || cur == nil {
			nullsSeen = true
			switch spec.Nulls {
			case "first":
				if prev != nil && cur == nil {
					return fmt.Sprintf("%s: null %s after a value at index %d, want nulls first", label, spec.By, i)
				}
			case "last":
				if prev == nil && cur != nil {
					return fmt.Sprintf("%s: value %s after a null at index %d, want nulls last", label, spec.By, i)
				}
			default:
				return fmt.Sprintf("%s: null %s at index %d but the sort spec declares no null placement", label, spec.By, i)
			}
			continue
		}
		cmp, ok := compare(prev, cur)
		if !ok {
			return fmt.Sprintf("%s: %s values %s and %s are not comparable", label, spec.By, short(prev), short(cur))
		}
		if spec.Direction == "desc" {
			cmp = -cmp
		}
		if cmp > 0 {
			return fmt.Sprintf("%s: not sorted by %s %s at index %d (%s then %s)", label, spec.By, spec.Direction, i, short(prev), short(cur))
		}
		if cmp == 0 && spec.ThenBy != "" {
			p2, _ := resolvePointer(arr[i-1], spec.ThenBy)
			c2, _ := resolvePointer(arr[i], spec.ThenBy)
			cmp2, ok := compare(p2, c2)
			if !ok {
				return fmt.Sprintf("%s: tie-breaker %s values %s and %s are not comparable", label, spec.ThenBy, short(p2), short(c2))
			}
			if spec.ThenDirection == "desc" {
				cmp2 = -cmp2
			}
			if cmp2 > 0 {
				return fmt.Sprintf("%s: tie at index %d not broken by %s %s", label, i, spec.ThenBy, spec.ThenDirection)
			}
		}
	}
	_ = nullsSeen
	return ""
}

// resolvePointer walks an RFC 6901 pointer through a decoded JSON document.
func resolvePointer(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	cur := doc
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func jsonType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if t == math.Trunc(t) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func length(v any) (int, bool) {
	switch t := v.(type) {
	case string:
		return utf8.RuneCountInString(t), true
	case []any:
		return len(t), true
	case map[string]any:
		return len(t), true
	}
	return 0, false
}

func containsValue(got, want any) bool {
	switch t := got.(type) {
	case []any:
		for _, el := range t {
			if reflect.DeepEqual(el, want) {
				return true
			}
		}
	case string:
		s, ok := want.(string)
		return ok && strings.Contains(t, s)
	case map[string]any:
		k, ok := want.(string)
		if !ok {
			return false
		}
		_, has := t[k]
		return has
	}
	return false
}

// compare orders two scalar JSON values: numbers numerically, strings
// lexically, booleans false<true.
func compare(a, b any) (int, bool) {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(x, y), true
	case bool:
		y, ok := b.(bool)
		if !ok {
			return 0, false
		}
		switch {
		case x == y:
			return 0, true
		case !x:
			return -1, true
		}
		return 1, true
	}
	return 0, false
}

func stringList(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		if s, ok := el.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func short(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(raw)
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

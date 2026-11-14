// Package jsonsize bounds what decoding an untrusted JSON document into a
// Go value allocates, before it is decoded. A short document can decode to
// far more memory than it holds: `[{},{},…]` is three bytes an element,
// and each element becomes a whole struct. Check walks the document once
// beside the target type, without decoding it, and adds up what
// encoding/json would allocate for it; a document over its bound is
// refused before anything is allocated.
package jsonsize

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Limits bound one document.
type Limits struct {
	// Depth bounds how deeply arrays and objects nest.
	Depth int
	// Bytes bounds the estimated memory the decoded value takes.
	Bytes int64
	// Elements bounds the elements of any one array and the members of any
	// one object.
	Elements int
}

// ErrTooLarge refuses a document over its Limits.
var ErrTooLarge = errors.New("JSON document decodes to more than this reader accepts")

// Check returns the estimated bytes decoding data into a value of into's
// type allocates, and refuses a document over l. It counts each array
// element and map entry at its Go type's size (doubled for a slice's
// growth), each pointer's target, each string's bytes, and a
// json.RawMessage's bytes; members the type does not have count as raw
// bytes, since a strict decoder refuses them. It does not otherwise
// validate the JSON: the decoder that follows does.
func Check(data []byte, into any, l Limits) (int64, error) {
	t := reflect.TypeOf(into)
	if t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s := &scanner{data: data, limits: l}
	s.skipSpace()
	if err := s.value(t, 0); err != nil {
		return s.bytes, err
	}
	return s.bytes, nil
}

type scanner struct {
	data   []byte
	i      int
	bytes  int64
	limits Limits
}

// size is t's size as a byte count; a type's size always fits.
func size(t reflect.Type) int64 { return int64(t.Size()) } //nolint:gosec // G115: a Go type's size fits in int64

var (
	rawMessageType = reflect.TypeFor[json.RawMessage]()
	errSyntax      = errors.New("malformed JSON")
)

func (s *scanner) add(n int64) error {
	s.bytes += n
	if s.bytes > s.limits.Bytes {
		return fmt.Errorf("%w: over %d bytes", ErrTooLarge, s.limits.Bytes)
	}
	return nil
}

func (s *scanner) skipSpace() {
	for s.i < len(s.data) {
		switch s.data[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// value scans one value decoded into t (nil: kept raw or refused).
func (s *scanner) value(t reflect.Type, depth int) error {
	if s.i >= len(s.data) {
		return errSyntax
	}
	for t != nil && t.Kind() == reflect.Pointer {
		if s.data[s.i] == 'n' {
			break
		}
		if err := s.add(size(t.Elem())); err != nil {
			return err
		}
		t = t.Elem()
	}
	if t == rawMessageType || t != nil && t.Kind() == reflect.Interface {
		// A RawMessage holds the value's bytes; an interface value is
		// decoded generically, at no less than its bytes again.
		start := s.i
		if err := s.value(nil, depth); err != nil {
			return err
		}
		return s.add(int64(s.i - start))
	}
	switch s.data[s.i] {
	case '{':
		return s.object(t, depth+1)
	case '[':
		return s.array(t, depth+1)
	case '"':
		start := s.i
		if _, _, err := s.str(); err != nil {
			return err
		}
		if t != nil && t.Kind() == reflect.String {
			return s.add(int64(s.i - start))
		}
		return nil
	default:
		return s.literal()
	}
}

func (s *scanner) enter(depth int) error {
	if depth > s.limits.Depth {
		return fmt.Errorf("%w: nested deeper than %d", ErrTooLarge, s.limits.Depth)
	}
	return nil
}

func (s *scanner) count(n int) error {
	if n > s.limits.Elements {
		return fmt.Errorf("%w: more than %d elements in one array or object", ErrTooLarge, s.limits.Elements)
	}
	return nil
}

func (s *scanner) array(t reflect.Type, depth int) error {
	if err := s.enter(depth); err != nil {
		return err
	}
	var elem reflect.Type
	var each int64
	if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
		elem = t.Elem()
		each = size(elem)
		if t.Kind() == reflect.Slice {
			each *= 2 // append grows a slice by up to twice what it holds
		}
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.data) && s.data[s.i] == ']' {
		s.i++
		return nil
	}
	for n := 1; ; n++ {
		if err := s.count(n); err != nil {
			return err
		}
		if err := s.add(each); err != nil {
			return err
		}
		if err := s.value(elem, depth); err != nil {
			return err
		}
		s.skipSpace()
		if s.i >= len(s.data) {
			return errSyntax
		}
		switch s.data[s.i] {
		case ',':
			s.i++
			s.skipSpace()
		case ']':
			s.i++
			return nil
		default:
			return errSyntax
		}
	}
}

func (s *scanner) object(t reflect.Type, depth int) error {
	if err := s.enter(depth); err != nil {
		return err
	}
	var fields map[string]reflect.Type
	var mapEntry int64
	var mapElem reflect.Type
	if t != nil {
		switch t.Kind() {
		case reflect.Struct:
			fields = structFields(t)
		case reflect.Map:
			mapElem = t.Elem()
			// A map entry's key and value, and the map's own overhead
			// for it, about as much again.
			mapEntry = 2 * (size(t.Key()) + size(t.Elem()))
		}
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.data) && s.data[s.i] == '}' {
		s.i++
		return nil
	}
	for n := 1; ; n++ {
		if err := s.count(n); err != nil {
			return err
		}
		if s.i >= len(s.data) || s.data[s.i] != '"' {
			return errSyntax
		}
		start := s.i
		key, escaped, err := s.str()
		if err != nil {
			return err
		}
		var member reflect.Type
		switch {
		case fields != nil:
			member = field(fields, key, escaped, s.data[start:s.i])
		case mapElem != nil:
			member = mapElem
			if err := s.add(mapEntry + int64(s.i-start)); err != nil {
				return err
			}
		}
		s.skipSpace()
		if s.i >= len(s.data) || s.data[s.i] != ':' {
			return errSyntax
		}
		s.i++
		s.skipSpace()
		if err := s.value(member, depth); err != nil {
			return err
		}
		s.skipSpace()
		if s.i >= len(s.data) {
			return errSyntax
		}
		switch s.data[s.i] {
		case ',':
			s.i++
			s.skipSpace()
		case '}':
			s.i++
			return nil
		default:
			return errSyntax
		}
	}
}

// str scans a string and returns its bytes between the quotes, and
// whether it holds an escape.
func (s *scanner) str() (raw []byte, escaped bool, err error) {
	s.i++
	start := s.i
	for s.i < len(s.data) {
		switch s.data[s.i] {
		case '\\':
			escaped = true
			s.i += 2
		case '"':
			raw = s.data[start:s.i]
			s.i++
			return raw, escaped, nil
		default:
			s.i++
		}
	}
	return nil, false, errSyntax
}

func (s *scanner) literal() error {
	start := s.i
	for s.i < len(s.data) {
		switch s.data[s.i] {
		case ',', ']', '}', ' ', '\t', '\n', '\r':
			if s.i == start {
				return errSyntax
			}
			return nil
		}
		s.i++
	}
	if s.i == start {
		return errSyntax
	}
	return nil
}

// field is the type of the struct field encoding/json fills from key, or
// nil. encoding/json prefers an exact name and else takes a
// case-insensitive match; strings.EqualFold accepts at least every name it
// does, so a member is never counted as unknown when it is decoded.
func field(fields map[string]reflect.Type, key []byte, escaped bool, quoted []byte) reflect.Type {
	name := string(key)
	if escaped {
		if err := json.Unmarshal(quoted, &name); err != nil {
			return nil
		}
	}
	if t, ok := fields[name]; ok {
		return t
	}
	for candidate, t := range fields {
		if strings.EqualFold(candidate, name) {
			return t
		}
	}
	return nil
}

var fieldCache sync.Map // reflect.Type -> map[string]reflect.Type

// structFields maps the JSON names of t's fields, embedded structs' fields
// included, to their types.
func structFields(t reflect.Type) map[string]reflect.Type {
	if cached, ok := fieldCache.Load(t); ok {
		if fields, ok := cached.(map[string]reflect.Type); ok {
			return fields
		}
	}
	fields := map[string]reflect.Type{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				embedded := f.Type
				if embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					walk(embedded)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if _, taken := fields[name]; !taken {
				fields[name] = f.Type
			}
		}
	}
	walk(t)
	fieldCache.Store(t, fields)
	return fields
}

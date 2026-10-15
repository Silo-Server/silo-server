package queryattr

import (
	"context"
	"runtime"
	"strings"
)

const (
	modulePrefix                  = "github.com/Silo-Server/silo-server/"
	personalCollectionCountsLabel = "catalog.personal_collection_counts"
)

// Label is a reviewed, compile-time caller replacement.
type Label uint8

const (
	LabelPersonalCollectionCounts Label = iota + 1
	labelTest
)

type labelKey struct{}

// WithLabel applies a reviewed caller label to all operations under ctx.
func WithLabel(ctx context.Context, label Label) context.Context {
	return context.WithValue(ctx, labelKey{}, label)
}
func labelName(label Label) string {
	switch label {
	case LabelPersonalCollectionCounts:
		return personalCollectionCountsLabel
	case labelTest:
		return "queryattr.test"
	}
	return ""
}
func displayName(name string) string {
	name = strings.TrimPrefix(name, modulePrefix)
	name = strings.TrimPrefix(name, "internal/")
	return name
}
func skipFunction(name string) bool {
	return !strings.HasPrefix(name, modulePrefix) && !strings.HasPrefix(name, "main.") || strings.HasPrefix(name, modulePrefix+"internal/database")
}
func (s *Store) resolvePC(pc uintptr) Caller {
	s.mu.Lock()
	c, ok := s.pcs[pc]
	s.mu.Unlock()
	if ok {
		return c
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	for {
		f, more := frames.Next()
		if !skipFunction(f.Function) {
			c = Caller{displayName(f.Function), f.Line}
			break
		}
		if !more {
			break
		}
	}
	s.mu.Lock()
	if len(s.pcs) >= s.pcCap {
		clear(s.pcs)
	}
	s.pcs[pc] = c
	s.mu.Unlock()
	return c
}
func (s *Store) caller(ctx context.Context, st *connectionState) Caller {
	if l, ok := ctx.Value(labelKey{}).(Label); ok {
		name := labelName(l)
		if name == "" {
			s.gap(cAttributionFailures)
		}
		return Caller{Function: name}
	}
	var local [16]uintptr
	pcs := local[:]
	if st != nil {
		pcs = st.pcs[:]
	}
	n := runtime.Callers(3, pcs)
	for _, pc := range pcs[:n] {
		if c := s.resolvePC(pc); c.Function != "" {
			return c
		}
	}
	if n == len(pcs) {
		var deep [64]uintptr
		n = runtime.Callers(3, deep[:])
		for _, pc := range deep[:n] {
			if c := s.resolvePC(pc); c.Function != "" {
				return c
			}
		}
	}
	return Caller{}
}

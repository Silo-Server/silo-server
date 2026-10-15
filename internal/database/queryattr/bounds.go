package queryattr

import (
	"sync"
	"sync/atomic"
)

const (
	IdentityCap    = 8192
	FunctionCap    = 4096
	PCCap          = 16384
	ShapeThreshold = 64
	overflowName   = "overflow"
)

// Caller contains binary-backed code names and a display-only line, never a path.
type Caller struct {
	Function string
	Line     int
}
type Form uint8

const (
	Statement Form = iota
	BatchEnvelope
	Transaction
	Copy
	FunctionOnly
	Unavailable
)

type Identity struct {
	Caller      Caller
	Fingerprint uint64
	Form        Form
	// canonical is stable for the row's lifetime. Request tables retain this
	// pointer instead of copying a full identity into each of their 64 entries.
	canonical *Identity
}
type row struct {
	identity Identity
	counts   counters
}
type identityKey struct {
	function    string
	fingerprint uint64
	form        Form
}
type function struct {
	fallback                                               row
	shapes                                                 uint32
	shapeWarn, overflowWarn, shapePending, overflowPending bool
}

// Store retains bounded identities and complete dense outcome counters.
type Store struct {
	acquireOutcomes                 [ClassCount]atomic.Uint64
	mu                              sync.Mutex
	batchMu                         sync.Mutex
	identities                      map[identityKey]*row
	functions                       map[string]*function
	pcs                             map[uintptr]Caller
	reserved                        [15]row
	identityCap, functionCap, pcCap int
}

func NewStore() *Store { return newStore(IdentityCap, FunctionCap, PCCap) }
func newStore(i, f, p int) *Store {
	s := &Store{identities: make(map[identityKey]*row), functions: make(map[string]*function), pcs: make(map[uintptr]Caller), identityCap: i, functionCap: f, pcCap: p}
	for j, name := range [...]string{"node", "background", "request", "internal", "caller_unavailable", "telemetry_persistence", "connection_setup", "failed_acquire", "empty_batch", "copy_setup_failure", "unobserved_batch", "connection_closed_unobserved", "origin_untracked", "attribution_failure", "duplicate_end"} {
		s.reserved[j].identity = Identity{Caller: Caller{Function: name}, Form: Unavailable}
		s.reserved[j].identity.canonical = &s.reserved[j].identity
	}
	return s
}

var Default = NewStore()

func (s *Store) lookup(c Caller, fp uint64, form Form) *row {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := identityKey{c.Function, fp, form}
	if r := s.identities[k]; r != nil {
		return r
	}
	if c.Function == "" {
		return &s.reserved[4]
	}
	f := s.functions[c.Function]
	if f == nil {
		if len(s.functions) >= s.functionCap {
			return &s.reserved[4]
		}
		f = &function{fallback: row{identity: Identity{Caller: c, Form: FunctionOnly}}}
		f.fallback.identity.canonical = &f.fallback.identity
		s.functions[c.Function] = f
	}
	if len(s.identities) >= s.identityCap {
		if !f.overflowWarn {
			f.overflowWarn = true
			f.overflowPending = true
		}
		return &f.fallback
	}
	r := &row{identity: Identity{Caller: c, Fingerprint: fp, Form: form}}
	r.identity.canonical = &r.identity
	s.identities[k] = r
	f.shapes++
	if f.shapes > ShapeThreshold && !f.shapeWarn {
		f.shapeWarn = true
		f.shapePending = true
	}
	return r
}

// RowSnapshot is a detached accounting view without SQL, values or file paths.
type RowSnapshot struct {
	Identity Identity
	Counters Counters
}

// Snapshot returns independent values. Callbacks never perform output work.
func (s *Store) Snapshot() []RowSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RowSnapshot, 0, len(s.identities)+len(s.functions)+len(s.reserved))
	for _, r := range s.identities {
		out = append(out, RowSnapshot{r.identity, r.counts.snapshot()})
	}
	for _, f := range s.functions {
		out = append(out, RowSnapshot{f.fallback.identity, f.fallback.counts.snapshot()})
	}
	for i := range s.reserved {
		c := s.reserved[i].counts.snapshot()
		if i == 0 {
			c = s.Totals()
		}
		out = append(out, RowSnapshot{s.reserved[i].identity, c})
	}
	return out
}

// Warning describes a bounded, pending warning; draining performs no logging.
type Warning struct{ Caller, Kind string }

func (s *Store) DrainWarnings() []Warning {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Warning
	for name, f := range s.functions {
		if f.overflowPending {
			out = append(out, Warning{name, overflowName})
			f.overflowPending = false
		}
		if f.shapePending {
			out = append(out, Warning{name, "shapes"})
			f.shapePending = false
		}
	}
	return out
}

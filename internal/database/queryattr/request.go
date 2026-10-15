package queryattr

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

const (
	RequestIdentityCap = 64
	RepeatThreshold    = 10
	noQueryValue       = "none"
)

// RequestScope counts all request work and retains at most 64 identities for
// repeat detection. Seal freezes its snapshot; later work changes coverage only.
type RequestScope struct {
	owner            ownership
	mu               sync.Mutex
	snapshot         RequestSnapshot
	entries          [RequestIdentityCap]requestEntry
	acquireBalance   int64
	acquires         uint64
	used             uint8
	sealed, hijacked bool
}

type requestEntry struct {
	identity *Identity
	count    uint64
}

// RequestSnapshot contains integer-nanosecond sums, not request wall time.
// InFlight counts statements, including queued but unfinished batch children.
type RequestSnapshot struct {
	Queries, Batches, QueryNS, AcquireWaitNS uint64
	Slowest                                  Identity
	SlowestNS, Repeated, InFlight            uint64
	Incomplete                               bool
	enabled                                  bool
}

// RequestCounters are complete process-lifetime coverage counters.
type RequestCounters struct{ LateCompletions, LateRequestStarts, IncompleteRequests uint64 }

var lateCompletions, lateRequestStarts, incompleteRequests atomic.Uint64

func RequestCoverage() RequestCounters {
	return RequestCounters{lateCompletions.Load(), lateRequestStarts.Load(), incompleteRequests.Load()}
}

// StartRequest reuses an unsealed request owner. With attribution disabled it
// returns ctx unchanged and a nil scope, with no allocation.
func StartRequest(ctx context.Context) (context.Context, *RequestScope) {
	if !Enabled() {
		return ctx, nil
	}
	if own := ownershipFrom(ctx); own.origin == RequestOrigin {
		if scope, ok := own.scope.(*RequestScope); ok {
			scope.mu.Lock()
			sealed := scope.sealed
			scope.mu.Unlock()
			if !sealed {
				return ctx, scope
			}
		}
	}
	scope := &RequestScope{snapshot: RequestSnapshot{enabled: true}}
	scope.owner = ownership{Context: ctx, origin: RequestOrigin, scope: scope}
	return &scope.owner, scope
}

func (s *RequestScope) Start(id Identity, kind Kind, n uint64, _ Sizes, started int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		lateRequestStarts.Add(1)
		return
	}
	if kind == Acquire {
		s.acquireBalance -= started
		s.acquires++
		return
	}
	s.snapshot.Queries += n
	s.snapshot.InFlight += n
	if kind == Batch {
		s.snapshot.Batches++
		return
	}
	if id.Form == FunctionOnly || id.Caller.Function == "caller_unavailable" || id.canonical == nil {
		s.snapshot.Incomplete = true
		return
	}
	for i := range int(s.used) {
		if s.entries[i].identity == id.canonical {
			s.entries[i].count += n
			return
		}
	}
	if int(s.used) == len(s.entries) {
		s.snapshot.Incomplete = true
		return
	}
	s.entries[s.used] = requestEntry{id.canonical, n}
	s.used++
}

func (s *RequestScope) Finish(o Observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		lateCompletions.Add(1)
		return
	}
	if o.Kind == Acquire {
		s.acquireBalance += o.StartedNS + int64(o.NS)
		s.acquires--
		return
	}
	s.snapshot.InFlight -= o.Statements
	s.snapshot.QueryNS += o.NS
	if o.Child {
		return
	}
	id := o.Identity
	current := s.snapshot.Slowest
	if current.Caller.Function == "" || o.NS > s.snapshot.SlowestNS || o.NS == s.snapshot.SlowestNS && (id.Caller.Function < current.Caller.Function || id.Caller.Function == current.Caller.Function && id.Fingerprint < current.Fingerprint) {
		id.canonical = nil
		s.snapshot.Slowest, s.snapshot.SlowestNS = id, o.NS
	}
}

// Seal returns the same immutable snapshot on every call, including on panic.
// Pending acquires contribute elapsed wait through this seal's monotonic time.
func (s *RequestScope) Seal() RequestSnapshot {
	if s == nil {
		return RequestSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sealed {
		s.sealed = true
		s.snapshot.AcquireWaitNS = uint64(max(s.acquireBalance+int64(s.acquires)*NowNS(), 0))
		s.snapshot.Incomplete = s.snapshot.Incomplete || s.snapshot.InFlight != 0 || s.acquires != 0
		if s.snapshot.Incomplete {
			incompleteRequests.Add(1)
		}
		if !s.hijacked {
			s.snapshot.Repeated = s.repeatedLocked()
		}
		clear(s.entries[:])
	}
	return s.snapshot
}

// Hijack skips repeat detection after a successful takeover. Statements keep
// counting until the request seals, including work after the takeover.
func (s *RequestScope) Hijack() {
	if s != nil {
		s.mu.Lock()
		s.hijacked = true
		s.mu.Unlock()
	}
}

func (s *RequestScope) repeatedLocked() uint64 {
	var n uint64
	for i := range int(s.used) {
		if s.entries[i].count > RepeatThreshold {
			n++
		}
	}
	return n
}

// LogAttrs is the shared request-line projection. Off omits every db_* field;
// an enabled request with no completed query names its slowest query as none.
func (s RequestSnapshot) LogAttrs() []any {
	if !s.enabled {
		return nil
	}
	site, fp := noQueryValue, noQueryValue
	if s.Slowest.Caller.Function != "" {
		site = s.Slowest.Caller.Function
		switch s.Slowest.Form {
		case FunctionOnly:
			fp = overflowName
		case Unavailable, BatchEnvelope:
			fp = "unavailable"
		default:
			fp = fmt.Sprintf("%016x", s.Slowest.Fingerprint)
		}
	}
	return []any{
		"db_queries", s.Queries, "db_batches", s.Batches,
		"db_query_ns", s.QueryNS, "db_acquire_wait_ns", s.AcquireWaitNS,
		"db_slowest_site", site, "db_slowest_fp", fp, "db_slowest_ns", s.SlowestNS,
		"db_repeated", s.Repeated, "db_in_flight", s.InFlight, "db_incomplete", s.Incomplete,
		"db_scheme", Scheme,
	}
}

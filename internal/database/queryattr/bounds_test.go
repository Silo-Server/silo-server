package queryattr

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"unsafe"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func populatedStore() *Store {
	s := NewStore()
	for i := 0; i < FunctionCap; i++ {
		c := Caller{Function: fmt.Sprintf("fixture.function%d", i)}
		s.lookup(c, uint64(i), Statement)
		s.lookup(c, uint64(i+FunctionCap), Statement)
	}
	s.mu.Lock()
	for i := 0; i < PCCap; i++ {
		s.pcs[uintptr(i+1)] = Caller{Function: "fixture.binary_backed", Line: 1}
	}
	s.mu.Unlock()
	for _, r := range s.identities {
		for i := range r.counts.outcomes {
			r.counts.outcomes[i].Store(1)
			r.counts.envelopeOutcomes[i].Store(1)
		}
	}
	for _, f := range s.functions {
		for i := range f.fallback.counts.outcomes {
			f.fallback.counts.outcomes[i].Store(1)
			f.fallback.counts.envelopeOutcomes[i].Store(1)
		}
	}
	for j := range s.reserved {
		for i := range s.reserved[j].counts.outcomes {
			s.reserved[j].counts.outcomes[i].Store(1)
			s.reserved[j].counts.envelopeOutcomes[i].Store(1)
		}
	}
	return s
}
func TestHeapAtCaps(t *testing.T) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s := populatedStore()
	runtime.GC()
	runtime.ReadMemStats(&after)
	fixed := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if fixed > 20<<20 {
		t.Fatalf("fixed heap=%d ceiling=%d", fixed, 20<<20)
	}
	const connections = 100
	const records = 1000
	const requests = 100
	states := make([]*connectionState, connections)
	live := make([]*record, records)
	scopes := make([]*RequestScope, requests)
	previous := enabled.Swap(true)
	t.Cleanup(func() { enabled.Store(previous) })
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range states {
		states[i] = new(connectionState)
	}
	for i := range scopes {
		_, scopes[i] = StartRequest(context.Background())
		for _, r := range s.identities {
			scopes[i].Start(r.identity, Query, 1, Sizes{}, NowNS())
			if int(scopes[i].used) == RequestIdentityCap {
				break
			}
		}
	}
	for i := range live {
		live[i] = &record{Context: &scopes[i%requests].owner, scope: scopes[i%requests], row: &s.reserved[4]}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	variable := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	ceiling := connections*256 + records*96 + requests*1280
	if variable > int64(ceiling) {
		t.Fatalf("variable heap=%d ceiling=%d", variable, ceiling)
	}
	t.Logf("identities=%d functions=%d PCs=%d fixed_heap=%d fixed_ceiling=%d connections=%d live_records=%d scopes=%d variable_heap=%d variable_ceiling=%d row=%d counters=%d function=%d record=%d connection=%d scope=%d", len(s.identities), len(s.functions), len(s.pcs), fixed, 20<<20, connections, records, requests, variable, ceiling, unsafe.Sizeof(row{}), unsafe.Sizeof(counters{}), unsafe.Sizeof(function{}), unsafe.Sizeof(record{}), unsafe.Sizeof(connectionState{}), unsafe.Sizeof(RequestScope{}))
	runtime.KeepAlive(s)
	runtime.KeepAlive(states)
	runtime.KeepAlive(live)
	runtime.KeepAlive(scopes)
}

func BenchmarkRowsWorkCounter(b *testing.B) {
	s := NewStore()
	tr := Tracer{Store: s}
	ctx := context.Background()
	d := pgx.TraceQueryStartData{SQL: "SELECT 1"}
	b.Run("core", func(b *testing.B) {
		for range 10 {
			r := tr.TraceQueryStart(ctx, nil, d)
			tr.TraceQueryEnd(r, nil, pgx.TraceQueryEndData{})
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := tr.TraceQueryStart(ctx, nil, d)
			tr.TraceQueryEnd(r, nil, pgx.TraceQueryEndData{})
		}
	})
	b.Run("rows", func(b *testing.B) {
		tag := pgconn.NewCommandTag("SELECT 123")
		a, c, d := &s.reserved[0].counts, &s.reserved[1].counts, &s.reserved[4].counts
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			n := uint64(tag.RowsAffected())
			a.values[cRows].Add(n)
			c.values[cRows].Add(n)
			d.values[cRows].Add(n)
		}
	})
}

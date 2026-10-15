package queryattr

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgconn"
)

// Outcome indices form a fixed vocabulary; errors are never rendered.
const (
	Success = iota
	Canceled
	Deadline
	PostgresOther
	Client
	Network
	firstSQLClass
)
const ClassCount = 48
const sqlClasses = "00 01 02 03 08 09 0A 0B 0F 0L 0P 0Z 20 21 22 23 24 25 26 27 28 2B 2D 2F 34 38 39 3B 3D 3F 40 42 44 53 54 55 57 58 F0 HV P0 XX"

func classIndex(code string) int {
	for i := 0; i < len(sqlClasses); i += 3 {
		if sqlClasses[i:i+2] == code {
			return firstSQLClass + i/3
		}
	}
	return PostgresOther
}
func outcome(err error) int {
	switch {
	case err == nil:
		return Success
	case errors.Is(err, context.Canceled):
		return Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return Deadline
	}
	var p *pgconn.PgError
	if errors.As(err, &p) {
		if len(p.Code) == 5 {
			for i := range p.Code {
				c := p.Code[i]
				if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
					return PostgresOther
				}
			}
			return classIndex(p.Code[:2])
		}
		return PostgresOther
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return Network
	}
	var n net.Error
	if errors.As(err, &n) {
		return Network
	}
	return Client
}

// Counters separates observed outcomes, resolved-unobserved statements and
// finished batch envelopes. Started = Completed + in-flight statements.
type Counters struct {
	AcquireOutcomes                                                 [ClassCount]uint64
	UnobservedEnvelopes                                             uint64
	Started, Completed, Observed, Unobserved                        uint64
	QueryNS, Rows, Batches, Envelopes                               uint64
	AcquireNS, Acquires, FailedAcquires                             uint64
	UnobservedBatch, ConnectionClosed                               uint64
	DuplicateEnds, AttributionFailures, Overflow, OutcomeDetailLoss uint64
	EmptyBatches, CopySetupFailures, OriginUntracked                uint64
	MaxArgs, MaxArray, MaxList, MaxBatch                            uint64
	Outcomes                                                        [ClassCount]uint64
	EnvelopeOutcomes                                                [ClassCount]uint64
}
type counters struct {
	values                     [25]atomic.Uint64
	outcomes, envelopeOutcomes [ClassCount]atomic.Uint64
}

const (
	cStarted = iota
	cCompleted
	cObserved
	cUnobserved
	cQueryNS
	cRows
	cBatches
	cEnvelopes
	cAcquireNS
	cAcquires
	cFailedAcquires
	cUnobservedBatch
	cConnectionClosed
	cDuplicateEnds
	cAttributionFailures
	cOverflow
	cOutcomeDetailLoss
	cEmptyBatches
	cCopySetupFailures
	cOriginUntracked
	cMaxArgs
	cMaxArray
	cMaxList
	cMaxBatch
	cUnobservedEnvelopes
)

func (c *counters) snapshot() Counters {
	var v [25]uint64
	for i := range v {
		v[i] = c.values[i].Load()
	}
	r := Counters{UnobservedEnvelopes: v[24], Started: v[0], Completed: v[1], Observed: v[2], Unobserved: v[3], QueryNS: v[4], Rows: v[5], Batches: v[6], Envelopes: v[7], AcquireNS: v[8], Acquires: v[9], FailedAcquires: v[10], UnobservedBatch: v[11], ConnectionClosed: v[12], DuplicateEnds: v[13], AttributionFailures: v[14], Overflow: v[15], OutcomeDetailLoss: v[16], EmptyBatches: v[17], CopySetupFailures: v[18], OriginUntracked: v[19], MaxArgs: v[20], MaxArray: v[21], MaxList: v[22], MaxBatch: v[23]}
	for i := range r.Outcomes {
		r.Outcomes[i] = c.outcomes[i].Load()
		r.EnvelopeOutcomes[i] = c.envelopeOutcomes[i].Load()
	}
	return r
}
func (s *Store) Totals() Counters {
	r := s.reserved[0].counts.snapshot()
	for i := range r.AcquireOutcomes {
		r.AcquireOutcomes[i] = s.acquireOutcomes[i].Load()
	}
	return r
}
func completeCounts(c *counters, k Kind, n, ns, rows uint64, o int, unobserved bool) {
	if k == Acquire {
		c.values[cAcquires].Add(1)
		c.values[cAcquireNS].Add(ns)
		return
	}
	if k == Batch {
		c.values[cEnvelopes].Add(1)
		if !unobserved {
			c.envelopeOutcomes[o].Add(1)
		} else {
			c.values[cUnobservedEnvelopes].Add(1)
		}
		c.values[cQueryNS].Add(ns)
		return
	}
	c.values[cCompleted].Add(n)
	c.values[cQueryNS].Add(ns)
	c.values[cRows].Add(rows)
	if unobserved {
		c.values[cUnobserved].Add(n)
	} else {
		c.values[cObserved].Add(n)
		c.outcomes[o].Add(n)
	}
}
func (s *Store) completed(r *row, k Kind, n, ns, rows uint64, o int, unobserved bool) {
	completeCounts(&s.reserved[0].counts, k, n, ns, rows, o, unobserved)
	completeCounts(&r.counts, k, n, ns, rows, o, unobserved)
}
func (s *Store) gapN(index int, n uint64) {
	s.reserved[0].counts.values[index].Add(n)
	reserved := 0
	switch index {
	case cFailedAcquires:
		reserved = 7
	case cEmptyBatches:
		reserved = 8
	case cCopySetupFailures:
		reserved = 9
	case cUnobservedBatch:
		reserved = 10
	case cConnectionClosed:
		reserved = 11
	case cOriginUntracked:
		reserved = 12
	case cAttributionFailures:
		reserved = 13
	case cDuplicateEnds:
		reserved = 14
	}
	if reserved != 0 {
		s.reserved[reserved].counts.values[index].Add(n)
	}
}
func updateSizes(c *counters, s Sizes) {
	for i, v := range [4]int32{s.Arguments, s.Array, s.List, s.Batch} {
		if v > 0 {
			a := &c.values[cMaxArgs+i]
			for old := a.Load(); uint64(v) > old; old = a.Load() {
				if a.CompareAndSwap(old, uint64(v)) {
					break
				}
			}
		}
	}
}
func (s *Store) gap(index int) { s.gapN(index, 1) }
func statementSQL(sql string) bool {
	l := lexer{sql, 0}
	t := l.next()
	if t.kind != 'w' {
		return false
	}
	// A prepared name is a single identifier. A SQL command has another token;
	// literals, options and identifiers stay private to the streaming lexer.
	if l.next().kind == 0 {
		return control(sql) != 0
	}
	for _, keyword := range [...]string{"select", "insert", "update", "delete", "with", "begin", "start", "commit", "end", "rollback", "abort", "savepoint", "release", "set", "show", "create", "alter", "drop", "truncate", "explain", "analyze", "vacuum", "grant", "revoke", "copy", "do", "call", "values", "refresh", "listen", "unlisten", "notify", "discard", "reset", "deallocate", "prepare", "execute", "comment", "reindex", "lock", "merge", "fetch", "move", "declare", "close", "checkpoint", "cluster", "security", "import", "load", "reassign"} {
		if strings.EqualFold(t.text, keyword) {
			return true
		}
	}
	return false
}

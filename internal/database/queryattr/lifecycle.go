package queryattr

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/envutil"
)

// Kind identifies the callback that owns an operation.
type Kind uint8

const (
	Query Kind = iota
	CopyFrom
	Batch
	Acquire
)

// Observation contains only code-derived identity, fixed categories and numbers.
type Observation struct {
	Identity             Identity
	Kind                 Kind
	Sizes                Sizes
	Statements, NS, Rows uint64
	Outcome              int
	Unobserved           bool
	Child                bool
	StartedNS            int64
}

// Scope receives one Start per operation. A batch starts its queued count once,
// then Finish reports each observed child (Child=true) and its unobserved tail
// with the envelope duration. Acquires carry StartedNS for sealing live waits.
// Callbacks receive no SQL, values, errors or caller contexts.
type Scope interface {
	Start(Identity, Kind, uint64, Sizes, int64)
	Finish(Observation)
}

var epoch = time.Now()

// NowNS is the process-monotonic clock used by acquires and request seals.
func NowNS() int64 { return time.Since(epoch).Nanoseconds() }

type recordKey struct{}
type record struct {
	context.Context
	row         *row
	scope       Scope
	batch       *pgx.Batch
	start       int64
	sizes       Sizes
	next        *record
	children    atomic.Uint32
	done        atomic.Uint32
	kind        Kind
	origin      Origin
	ctl         uint8
	callerKnown bool
}

func (r *record) Value(key any) any {
	if _, ok := key.(recordKey); ok {
		return r
	}
	return r.Context.Value(key)
}

// Tracer implements attribution independently of the coarse dependency tracer.
type Tracer struct{ Store *Store }

func (t Tracer) store() *Store {
	if t.Store != nil {
		return t.Store
	}
	return Default
}
func (t Tracer) recoverStart() {
	if recover() != nil {
		t.store().gap(cAttributionFailures)
	}
}
func (t Tracer) begin(ctx context.Context, conn *pgx.Conn, k Kind, n uint64) *record {
	start := NowNS()
	r := &record{Context: ctx, row: &t.store().reserved[4], start: start, kind: k}
	s := t.store()
	if k != Acquire {
		s.gapN(cStarted, n)
		if k != Batch {
			r.row.counts.values[cStarted].Add(n)
		}
	}
	if k == Batch {
		s.gap(cBatches)
	}
	if k != Acquire {
		st := stateFor(conn)
		if st != nil {
			st.mu.Lock()
			r.next = st.active
			st.active = r
			if r.kind == CopyFrom {
				st.copyTicket = r
			}
			st.mu.Unlock()
		}
	}
	return r
}
func (t Tracer) enrich(r *record, conn *pgx.Conn, sql string, args []any, form Form, copyHash uint64) {
	defer t.recoverStart()
	s := t.store()
	defer func() {
		n := uint64(1)
		if r.kind == Batch || r.kind == Acquire {
			n = 0
		}
		s.reserved[1+int(r.origin)].counts.values[cStarted].Add(n)
		if r.kind == Batch {
			r.row.counts.values[cBatches].Add(1)
			s.reserved[1+int(r.origin)].counts.values[cBatches].Add(1)
		}
		// The request start must survive a caller or size-enrichment panic.
		if r.kind != Batch {
			t.scopeStart(r, n)
		}
	}()
	st := stateFor(conn)
	own := ownershipFrom(r.Context)
	r.origin = own.origin
	r.scope = own.scope
	c := Caller{}
	fp := copyHash
	var target *row
	if own.origin == InternalOrigin {
		r.scope = nil
		index := 5 + int(own.component)
		if index > 6 {
			index = 4
			s.gap(cAttributionFailures)
		}
		target = &s.reserved[index]
	} else {
		r.ctl = control(sql)
		if r.ctl != 0 && st != nil {
			st.mu.Lock()
			c = st.owner
			st.mu.Unlock()
		}
		if c.Function == "" {
			c = s.caller(r.Context, st)
		}
		r.callerKnown = c.Function != ""
		if r.ctl != 0 {
			form = Transaction
		} else if form == Statement && statementSQL(sql) {
			fp, r.sizes.List = fingerprint(sql)
		} else if form == Statement {
			form = Unavailable
		}
		target = s.lookup(c, fp, form)
		if form == Statement || form == Unavailable {
			sizes := argumentSizes(args)
			if sizes.Arguments < 0 {
				fp = 0
				target = s.lookup(c, fp, Unavailable)
			}
			sizes.List = r.sizes.List
			r.sizes = sizes
		}
		if own.origin == BackgroundOrigin && own.category == "" {
			s.gap(cOriginUntracked)
		}
	}
	n := uint64(1)
	if r.kind == Batch || r.kind == Acquire {
		n = 0
	}
	if target != r.row && n > 0 {
		r.row.counts.values[cStarted].Add(^(n - 1))
		target.counts.values[cStarted].Add(n)
	}
	r.row = target
	if c.Function != "" && (target.identity.Form == FunctionOnly || target == &s.reserved[4]) && r.kind != Acquire {
		s.gapN(cOverflow, n)
	}
}
func (t Tracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r := t.begin(ctx, c, Query, 1)
	t.enrich(r, c, d.SQL, d.Args, Statement, 0)
	return r
}
func (t Tracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	t.finishContext(ctx, c, Query, d.CommandTag, d.Err)
}
func (t Tracer) TraceBatchStart(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	if d.Batch == nil || d.Batch.Len() == 0 {
		return ctx
	}
	r := t.begin(ctx, c, Batch, uint64(d.Batch.Len()))
	r.batch = d.Batch
	t.enrich(r, c, "", nil, BatchEnvelope, 0)
	r.sizes.Batch = boundedSize(d.Batch.Len())
	t.scopeStart(r, uint64(d.Batch.Len()))
	t.store().reserved[1+int(r.origin)].counts.values[cStarted].Add(uint64(d.Batch.Len()))
	return r
}
func (t Tracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchQueryData) {
	defer t.recoverStart()
	r := t.find(ctx, Batch)
	if r == nil {
		return
	}
	t.store().batchMu.Lock()
	defer t.store().batchMu.Unlock()
	if r.done.Load() != 0 {
		return
	}
	if r.batch == nil || int(r.children.Load()) >= r.batch.Len() {
		t.store().gap(cAttributionFailures)
		return
	}
	r.children.Add(1)
	s := t.store()
	target := r.row
	sizes := argumentSizes(d.Args)
	if r.origin != InternalOrigin && r.callerKnown && r.row != &s.reserved[4] {
		form := Statement
		fp := uint64(0)
		if sizes.Arguments >= 0 && statementSQL(d.SQL) {
			fp, sizes.List = fingerprint(d.SQL)
		} else {
			form = Unavailable
		}
		target = s.lookup(r.row.identity.Caller, fp, form)
	}
	if r.callerKnown && (target.identity.Form == FunctionOnly || target == &s.reserved[4]) {
		s.gap(cOverflow)
	}
	target.counts.values[cStarted].Add(1)
	s.completed(target, Query, 1, 0, uint64(max(d.CommandTag.RowsAffected(), 0)), outcome(d.Err), false)
	completeCounts(&s.reserved[1+int(r.origin)].counts, Query, 1, 0, uint64(max(d.CommandTag.RowsAffected(), 0)), outcome(d.Err), false)
	updateSizes(&target.counts, sizes)
	updateSizes(&s.reserved[0].counts, sizes)
	updateSizes(&s.reserved[1+int(r.origin)].counts, sizes)
	if r.scope != nil {
		t.scopeFinish(r, Observation{Identity: target.identity, Kind: Batch, Sizes: sizes, Statements: 1, Rows: uint64(max(d.CommandTag.RowsAffected(), 0)), Outcome: outcome(d.Err), Child: true, StartedNS: r.start})
	}
}
func (t Tracer) TraceBatchEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchEndData) {
	t.finishContext(ctx, c, Batch, pgconn.CommandTag{}, d.Err)
}
func (t Tracer) TraceCopyFromStart(ctx context.Context, c *pgx.Conn, d pgx.TraceCopyFromStartData) context.Context {
	r := t.begin(ctx, c, CopyFrom, 1)
	h := addToken(hashBasis, token{'w', "copy"})
	for _, name := range d.TableName {
		h = addToken(h, token{'q', name})
	}
	h = addToken(h, token{'p', "("})
	for _, name := range d.ColumnNames {
		h = addToken(h, token{'q', name})
	}
	t.enrich(r, c, "", nil, Copy, h)
	return r
}
func (t Tracer) TraceCopyFromEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceCopyFromEndData) {
	t.finishContext(ctx, c, CopyFrom, d.CommandTag, d.Err)
}
func (t Tracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	r := t.begin(ctx, nil, Acquire, 0)
	t.enrichAcquire(r)
	return r
}
func (t Tracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, d pgxpool.TraceAcquireEndData) {
	t.finishContext(ctx, d.Conn, Acquire, pgconn.CommandTag{}, d.Err)
}
func (t Tracer) find(ctx context.Context, k Kind) *record {
	r, ok := ctx.Value(recordKey{}).(*record)
	if !ok || r.kind != k {
		t.store().gap(cAttributionFailures)
		return nil
	}
	return r
}
func (t Tracer) finishContext(ctx context.Context, c *pgx.Conn, k Kind, tag pgconn.CommandTag, err error) {
	defer t.recoverStart()
	if r := t.find(ctx, k); r != nil {
		t.finish(r, c, k, tag, err, false)
	}
}
func (t Tracer) finish(r *record, c *pgx.Conn, k Kind, tag pgconn.CommandTag, err error, closed bool) bool {
	if k == Batch {
		t.store().batchMu.Lock()
		defer t.store().batchMu.Unlock()
	}
	if r.kind != k {
		t.store().gap(cAttributionFailures)
		return false
	}
	if !r.done.CompareAndSwap(0, 1) {
		if !closed {
			t.store().gap(cDuplicateEnds)
		}
		return false
	}
	st := stateFor(c)
	defer func() {
		r.scope = nil
		r.batch = nil
		if st != nil {
			removeRecord(st, r)
		}
	}()
	s := t.store()
	ns := uint64(max(NowNS()-r.start, 0))
	n := uint64(1)
	resolved := uint64(1)
	if k == Acquire {
		resolved = 0
	}
	o := outcome(err)
	rows := uint64(max(tag.RowsAffected(), 0))
	if k == Batch {
		n = 0
		if r.batch != nil {
			n = uint64(r.batch.Len()) - uint64(r.children.Load())
		}
		if n > 0 {
			r.row.counts.values[cStarted].Add(n)
			s.completed(r.row, Query, n, 0, 0, 0, true)
			completeCounts(&s.reserved[1+int(r.origin)].counts, Query, n, 0, 0, 0, true)
			s.gapN(cUnobservedBatch, n)
		}
		resolved = n
		n = 0
	}
	if closed {
		s.gap(cConnectionClosed)
	}
	s.completed(r.row, k, n, ns, rows, o, closed)
	completeCounts(&s.reserved[1+int(r.origin)].counts, k, n, ns, rows, o, closed)
	if k == Acquire {
		s.acquireOutcomes[o].Add(1)
		if err != nil {
			s.gap(cFailedAcquires)
		}
	}
	updateSizes(&r.row.counts, r.sizes)
	updateSizes(&s.reserved[0].counts, r.sizes)
	updateSizes(&s.reserved[1+int(r.origin)].counts, r.sizes)
	if r.ctl != 0 && st != nil && c != nil {
		st.mu.Lock()
		if r.ctl == 1 && err == nil && st.owner.Function == "" {
			st.owner = r.row.identity.Caller
		}
		if c.PgConn().TxStatus() == 'I' {
			st.owner = Caller{}
		}
		st.mu.Unlock()
	}
	if r.scope != nil {
		t.scopeFinish(r, Observation{Identity: r.row.identity, Kind: k, Sizes: r.sizes, Statements: resolved, NS: ns, Rows: rows, Outcome: o, Unobserved: closed || k == Batch && resolved > 0, StartedNS: r.start})
	}
	return true
}

// CountEmptyBatch records a wrapper-only gap without starting a statement.
func (t Tracer) CountEmptyBatch() { t.store().gap(cEmptyBatches) }

var enabled atomic.Bool
var initialize = sync.OnceValue(func() bool {
	v := envutil.BoolDefault("SILO_QUERY_ATTRIBUTION_ENABLED", true)
	enabled.Store(v)
	return v
})

// Initialize reads the process kill switch once, at the first pool.
func Initialize() bool { return initialize() }

// Enabled reports the initialized process-wide attribution switch.
func Enabled() bool { return enabled.Load() }

func (t Tracer) scopeStart(r *record, n uint64) {
	defer t.recoverStart()
	if r.scope != nil {
		r.scope.Start(r.row.identity, r.kind, n, r.sizes, r.start)
	}
}
func (t Tracer) scopeFinish(r *record, o Observation) { defer t.recoverStart(); r.scope.Finish(o) }
func (t Tracer) enrichAcquire(r *record) {
	defer t.recoverStart()
	own := ownershipFrom(r.Context)
	r.scope = own.scope
	r.origin = own.origin
	t.scopeStart(r, 0)
}

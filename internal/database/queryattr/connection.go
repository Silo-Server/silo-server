package queryattr

import (
	"context"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const stateKey = "silo.queryattr"

type connectionState struct {
	mu                 sync.Mutex
	pcs                [16]uintptr
	owner              Caller
	active, copyTicket *record
}

func stateFor(conn *pgx.Conn) *connectionState {
	if conn == nil || conn.PgConn() == nil {
		return nil
	}
	data := conn.PgConn().CustomData()
	if s, ok := data[stateKey].(*connectionState); ok {
		return s
	}
	s := &connectionState{}
	data[stateKey] = s
	return s
}
func control(sql string) uint8 {
	l := lexer{sql, 0}
	t := l.next()
	if t.kind != 'w' {
		return 0
	}
	switch {
	case equalToken(t, token{'w', "begin"}):
		return 1
	case equalToken(t, token{'w', "start"}):
		if equalToken(l.next(), token{'w', "transaction"}) {
			return 1
		}
	case equalToken(t, token{'w', "commit"}), equalToken(t, token{'w', "end"}), equalToken(t, token{'w', "rollback"}), equalToken(t, token{'w', "abort"}), equalToken(t, token{'w', "savepoint"}), equalToken(t, token{'w', "release"}):
		return 2
	}
	return 0
}

// ClearCopyTicket arms the connection before forwarding a transaction CopyFrom.
func ClearCopyTicket(conn *pgx.Conn) {
	if st := stateFor(conn); st != nil {
		st.mu.Lock()
		st.copyTicket = nil
		st.mu.Unlock()
	}
}

// ResolveCopySetup finishes only a copy whose pgx end callback was omitted.
// The returned context lets the wrapper finish the existing coarse operation.
func (t Tracer) ResolveCopySetup(conn *pgx.Conn, rows int64, err error) context.Context {
	st := stateFor(conn)
	if st == nil {
		return nil
	}
	st.mu.Lock()
	r := st.copyTicket
	st.copyTicket = nil
	st.mu.Unlock()
	if r == nil || r.done.Load() != 0 {
		return nil
	}
	tag := pgconn.NewCommandTag("COPY " + strconv.FormatInt(rows, 10))
	if t.finish(r, conn, CopyFrom, tag, err, false) {
		t.store().gap(cCopySetupFailures)
		return r
	}
	return nil
}

// BeforeClose resolves abandoned rows and pgx's failed-deallocation Exec gap
// without inferring an observed outcome from connection teardown.
func (t Tracer) BeforeClose(conn *pgx.Conn) {
	defer t.recoverStart()
	st := stateFor(conn)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.owner = Caller{}
	st.copyTicket = nil
	st.mu.Unlock()
	for {
		st.mu.Lock()
		r := st.active
		st.mu.Unlock()
		if r == nil {
			return
		}
		if !t.finish(r, conn, r.kind, pgconn.CommandTag{}, nil, true) {
			removeRecord(st, r)
		}
	}
}
func removeRecord(st *connectionState, r *record) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for link := &st.active; *link != nil; link = &(*link).next {
		if *link == r {
			*link = r.next
			r.next = nil
			return
		}
	}
}

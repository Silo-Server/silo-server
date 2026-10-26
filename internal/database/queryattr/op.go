// Package queryattr attributes PostgreSQL operation costs without retaining SQL or values.
package queryattr

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/envutil"
)

// Kind identifies one traced operation; acquire contributes only request wait time.
type Kind uint8

const (
	KindQuery   Kind = iota + 1 // Query, QueryRow, Exec
	KindBatch                   // SendBatch envelope
	KindCopy                    // CopyFrom
	KindAcquire                 // pool acquire: request wait only, no site
)

var enabled atomic.Bool
var initializeOnce sync.Once

// Initialize reads SILO_QUERY_ATTRIBUTION_ENABLED once and returns Enabled.
// Attribution defaults off during the initial soak period.
func Initialize() bool {
	initializeOnce.Do(func() { enabled.Store(envutil.BoolDefault("SILO_QUERY_ATTRIBUTION_ENABLED", false)) })
	return Enabled()
}

// Enabled reports whether initialization enabled attribution. It is false before Initialize.
func Enabled() bool { return enabled.Load() }

// Op retains only counts, identity, context markers, and start time for a traced operation.
// The tracer owns exactly-once completion; an inert Op is safe to end.
type Op struct {
	row                        *row
	request                    *Request
	start                      time.Time
	kind                       Kind
	statements, args, maxArray int
	quiet                      bool
}

// Begin starts attribution for kind. SQL is hashed only for queries, and args are
// inspected only for sizes. Neither input is retained. statements counts queued
// batch statements; queries and copies always report one. Off returns an inert Op.
func Begin(ctx context.Context, kind Kind, sql string, args []any, statements int) Op {
	if !Enabled() {
		return Op{}
	}
	o := Op{request: requestFromContext(ctx), start: time.Now(), kind: kind, statements: statements}
	if kind == KindAcquire {
		return o
	}
	o.quiet, _ = ctx.Value(quietKey{}).(bool)
	s := caller()
	switch kind {
	case KindBatch:
		o.row = s.shapeRow("batch")
		o.args = -1
		o.maxArray = -1
	case KindCopy:
		o.row = s.shapeRow("copy")
		o.statements = 1
		o.args = -1
		o.maxArray = -1
	case KindQuery:
		o.row = queryRow(s, sql)
		o.statements = 1
		o.args, o.maxArray = argumentSizes(args)
	}
	return o
}

// End completes this operation. Errors contribute only an allowlisted outcome and count.
func (o *Op) End(ctx context.Context, err error) {
	now := time.Now()
	o.end(ctx, err, now.Sub(o.start).Nanoseconds(), now)
}

func (o *Op) end(ctx context.Context, err error, ns int64, now time.Time) {
	if ns < 0 {
		ns = 0
	}
	if o.kind == KindAcquire {
		o.request.add(nil, uint64(ns))
		return
	}
	if o.row == nil {
		return
	}
	r := o.row
	r.calls.Add(1)
	r.ns.Add(uint64(ns))
	if err != nil {
		r.errors.Add(1)
	}
	r.site.queries.Inc()
	r.site.seconds.Add(float64(ns) / float64(time.Second))
	inRequest := o.request.add(r, uint64(ns))
	o.event(ctx, err, ns, now, inRequest)
}

// Site returns the calling function and line, or "" for an inert or acquire operation.
func (o *Op) Site() string {
	if o.row == nil {
		return ""
	}
	return o.row.site.name
}

// Shape returns the bounded SQL shape, or "" for an inert or acquire operation.
func (o *Op) Shape() string {
	if o.row == nil {
		return ""
	}
	return o.row.shape
}

func argumentSizes(args []any) (count, largest int) {
	options := true
	for _, a := range args {
		switch a.(type) {
		case pgx.QueryExecMode, pgx.QueryResultFormats, pgx.QueryResultFormatsByOID:
			if options {
				continue
			}
		case pgx.QueryRewriter:
			return -1, -1
		}
		options = false
		count++
		switch a.(type) {
		case []byte, json.RawMessage:
			continue
		}
		v := reflect.ValueOf(a)
		if !v.IsValid() {
			continue
		}
		typ := v.Type()
		if typ.PkgPath() == "github.com/jackc/pgx/v5/pgtype" {
			largest = -1
			continue
		}
		if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
			if typ.Elem().Kind() == reflect.Slice || typ.Elem().Kind() == reflect.Array {
				largest = -1
				continue
			}
			if largest >= 0 {
				largest = max(largest, v.Len())
			}
		}
	}
	return count, largest
}

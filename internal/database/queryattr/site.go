package queryattr

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

const modulePrefix = "github.com/Silo-Server/silo-server/"
const shapeOther = "other"

var sites sync.Map // site name -> *site; also summary membership
var pcs sync.Map   // PC -> *site, including typed nil for skipped PCs

type site struct {
	name             string
	mu               sync.Mutex
	rows             map[string]*row
	queries, seconds prometheus.Counter
}

type totals struct{ calls, ns, errors uint64 }

type row struct {
	site              *site
	shape             string
	hash              uint64
	calls, ns, errors atomic.Uint64
	previous          totals // Only the summary goroutine reads or writes previous.
}

func internSite(name string) *site {
	if cached, ok := sites.Load(name); ok {
		s, _ := cached.(*site)
		return s
	}
	s := &site{name: name, rows: make(map[string]*row),
		queries: queriesTotal.WithLabelValues(name), seconds: querySecondsTotal.WithLabelValues(name)}
	actual, _ := sites.LoadOrStore(name, s)
	interned, _ := actual.(*site)
	return interned
}

func (s *site) shapeRow(shape string) *row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shapeRowLocked(shape)
}

func (s *site) shapeRowLocked(shape string) *row {
	if r := s.rows[shape]; r != nil {
		return r
	}
	count := len(s.rows)
	if s.rows[shapeOther] != nil {
		count--
	}
	if count >= 8 {
		shape = shapeOther
	}
	if r := s.rows[shape]; r != nil {
		return r
	}
	r := &row{site: s, shape: shape}
	s.rows[shape] = r
	return r
}

func displayName(name string) string {
	return strings.TrimPrefix(strings.TrimPrefix(name, modulePrefix), "internal/")
}

func skipFunction(name string) bool {
	return !strings.HasPrefix(name, modulePrefix) && !strings.HasPrefix(name, "main.") ||
		strings.HasPrefix(name, modulePrefix+"internal/database")
}

// shortWalk covers the usual path from the tracer through pgx to the calling
// site. runtime.Callers cost grows with the frames it unwinds; in a production
// build the full walk measured about 10 µs per query, so it runs only when the
// short one finds no site.
const shortWalk = 8

func caller() *site {
	var stack [32]uintptr
	n := runtime.Callers(3, stack[:shortWalk])
	if s := firstSite(stack[:n]); s != nil {
		return s
	}
	if n == shortWalk {
		n = runtime.Callers(3, stack[:])
		if s := firstSite(stack[shortWalk:n]); s != nil {
			return s
		}
	}
	return internSite("unknown")
}

func firstSite(stack []uintptr) *site {
	for _, pc := range stack {
		if cached, ok := pcs.Load(pc); ok {
			if s, _ := cached.(*site); s != nil {
				return s
			}
			continue
		}
		var s *site
		frames := runtime.CallersFrames([]uintptr{pc})
		for {
			frame, more := frames.Next()
			if !skipFunction(frame.Function) {
				s = internSite(displayName(frame.Function) + ":" + strconv.Itoa(frame.Line))
				break
			}
			if !more {
				break
			}
		}
		pcs.Store(pc, s)
		if s != nil {
			return s
		}
	}
	return nil
}

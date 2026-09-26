package debugserver

import (
	"context"
	"fmt"
	"html"
	"math"
	"net/http"
	pprof "net/http/pprof"
	"net/url"
	"runtime"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
)

const (
	profilePrefix    = "/debug/pprof/"
	indexName        = "index"
	profileName      = "profile"
	traceName        = "trace"
	heapName         = "heap"
	allocsName       = "allocs"
	goroutineName    = "goroutine"
	threadcreateName = "threadcreate"
	blockName        = "block"
	mutexName        = "mutex"
)

// Capture ownership is process-wide, including snapshots and forced GC. A
// canceled client retains the slot until the standard handler actually exits.
var captureSlot = make(chan struct{}, 1)

type sealedHandler struct{ handler http.Handler }

func (h sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "profiling requires GET", http.StatusMethodNotAllowed)
		return
	}
	if !loopbackAuthority(r.Host) || !localBrowserRequest(r) {
		http.Error(w, "profiling requires a local request", http.StatusForbidden)
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "profiling requests cannot have a body", http.StatusBadRequest)
		return
	}
	// Reject mux redirects and prefix matches before entering a handler. This
	// also prevents any future runtime profile from appearing automatically.
	switch r.URL.Path {
	case profilePrefix, profilePrefix + profileName, profilePrefix + traceName,
		profilePrefix + heapName, profilePrefix + allocsName, profilePrefix + goroutineName,
		profilePrefix + threadcreateName, profilePrefix + blockName, profilePrefix + mutexName:
	default:
		http.NotFound(w, r)
		return
	}
	h.handler.ServeHTTP(w, r)
}

func localBrowserRequest(r *http.Request) bool {
	if sites := r.Header.Values("Sec-Fetch-Site"); len(sites) > 1 {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) > 1 {
		return false
	}
	if len(origins) == 1 {
		u, err := url.Parse(origins[0])
		return err == nil && u.Scheme == "http" && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
	}
	return true
}

// newHandler seals the only profiling registration surface. The route
// inventory checks both this shape and the one named net/http/pprof import.
func newHandler(c Config, instance string) http.Handler {
	return sealedHandler{handler: newMux(c, instance)}
}

func newMux(c Config, instance string) *http.ServeMux {
	mux := http.NewServeMux()
	g := captureGuard{config: c, instance: instance, revision: buildRevision()}
	mux.Handle("/debug/pprof/", g.wrap(indexName, http.HandlerFunc(g.index)))
	mux.Handle("/debug/pprof/profile", g.wrap(profileName, http.HandlerFunc(pprof.Profile)))
	mux.Handle("/debug/pprof/trace", g.wrap(traceName, http.HandlerFunc(pprof.Trace)))
	mux.Handle("/debug/pprof/heap", g.wrap(heapName, pprof.Handler(heapName)))
	mux.Handle("/debug/pprof/allocs", g.wrap(allocsName, pprof.Handler(allocsName)))
	mux.Handle("/debug/pprof/goroutine", g.wrap(goroutineName, pprof.Handler(goroutineName)))
	mux.Handle("/debug/pprof/threadcreate", g.wrap(threadcreateName, pprof.Handler(threadcreateName)))
	mux.Handle("/debug/pprof/block", g.wrap(blockName, pprof.Handler(blockName)))
	mux.Handle("/debug/pprof/mutex", g.wrap(mutexName, pprof.Handler(mutexName)))
	return mux
}

type captureGuard struct {
	config   Config
	instance string
	revision string
}

func buildRevision() string {
	if revision := buildinfo.Current().Revision; revision != "" {
		return revision
	}
	return "unknown"
}

func (g captureGuard) wrap(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		duration, err := validateQuery(name, r.URL.RawQuery)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Silo-Go-Version", runtime.Version())
		w.Header().Set("X-Silo-Revision", g.revision)
		w.Header().Set("X-Silo-Instance", g.instance)
		w.Header().Set("X-Silo-Block-Rate", strconv.Itoa(g.config.BlockRate))
		w.Header().Set("X-Silo-Mutex-Fraction", strconv.Itoa(g.config.MutexFraction))
		w.Header().Set("X-Silo-Memory-Profile-Rate", strconv.Itoa(runtime.MemProfileRate))
		if name == indexName {
			next.ServeHTTP(w, r)
			return
		}
		if (name == blockName && g.config.BlockRate == 0) || (name == mutexName && g.config.MutexFraction == 0) {
			http.Error(w, "contention sampling is disabled; configure it at startup", http.StatusConflict)
			return
		}
		if r.Context().Err() != nil {
			return
		}
		select {
		case captureSlot <- struct{}{}:
			defer func() { <-captureSlot }()
		default:
			busyCaptures.Inc()
			w.Header().Set("Retry-After", "1")
			http.Error(w, "another profiling capture is running", http.StatusTooManyRequests)
			return
		}
		started := time.Now()
		w.Header().Set("X-Silo-Capture-Started", started.UTC().Format(time.RFC3339Nano))
		w.Header().Set("X-Silo-Capture-Requested-Seconds", strconv.FormatFloat(duration.Seconds(), 'f', -1, 64))
		w.Header().Add("Trailer", "X-Silo-Capture-Interrupted")
		w.Header().Add("Trailer", "X-Silo-Capture-Duration-Seconds")
		ctx, cancel := context.WithTimeout(r.Context(), duration+10*time.Second)
		defer cancel()
		// Pass the original writer through. Standard timed handlers use its
		// ResponseController to extend the write deadline by the capture time.
		next.ServeHTTP(w, r.WithContext(ctx))
		w.Header().Set("X-Silo-Capture-Interrupted", strconv.FormatBool(ctx.Err() != nil))
		w.Header().Set("X-Silo-Capture-Duration-Seconds", strconv.FormatFloat(time.Since(started).Seconds(), 'f', 6, 64))
	})
}

func (g captureGuard) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><title>Silo process profiles</title></head><body><h1>Silo process profiles</h1><p>Instance: %s. Go: %s. Revision: %s.</p><ul>", html.EscapeString(g.instance), html.EscapeString(runtime.Version()), html.EscapeString(g.revision))
	for _, name := range []string{profileName, traceName, heapName, allocsName, goroutineName, threadcreateName, blockName, mutexName} {
		if (name == blockName && g.config.BlockRate == 0) || (name == mutexName && g.config.MutexFraction == 0) {
			_, _ = fmt.Fprintf(w, "<li>%s: sampling disabled</li>", name)
		} else {
			_, _ = fmt.Fprintf(w, "<li><a href=\"/debug/pprof/%s\">%s</a></li>", name, name)
		}
	}
	_, _ = fmt.Fprintf(w, "</ul><p>Block sample period: %d ns. Mutex sampling denominator: %d (0 means disabled). Memory sample period: %d bytes. One capture at a time; CPU and delta captures at most 60 seconds, execution traces at most 5 seconds.</p></body></html>", g.config.BlockRate, g.config.MutexFraction, runtime.MemProfileRate)
}

func validateQuery(name, raw string) (time.Duration, error) {
	if len(raw) > 2048 {
		return 0, fmt.Errorf("profiling query is too long")
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid profiling query")
	}
	duration := time.Duration(0)
	if name == profileName {
		duration = 30 * time.Second
	}
	if name == traceName {
		duration = time.Second
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return 0, fmt.Errorf("%s requires one nonempty value", key)
		}
		value := values[0]
		switch key {
		case "seconds":
			if name == indexName {
				return 0, fmt.Errorf("seconds is unsupported on the index")
			}
			if name == traceName {
				seconds, parseErr := strconv.ParseFloat(value, 64)
				if parseErr != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 5 || seconds < 1e-9 {
					return 0, fmt.Errorf("trace seconds must be positive and at most 5")
				}
				duration = time.Duration(seconds * float64(time.Second))
			} else {
				seconds, parseErr := strconv.ParseUint(value, 10, 8)
				if parseErr != nil || seconds == 0 || seconds > 60 {
					return 0, fmt.Errorf("seconds must be an integer from 1 to 60")
				}
				duration = time.Duration(seconds) * time.Second
			}
		case "debug":
			if name == indexName || name == profileName || name == traceName || (value != "0" && value != "1" && (name != goroutineName || value != "2")) {
				return 0, fmt.Errorf("invalid debug value for this profile")
			}
		case "gc":
			if name != heapName || (value != "0" && value != "1") {
				return 0, fmt.Errorf("gc must be 0 or 1 on the heap profile")
			}
		default:
			return 0, fmt.Errorf("unsupported profiling parameter %s", key)
		}
	}
	if q.Has("seconds") && ((q.Has("debug") && q.Get("debug") != "0") || q.Has("gc")) {
		return 0, fmt.Errorf("seconds is incompatible with debug output or gc")
	}
	return duration, nil
}

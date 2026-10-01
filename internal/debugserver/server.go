package debugserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	available    = promauto.NewGauge(prometheus.GaugeOpts{Name: "silo_debug_listener_available", Help: "Whether this process currently serves the local profiling listener."})
	failures     = promauto.NewCounter(prometheus.CounterOpts{Name: "silo_debug_listener_failures_total", Help: "Failures binding or serving the local profiling listener."})
	busyCaptures = promauto.NewCounter(prometheus.CounterOpts{Name: "silo_debug_captures_busy_total", Help: "Profiling requests refused because a capture was already running."})
)

// Server has one lifecycle owner in bootstrap, across all serving roles.
type Server struct {
	http   *http.Server
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// Start leaves bind failure handling to bootstrap so an optional listener never
// takes application readiness down. Disabled configuration returns nil, nil.
func Start(c Config, instance string) (*Server, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.Listen == "" {
		return nil, nil
	}
	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		failures.Inc()
		return nil, err
	}
	runtime.SetBlockProfileRate(c.BlockRate)
	runtime.SetMutexProfileFraction(c.MutexFraction)
	return startListener(ln, c, instance), nil
}

func startListener(ln net.Listener, c Config, instance string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{cancel: cancel, done: make(chan struct{})}
	s.http = &http.Server{
		Handler:           newHandler(c, instance),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	available.Set(1)
	go func() {
		defer close(s.done)
		defer available.Set(0)
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures.Inc()
			slog.Error("local profiling listener failed", "error", err)
			cancel()
		}
	}()
	return s
}

// Shutdown cancels active captures before draining. If the budget expires it
// closes connections; CPU/GC/runtime work may finish after its client is gone.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.once.Do(s.cancel)
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
	}
	<-s.done
	return err
}

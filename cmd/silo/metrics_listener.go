package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const metricsListenEnv = "SILO_METRICS_LISTEN"

func newMetricsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

type metricsHandler struct{ mux http.Handler }

func (h metricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func newMetricsHandler() http.Handler { return metricsHandler{mux: newMetricsMux()} }

// startMetricsListener starts the opt-in operator listener. The public
// application listener deliberately has no metrics route; an operator must
// choose a separate address and expose it through an internal monitoring
// network.
func startMetricsListener(serving bool) (func(), error) {
	if !serving {
		return func() {}, nil
	}
	addr := strings.TrimSpace(os.Getenv(metricsListenEnv))
	if addr == "" {
		return func() {}, nil
	}
	srv := &http.Server{
		Addr:         addr,
		Handler:      newMetricsHandler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	slog.Info("metrics listener enabled", "address", addr)
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("metrics listener stopped", "error", serveErr)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if shutdownErr := srv.Shutdown(ctx); shutdownErr != nil {
				slog.Warn("metrics listener shutdown failed", "error", shutdownErr)
			}
		})
	}, nil
}

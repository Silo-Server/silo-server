package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/debugserver"
)

// startBootstrapDebugListener runs after dotenv loading, before database work.
// Utility invocations neither bind the port nor enable process-wide sampling.
func startBootstrapDebugListener(serving bool) (func(), error) {
	if !serving {
		return func() {}, nil
	}
	cfg, err := debugserver.LoadConfig(os.Getenv)
	if err != nil {
		return nil, err
	}
	srv, err := debugserver.Start(cfg, resolveNodeIdentity())
	if err != nil {
		slog.Error("local profiling listener unavailable; continuing server startup", "error", err)
		return func() {}, nil
	}
	if srv == nil {
		return func() {}, nil
	}
	slog.Info("local profiling listener enabled", "address", cfg.Listen, "block_rate_ns", cfg.BlockRate, "mutex_fraction", cfg.MutexFraction)
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				slog.Warn("local profiling shutdown", "error", err)
			}
		})
	}, nil
}

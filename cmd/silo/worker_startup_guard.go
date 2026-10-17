package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

// A core without a usable worker control plane must still classify every node
// request. Another replica can enroll a worker after this process starts.
type legacyWorkerGuard struct {
	registry *coreWorkerRegistry
	base     http.RoundTripper
}

func (g legacyWorkerGuard) RoundTrip(request *http.Request) (*http.Response, error) {
	_, enrolled, err := g.registry.ByURL(request.Context(), request.URL.Scheme+"://"+request.URL.Host)
	if err != nil {
		return nil, err
	}
	if enrolled {
		return nil, workercontrolcore.ErrUnavailable
	}
	return g.base.RoundTrip(request)
}

func installLegacyWorkerGuard(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher) error {
	registry := &coreWorkerRegistry{repo: workerregistration.New(pool, cipher)}
	if err := registry.refresh(ctx); err != nil && !workerRegistrySchemaPending(err) {
		return err
	}
	nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
		return legacyWorkerGuard{registry: registry, base: base}
	}, func(ctx context.Context, raw string) (bool, error) {
		_, enrolled, err := registry.ByURL(ctx, workerRegistryOrigin(raw))
		return enrolled, err
	})
	refreshWorkerRegistry(ctx, registry)
	return nil
}

func refreshWorkerRegistry(ctx context.Context, registry *coreWorkerRegistry) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = registry.refresh(ctx)
			}
		}
	}()
}

// Proxies can start before an API replica installs the worker schema. This
// exception is only for startup: an absent snapshot still denies all dispatch.
func workerRegistrySchemaPending(err error) bool {
	databaseError, ok := errors.AsType[*pgconn.PgError](err)
	return ok && databaseError.Code == "42P01"
}

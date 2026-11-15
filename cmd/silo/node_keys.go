package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// startProxyNodeKeyRegistry loads a proxy's copy of the stream node credential
// registry and keeps it current: every minute, and at once on
// node_pool_changed, which an admin key issue publishes. Nothing on the proxy
// reads it yet.
//
// A failed first load is not fatal. A proxy may start before the main server
// has migrated the key column, and the registry reloads every minute and on a
// lookup miss, so it fills in once the schema is there.
func startProxyNodeKeyRegistry(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher, bus cache.EventBus) {
	registry := nodepool.NewKeyRegistry(nodepool.NewRepository(pool), cipher)
	if err := registry.Refresh(ctx); err != nil {
		slog.WarnContext(ctx, "stream node credentials not loaded yet; retrying every minute", "error", err)
	}
	registry.Start(ctx)
	if err := bus.Subscribe(ctx, cache.ChannelAdmin, func(event cache.Event) {
		if event.Type == cache.EventNodePoolChanged {
			registry.Invalidate()
		}
	}); err != nil {
		slog.WarnContext(ctx, "node credential reload events unavailable; reloading every minute instead", "error", err)
	}
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/envutil"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// startProxyNodeKeyRegistry loads a proxy's copy of the stream node credential
// registry and keeps it current: every minute, and at once on
// node_pool_changed, which an admin key issue publishes. The proxy signs its
// relays to transcode nodes with the keys it holds.
//
// A failed first load is not fatal. A proxy may start before the main server
// has migrated the key column, and the registry reloads every minute and on a
// lookup miss, so it fills in once the schema is there.
func startProxyNodeKeyRegistry(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher, bus cache.EventBus) *nodepool.KeyRegistry {
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
	return registry
}

// transcodeNodeKey reads and validates SILO_NODE_KEY, or the file
// SILO_NODE_KEY_FILE names. A transcode node refuses to start without one, or
// with one that is not a well-formed node key: it could verify nothing, so
// every request to it would fail.
func transcodeNodeKey(getenv func(string) string) (string, error) {
	key, usedVar, err := envutil.LookupSecret(getenv, transcodenode.EnvNodeKey)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("%s is required: issue this node a key in the admin node settings", usedVar)
	}
	if _, err := nodepool.ParseNodeKey(key); err != nil {
		return "", fmt.Errorf("%s is malformed (%w); copy the key the admin node settings issued", usedVar, err)
	}
	return key, nil
}

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/config"
)

// redisFlushTimeout bounds clearing Redis after a restore. A Redis that does
// not answer in time is reported, not waited on.
const redisFlushTimeout = 15 * time.Second

// redisFlusher clears the one Redis database its client is on.
type redisFlusher interface {
	FlushDB(ctx context.Context) error
	Close() error
}

// dialRedis opens a client on the database rc names; tests replace it.
var dialRedis = func(rc config.RedisConfig) (redisFlusher, error) {
	options, failover, err := rc.Options()
	if err != nil {
		return nil, err
	}
	if failover != nil {
		return goRedisFlusher{redis.NewFailoverClient(failover)}, nil
	}
	return goRedisFlusher{redis.NewClient(options)}, nil
}

type goRedisFlusher struct{ client *redis.Client }

func (f goRedisFlusher) FlushDB(ctx context.Context) error { return f.client.FlushDB(ctx).Err() }
func (f goRedisFlusher) Close() error                      { return f.client.Close() }

// liveRedisConfig is the Redis the live deployment uses: REDIS_URL when the
// environment sets it, otherwise the redis.url and redis.db settings.
func liveRedisConfig(cfg *config.Config) config.RedisConfig {
	return cfg.Redis.WithBootstrapURL(os.Getenv("REDIS_URL"))
}

// redisPlan is what a replace does with Redis once the restored database is
// live: clear the deployment's Redis database, so no server serves state
// cached from before the restore, unless keep. readErr is a failure to read
// the Redis settings.
type redisPlan struct {
	keep    bool
	config  config.RedisConfig
	readErr error
}

// redisFlushCommand is the command an operator runs when the restore could
// not clear Redis itself.
func redisFlushCommand(rc config.RedisConfig) string {
	if db, ok := rc.Database(); ok {
		return fmt.Sprintf("redis-cli -n %d FLUSHDB", db)
	}
	return "FLUSHDB on the Redis database Silo uses"
}

// clearRedisAfterRestore runs plan after a replace finished. It runs FLUSHDB
// on the configured database only, never FLUSHALL, and skips a deployment
// without Redis. Nothing here fails the restore, which already finished: an
// unreachable Redis prints what the operator must do before starting Silo.
func clearRedisAfterRestore(ctx context.Context, plan redisPlan, dial func(config.RedisConfig) (redisFlusher, error), out io.Writer) {
	switch {
	case plan.keep:
		_, _ = fmt.Fprintln(out, "Left Redis as it is (--keep-redis); it can still hold state cached from before the restore.")
		return
	case plan.readErr != nil:
		_, _ = fmt.Fprintf(out, "Warning: could not read the Redis settings (%v). If Silo uses Redis, run FLUSHDB on its database before starting Silo.\n", plan.readErr)
		return
	case strings.TrimSpace(plan.config.URL) == "":
		return
	}
	db, _ := plan.config.Database()
	err := flushRedis(ctx, plan.config, dial)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Warning: could not clear Redis database %d: %v. Clear it before starting Silo: %s\n", db, err, redisFlushCommand(plan.config))
		return
	}
	_, _ = fmt.Fprintf(out, "Cleared Redis database %d, so Silo serves nothing cached from before the restore.\n", db)
}

func flushRedis(ctx context.Context, rc config.RedisConfig, dial func(config.RedisConfig) (redisFlusher, error)) error {
	client, err := dial(rc)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisFlushTimeout)
	defer cancel()
	return client.FlushDB(ctx)
}

// separateRestoreRedisNote is what a separate-mode restore prints: it
// touches nothing live, and the Redis a server on the restored database will
// use is not known here, so it clears none.
const separateRestoreRedisNote = "Redis was not cleared: before starting Silo on the restored database, run FLUSHDB on the Redis database it will use, or give it an empty one."

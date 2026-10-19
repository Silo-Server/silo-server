package main

import (
	"errors"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

const realtimeBackendMemory = "memory"
const realtimeBackendRedis = "redis"

// Work admission has a process-lifetime backend and counters of its own. HTTP
// limiting may be disabled or reloaded without creating fresh realtime credit.
func newRealtimeWorkAdmission(limits config.RateLimitConfig, redisConfig config.RedisConfig) (*realtimeadmission.Controller, func(), error) {
	if limits.Backend == "" || limits.Backend == realtimeBackendMemory {
		return realtimeadmission.New(ratelimit.NewMemoryWorkStore(), realtimeBackendMemory), func() {}, nil
	}
	if limits.Backend != realtimeBackendRedis {
		return nil, nil, errors.New("unknown realtime work backend")
	}
	client, err := cache.NewDeadlineRedisClientForRole(redisConfig, "realtime-work")
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		return nil, nil, errors.New("redis rate limiting requires a configured Redis connection")
	}
	return realtimeadmission.New(ratelimit.NewRedisWorkStore(client), realtimeBackendRedis), func() { _ = cache.CloseRedisClient(client) }, nil
}

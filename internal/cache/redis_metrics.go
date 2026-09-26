package cache

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"weak"

	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

const redisRoleLabel = "role"

var cacheLookups = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_redis_cache_lookups_total", Help: "Redis GET/HGET lookups by result; multi-key operations are excluded.",
}, []string{redisRoleLabel, "result"})

// Command names supplied through Do are arbitrary input too. Never use the
// command's String/Args (which contain keys, scripts and values).
func redisOperation(name string) string {
	switch name = strings.ToLower(name); name {
	case "get", "mget", "set", "del", "exists", "expire", "ttl", "hget", "hgetall", "hset", "hdel", "incr", "incrby", "decr", "publish", "eval", "evalsha", "ping", "scan", "zadd", "zrem", "zrange", "zrangebyscore", "zcard", "sadd", "srem", "smembers", "lpush", "rpush", "lpop", "rpop", "llen", "lrange", "xadd", "xread", "xack":
		return name
	default:
		return "other"
	}
}

func redisObservationError(err error) error {
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if errors.Is(err, redis.ErrPoolTimeout) {
		return context.DeadlineExceeded
	}
	return err
}

type redisHook struct{ role string }

func (h redisHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		ctx, finish := telemetry.StartDependency(ctx, "redis", h.role, "connect")
		conn, err := next(ctx, network, addr)
		finish(err)
		return conn, err
	}
}
func (h redisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		op := redisOperation(cmd.Name())
		ctx, finish := telemetry.StartDependency(ctx, "redis", h.role, op)
		err := next(ctx, cmd)
		observedErr := redisObservationError(err)
		finish(observedErr)
		if op == "get" || op == "hget" {
			result := "hit"
			if errors.Is(err, redis.Nil) {
				result = "miss"
			} else if err != nil {
				result = "error"
			}
			cacheLookups.WithLabelValues(h.role, result).Inc()
		}
		return err
	}
}
func (h redisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx, finish := telemetry.StartDependency(ctx, "redis", h.role, "pipeline")
		err := next(ctx, cmds)
		observedErr := redisObservationError(err)
		// Exec returns the first command error. Count an actual failure even if
		// an earlier cache miss hid it, without exporting any command values.
		for _, cmd := range cmds {
			if e := cmd.Err(); e != nil && !errors.Is(e, redis.Nil) {
				observedErr = redisObservationError(e)
				break
			}
		}
		finish(observedErr)
		return err
	}
}

type redisPoolCollector struct {
	mu          sync.Mutex
	clients     map[weak.Pointer[redis.Client]]string
	connections *prometheus.Desc
	waits       *prometheus.Desc
	waitSeconds *prometheus.Desc
}

var redisPools = &redisPoolCollector{
	clients:     make(map[weak.Pointer[redis.Client]]string),
	connections: prometheus.NewDesc("silo_redis_pool_connections", "Connections and pending requests in active Redis client pools.", []string{redisRoleLabel, "state"}, nil),
	waits:       prometheus.NewDesc("silo_redis_pool_waits", "Cumulative waits/timeouts of currently registered Redis pools; resets on client replacement.", []string{redisRoleLabel, "outcome"}, nil),
	waitSeconds: prometheus.NewDesc("silo_redis_pool_wait_seconds", "Cumulative time waiting in currently registered Redis pools; resets on client replacement.", []string{redisRoleLabel}, nil),
}

func init() { prometheus.MustRegister(redisPools) }
func instrumentRedis(client *redis.Client, role string) *redis.Client {
	role = telemetry.Role(role)
	client.AddHook(redisHook{role})
	redisPools.mu.Lock()
	for p := range redisPools.clients {
		if p.Value() == nil {
			delete(redisPools.clients, p)
		}
	}
	redisPools.clients[weak.Make(client)] = role
	redisPools.mu.Unlock()
	return client
}

// CloseRedisClient removes pool gauges when an owner stops or replaces a client.
func CloseRedisClient(client *redis.Client) error {
	if client == nil {
		return nil
	}
	redisPools.mu.Lock()
	delete(redisPools.clients, weak.Make(client))
	redisPools.mu.Unlock()
	return client.Close()
}
func (c *redisPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.connections
	ch <- c.waits
	ch <- c.waitSeconds
}
func (c *redisPoolCollector) Collect(ch chan<- prometheus.Metric) {
	totals := map[string][7]float64{}
	c.mu.Lock()
	for ref, role := range c.clients {
		client := ref.Value()
		if client == nil {
			delete(c.clients, ref)
			continue
		}
		s := client.PoolStats()
		v := totals[role]
		v[0] += float64(s.TotalConns)
		v[1] += float64(s.IdleConns)
		v[2] += float64(client.Options().PoolSize)
		v[3] += float64(s.PendingRequests)
		v[4] += float64(s.WaitCount)
		v[5] += float64(s.Timeouts)
		v[6] += float64(s.WaitDurationNs) / 1e9
		totals[role] = v
	}
	c.mu.Unlock()
	for role, v := range totals {
		for i, state := range []string{"total", "idle", "configured_size", "pending_requests"} {
			ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, v[i], role, state)
		}
		ch <- prometheus.MustNewConstMetric(c.waits, prometheus.GaugeValue, v[4], role, "waited")
		ch <- prometheus.MustNewConstMetric(c.waits, prometheus.GaugeValue, v[5], role, "timeout")
		ch <- prometheus.MustNewConstMetric(c.waitSeconds, prometheus.GaugeValue, v[6], role)
	}
}

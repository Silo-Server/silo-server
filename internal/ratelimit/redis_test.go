package ratelimit

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
)

func TestRedisWindowLimitsHonorBurst(t *testing.T) {
	sec, minute, effective := redisWindowLimits(Rate{
		RequestsPerSecond: 1,
		RequestsPerMinute: 60,
		Burst:             30,
	})
	if sec != 30 || minute != 60 || effective != 30 {
		t.Fatalf("limits = (%d, %d, %d), want (30, 60, 30)", sec, minute, effective)
	}
}

func TestRedisWindowLimitsKeepMinuteOnlyRatesUsable(t *testing.T) {
	sec, minute, _ := redisWindowLimits(Rate{
		RequestsPerSecond: 10.0 / 60.0,
		RequestsPerMinute: 10,
		Burst:             6,
	})
	if sec != 6 || minute != 10 {
		t.Fatalf("limits = (%d, %d), want burst 6 and minute 10", sec, minute)
	}
}

func TestRedisRetryAfterWaitsForAFullMinuteWindow(t *testing.T) {
	minuteStart := time.Unix(1_800_000_000-1_800_000_000%60, 0)
	const secTTL = 2 * time.Second

	tests := []struct {
		name     string
		second   int
		minCount int
		want     time.Duration
	}{
		{name: "minute window has room", second: 10, minCount: 20, want: secTTL},
		{name: "minute window full", second: 10, minCount: 30, want: 50 * time.Second},
		// The second key's cleanup TTL outlives the window; both roll over at the minute.
		{name: "minute window full in its last second", second: 59, minCount: 30, want: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := minuteStart.Add(time.Duration(tt.second) * time.Second)
			got := redisRetryAfter(now, tt.minCount, 30, secTTL)
			if got != tt.want {
				t.Fatalf("retry after = %s, want %s", got, tt.want)
			}
		})
	}
}

// commandHook counts the commands a client starts, each once however many
// times go-redis retries it. With answer set, it answers them in place of a
// server.
type commandHook struct {
	started atomic.Int64
	answer  func(redis.Cmder) error
}

func (h *commandHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *commandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.started.Add(1)
		if h.answer != nil {
			return h.answer(cmd)
		}
		return next(ctx, cmd)
	}
}

func (h *commandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// hookedRedisLimiter returns a limiter on a client built the way the server
// builds the rate limiter's, for a Redis at addr, with a short call timeout
// so tests do not wait out the real one.
func hookedRedisLimiter(t *testing.T, addr string, hook *commandHook) *RedisLimiter {
	t.Helper()
	client, err := cache.NewDeadlineRedisClientForRole(config.RedisConfig{URL: "redis://" + addr}, "tasks")
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	t.Cleanup(func() { _ = cache.CloseRedisClient(client) })
	client.AddHook(hook)
	rl := NewRedisLimiter(client)
	rl.callTimeout = 100 * time.Millisecond
	return rl
}

// refusingRedisAddr returns a local address nothing listens on.
func refusingRedisAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// silentRedisAddr returns a local address that accepts connections and never
// answers, like a Redis that stopped responding without closing them.
func silentRedisAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return ln.Addr().String()
}

// A Redis outage must not delay requests: the first check gives up on Redis
// within its timeout, and the checks after it skip Redis.
func TestRedisLimiterFailsOpenQuicklyWhileRedisIsDown(t *testing.T) {
	rate := Rate{RequestsPerSecond: 10, RequestsPerMinute: 600, Burst: 10}
	for _, tt := range []struct {
		name string
		addr func(*testing.T) string
	}{
		{name: "connection refused", addr: refusingRedisAddr},
		{name: "no answer", addr: silentRedisAddr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			commands := &commandHook{}
			rl := hookedRedisLimiter(t, tt.addr(t), commands)

			start := time.Now()
			first := rl.Allow(t.Context(), "ip:192.0.2.1", rate)
			elapsed := time.Since(start)
			t.Logf("first check: %s", elapsed)
			if !first.Allowed || first.Remaining != -1 {
				t.Fatalf("first check = %+v, want allowed with remaining -1", first)
			}
			// Unbounded, a check waits out go-redis's retries: about 2 s.
			if elapsed >= time.Second {
				t.Fatalf("first check took %s, want it to give up on Redis within its timeout", elapsed)
			}
			sent := commands.started.Load()
			if sent == 0 {
				t.Fatal("first check never tried Redis")
			}

			// The request's other checks, and the requests after it, skip Redis.
			start = time.Now()
			for _, key := range []string{"key:1", "account:1", "global", "ip:192.0.2.2"} {
				if got := rl.Allow(t.Context(), key, rate); !got.Allowed || got.Remaining != -1 {
					t.Fatalf("check %s during the pause = %+v, want allowed with remaining -1", key, got)
				}
			}
			t.Logf("four checks during the pause: %s", time.Since(start))
			if got := commands.started.Load(); got != sent {
				t.Fatalf("checks during the pause sent %d commands to Redis, want none", got-sent)
			}
		})
	}
}

// Once a pause runs out, checks still do not wait on Redis: one request is
// counted in the background, and the limiter counts requests again as soon as
// Redis answers. If the pause never ended, rate limiting would stay off.
func TestRedisLimiterCountsAgainOnceRedisAnswers(t *testing.T) {
	rate := Rate{RequestsPerSecond: 10, RequestsPerMinute: 600, Burst: 10}
	var answering atomic.Bool
	commands := &commandHook{answer: func(cmd redis.Cmder) error {
		if !answering.Load() {
			err := errors.New("dial tcp: connect: connection refused")
			cmd.SetErr(err)
			return err
		}
		// allowed, second count, second limit, minute count, minute limit, TTL
		cmd.(*redis.Cmd).SetVal([]any{int64(1), int64(1), int64(10), int64(1), int64(600), int64(1)})
		return nil
	}}
	rl := hookedRedisLimiter(t, refusingRedisAddr(t), commands)
	const key = "ip:192.0.2.1"
	now := time.Now()

	rl.allowAt(t.Context(), key, rate, now)
	if got := rl.allowAt(t.Context(), key, rate, now.Add(time.Second)); got.Remaining != -1 || commands.started.Load() != 1 {
		t.Fatalf("check during the pause = %+v after %d commands, want remaining -1 after 1", got, commands.started.Load())
	}

	// Still down: the background count fails and the next pause holds.
	later := now.Add(redisOutagePause)
	if got := rl.allowAt(t.Context(), key, rate, later); got.Remaining != -1 {
		t.Fatalf("first check after the pause = %+v, want remaining -1", got)
	}
	waitUntil(t, "the background count never ran", func() bool { return commands.started.Load() == 2 })
	if got := rl.allowAt(t.Context(), key, rate, later.Add(time.Second)); got.Remaining != -1 || commands.started.Load() != 2 {
		t.Fatalf("check during the next pause = %+v after %d commands, want remaining -1 after 2", got, commands.started.Load())
	}

	// Answering again: the first check after the pause still fails open at
	// once, and the checks after the background count reach Redis.
	answering.Store(true)
	later = later.Add(redisOutagePause)
	if got := rl.allowAt(t.Context(), key, rate, later); got.Remaining != -1 {
		t.Fatalf("first check after the pause = %+v, want it to fail open without waiting", got)
	}
	waitUntil(t, "checks kept skipping Redis after it answered", func() bool {
		return rl.allowAt(t.Context(), key, rate, later).Remaining >= 0
	})
}

// waitUntil fails the test unless done reports true within a few seconds.
func waitUntil(t *testing.T, failure string, done func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case <-deadline:
			t.Fatal(failure)
		case <-time.After(time.Millisecond):
		}
	}
}

// A caller that gives up says nothing about Redis, so it must not pause the
// limiter; otherwise a client that drops its requests could keep rate
// limiting off.
func TestRedisLimiterCanceledCallerDoesNotPause(t *testing.T) {
	rate := Rate{RequestsPerSecond: 10, RequestsPerMinute: 600, Burst: 10}
	commands := &commandHook{}
	rl := hookedRedisLimiter(t, refusingRedisAddr(t), commands)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := rl.Allow(ctx, "ip:192.0.2.1", rate); !got.Allowed || got.Remaining != -1 {
		t.Fatalf("canceled check = %+v, want allowed with remaining -1", got)
	}
	sent := commands.started.Load()
	rl.Allow(t.Context(), "ip:192.0.2.1", rate)
	if commands.started.Load() == sent {
		t.Fatal("a canceled caller paused the limiter")
	}
}

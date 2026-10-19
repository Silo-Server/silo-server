package ratelimit

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// These finite integration tests are deliberately environment-gated. Adapter
// hooks do not execute Lua. No Lua/atomicity claim is supported when these skip.
func workLuaClient(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("unexecuted Lua integration: requires an explicitly authorized disposable Redis")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal("invalid test Redis URL") // Do not include a credential-bearing URL.
	}
	options.ContextTimeoutEnabled = true
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func workLuaKey(t *testing.T, client *redis.Client) string {
	t.Helper()
	key := workRedisPrefix + "synthetic-lua:" + uuid.NewString()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := client.Del(ctx, key).Err(); err != nil {
			t.Error("could not remove task-owned Lua test key")
		}
	})
	return key
}

func TestWorkRedisLuaRetainedWindowsAndExactExpiry(t *testing.T) {
	client := workLuaClient(t)
	key := workLuaKey(t, client)
	stamp, err := client.Time(t.Context()).Result()
	if err != nil {
		t.Fatal("test Redis TIME failed")
	}
	// Substitute only TIME in the production body, so finite clock jumps need
	// neither wall sleeps nor changes to the server clock. Future logical times
	// keep the container hash alive on Redis's actual physical expiry clock.
	if strings.Count(workLua, "redis.call('TIME')") != 1 {
		t.Fatal("test TIME substitution no longer matches the production script")
	}
	script := redis.NewScript(strings.Replace(workLua, "redis.call('TIME')", "{ARGV[3], ARGV[4]}", 1))
	start := stamp.Truncate(time.Minute).Add(time.Minute + 123456*time.Microsecond)
	decide := func(at time.Time, limit WorkLimit) WorkResult {
		t.Helper()
		raw, err := script.Run(t.Context(), client, []string{key}, limit.PerSecond, limit.PerMinute,
			strconv.FormatInt(at.Unix(), 10), strconv.Itoa(at.Nanosecond()/1000)).Result()
		if err != nil {
			t.Fatal("test work Lua execution failed")
		}
		return parseWorkResult(raw)
	}
	limit := WorkLimit{1, 3}
	requireWork(t, decide(start, limit), true, 0)
	second := "s:" + strconv.FormatInt(start.Unix(), 10)
	minute := "m:" + strconv.FormatInt(start.Unix()/60, 10)
	before, err := client.HGetAll(t.Context(), key).Result()
	if err != nil {
		t.Fatal("test hash read failed")
	}
	wantExpiry := "1:" + strconv.FormatInt(start.UnixMicro()+2_000_000, 10)
	if before[second] != wantExpiry || before[minute] != "1:"+strconv.FormatInt(start.UnixMicro()+120_000_000, 10) {
		t.Fatalf("logical expiry = %#v", before)
	}
	requireWork(t, decide(start.Add(time.Second), limit), true, 0)
	expiry, err := client.PExpireTime(t.Context(), key).Result()
	if err != nil {
		t.Fatal("test expiry read failed")
	}
	requireWork(t, decide(start, limit), false, 877*time.Millisecond)
	afterExpiry, err := client.PExpireTime(t.Context(), key).Result()
	if err != nil || afterExpiry != expiry {
		t.Fatal("denial changed physical key expiry")
	}
	after, err := client.HGetAll(t.Context(), key).Result()
	if err != nil || after[second] != before[second] || after[minute] != "2:"+strconv.FormatInt(start.UnixMicro()+120_000_000, 10) {
		t.Fatal("revisited window lost a retained count or moved logical expiry")
	}
	requireWork(t, decide(start.Add(2*time.Second), limit), true, 0)
	if client.HExists(t.Context(), key, second).Val() {
		t.Fatal("logical second not removed at exact expiry")
	}
	requireWork(t, decide(start.Add(3*time.Second), limit), false, 56_877*time.Millisecond)
	requireWork(t, decide(start.Add(time.Minute), limit), true, 0)
	requireWork(t, decide(start.Add(3*time.Second), limit), false, 56_877*time.Millisecond)
	requireWork(t, decide(start.Add(2*time.Minute), limit), true, 0)
	if client.HExists(t.Context(), key, minute).Val() {
		t.Fatal("logical minute not removed at exact expiry")
	}
}

func TestWorkRedisLuaStoreTimeAndMalformedState(t *testing.T) {
	client := workLuaClient(t)
	key := workLuaKey(t, client)
	ctx := t.Context()
	raw, err := workScript.Run(ctx, client, []string{key}, 1, 1).Result()
	if err != nil {
		t.Fatal("production work Lua execution failed")
	}
	requireWork(t, parseWorkResult(raw), true, 0)
	if err := client.HSet(ctx, key, "unexpected", "1:1").Err(); err != nil {
		t.Fatal("could not seed synthetic malformed state")
	}
	raw, err = workScript.Run(ctx, client, []string{key}, 1, 1).Result()
	if err != nil || !parseWorkResult(raw).Unavailable {
		t.Fatal("malformed hash did not refuse work")
	}
}

package autoscan

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyRecorder answers every command itself, so the test sees the keys the
// suppressor uses without a Redis server. The claim and release scripts run
// as EVALSHA (or EVAL), whose first key follows the script and key count.
type keyRecorder struct{ keys []string }

func (r *keyRecorder) DialHook(next redis.DialHook) redis.DialHook { return next }

func (r *keyRecorder) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		if name := strings.ToLower(cmd.Name()); name == "evalsha" || name == "eval" {
			r.keys = append(r.keys, fmt.Sprint(cmd.Args()[3]))
		}
		if script, ok := cmd.(*redis.Cmd); ok {
			script.SetVal(int64(1))
		}
		return nil
	}
}

func (r *keyRecorder) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestSuppressorKeysAreUnderTheSiloPrefix(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	recorder := &keyRecorder{}
	client.AddHook(recorder)
	suppressor := NewRedisSuppressor(client)

	key := "1|/data/movies/Example Movie (2024)/Example Movie (2024).mkv"
	claimed, err := suppressor.ShouldScan(t.Context(), key, stateAbsent, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("ShouldScan = (%v, %v), want (true, nil)", claimed, err)
	}
	if err := suppressor.Release(t.Context(), key, stateAbsent); err != nil {
		t.Fatalf("Release: %v", err)
	}

	want := "silo:autoscan:scanned:" + key
	if len(recorder.keys) != 2 || recorder.keys[0] != want || recorder.keys[1] != want {
		t.Fatalf("keys = %q, want the claim and release scripts on %q", recorder.keys, want)
	}
}

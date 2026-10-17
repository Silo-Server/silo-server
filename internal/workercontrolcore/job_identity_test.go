package workercontrolcore

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestAssignmentJobIdentitySurvivesReplacementAndCompletion(t *testing.T) {
	stores := map[string]AssignmentStore{"memory": NewMemoryStore()}
	if address := os.Getenv("SILO_WORKER_TEST_REDIS"); address != "" {
		client := redis.NewClient(&redis.Options{Addr: address})
		prefix := "silo:job-identity-test:" + uuid.NewString() + ":"
		stores["redis"] = NewRedisStore(client, prefix)
		t.Cleanup(func() {
			keys, _ := client.Keys(context.Background(), prefix+"*").Result()
			if len(keys) > 0 {
				_ = client.Del(context.Background(), keys...).Err()
			}
			_ = client.Close()
		})
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			for _, original := range []string{"", "parent-playback"} {
				work := "work-" + uuid.NewString()
				a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: work, JobID: original})
				if err != nil {
					t.Fatal(err)
				}
				a.JobID = "complete-must-not-regroup"
				if err := store.Complete(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				if err := store.Activate(t.Context(), 7, work, a.Generation); err != nil {
					t.Fatal(err)
				}
				live, found, err := store.Get(t.Context(), 7, work)
				if err != nil || !found || live.JobID != original {
					t.Fatalf("Complete changed authoritative identity: got=%q want=%q err=%v", live.JobID, original, err)
				}
				a.JobID = "replacement-must-not-regroup"
				next, err := store.Begin(t.Context(), a)
				if err != nil || next.JobID != original {
					t.Fatalf("Begin changed live identity: got=%q want=%q err=%v", next.JobID, original, err)
				}
				if err := store.Stop(t.Context(), 7, work); err != nil {
					t.Fatal(err)
				}
			}
			for _, invalid := range []string{"../escape", "x\x00y", ".", "..", strings.Repeat("a", 161)} {
				if _, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "invalid-job", JobID: invalid}); err == nil {
					t.Errorf("invalid accounting identity %q admitted", invalid)
				}
			}
			if redisStore, ok := store.(*RedisStore); ok {
				t.Run("stale observations cannot mutate records", func(t *testing.T) {
					assertStaleJobObservationDoesNotMutate(t, redisStore)
				})
			}
		})
	}
}

func assertStaleJobObservationDoesNotMutate(t *testing.T, store *RedisStore) {
	t.Helper()
	work := "cas-" + uuid.NewString()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: work, JobID: "original"})
	if err != nil {
		t.Fatal(err)
	}
	keys := store.keys(7)
	observed, err := store.client.HGet(t.Context(), keys[1], work).Result()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	current, err := store.client.HGet(t.Context(), keys[1], work).Result()
	if err != nil {
		t.Fatal(err)
	}
	a.JobID = "must-not-replace-original"
	encoded, _ := json.Marshal(a)
	for _, attempt := range []struct {
		name string
		run  func() (int, error)
	}{
		{"Begin", func() (int, error) {
			return beginScript.Run(t.Context(), store.client, keys, work, string(encoded), "", "", observed).Int()
		}},
		{"Complete", func() (int, error) {
			return completeScript.Run(t.Context(), store.client, keys, work, "1", string(encoded), observed).Int()
		}},
	} {
		result, err := attempt.run()
		if err != nil || result != -3 {
			t.Fatalf("%s accepted changed record: result=%d err=%v", attempt.name, result, err)
		}
		generation, err := store.client.HGet(t.Context(), keys[0], work).Result()
		if err != nil || generation != "1" {
			t.Fatalf("stale %s advanced generation: %q %v", attempt.name, generation, err)
		}
		retained, err := store.client.HGet(t.Context(), keys[1], work).Result()
		if err != nil || retained != current {
			t.Fatalf("stale %s rewrote authoritative record: %v", attempt.name, err)
		}
	}
}

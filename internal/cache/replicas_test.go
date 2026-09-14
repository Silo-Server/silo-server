package cache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestAPIReplicaPresenceNilReportsOneReplica(t *testing.T) {
	var presence *APIReplicaPresence
	if err := presence.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count, err := presence.Count(context.Background()); err != nil || count != 1 {
		t.Fatalf("Count on nil presence = (%d, %v), want (1, nil)", count, err)
	}
	if NewAPIReplicaPresence(nil, "a") != nil || NewAPIReplicaPresence(redis.NewClient(&redis.Options{}), "") != nil {
		t.Fatal("presence built without a client or an id")
	}
}

// Two replicas registering see each other; one whose context ends drops its
// marker so the census shrinks again.
func TestAPIReplicaPresenceCountsLiveReplicas(t *testing.T) {
	rawURL := os.Getenv("SILO_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	baseline, err := (&APIReplicaPresence{client: client, id: "census-" + unique, ttl: time.Minute, refresh: time.Minute}).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}

	newPresence := func(id string) *APIReplicaPresence {
		p := NewAPIReplicaPresence(client, id+"-"+unique)
		p.ttl, p.refresh = time.Minute, 10*time.Millisecond
		t.Cleanup(func() { _ = client.Del(context.Background(), p.key()).Err() })
		return p
	}
	first := newPresence("first")
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	if err := first.Register(firstCtx); err != nil {
		t.Fatal(err)
	}
	second := newPresence("second")
	secondCtx, stopSecond := context.WithCancel(ctx)
	defer stopSecond()
	if err := second.Register(secondCtx); err != nil {
		t.Fatal(err)
	}
	if count, err := second.Count(ctx); err != nil || count != baseline+2 {
		t.Fatalf("Count with two replicas = (%d, %v), want %d", count, err, baseline+2)
	}

	stopFirst()
	deadline := time.Now().Add(10 * time.Second)
	for {
		count, err := second.Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if count == baseline+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("count after the first replica left = %d, want %d", count, baseline+1)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

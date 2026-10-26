package cache

import (
	"context"
	"testing"

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

func TestAPIReplicaPresenceSeparatesIdenticalNodeNames(t *testing.T) {
	client := redis.NewClient(&redis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	first := NewAPIReplicaPresence(client, "shared-api-name")
	second := NewAPIReplicaPresence(client, "shared-api-name")
	if first.key() == second.key() {
		t.Fatal("replicas with the same node name share a presence marker")
	}
}

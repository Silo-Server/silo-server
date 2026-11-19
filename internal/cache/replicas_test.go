package cache

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestAPIReplicaPresenceSeparatesIdenticalNodeNames(t *testing.T) {
	client := redis.NewClient(&redis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	first := NewAPIReplicaPresence(client, "shared-api-name")
	second := NewAPIReplicaPresence(client, "shared-api-name")
	if first.key() == second.key() {
		t.Fatal("replicas with the same node name share a presence marker")
	}
}

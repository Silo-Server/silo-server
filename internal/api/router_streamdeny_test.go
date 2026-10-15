package api

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The Jellyfin handlers are built outside the router, so the router's relays
// must check the deny marker store the process handed it: a marker whose
// Redis write failed lives only in that value's local cache, and a store the
// router built for itself would not see it.
func TestRouterSharesTheProcessStreamDeny(t *testing.T) {
	unreachable := func() *redis.Client {
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	process := playback.NewStreamDeny(unreachable())
	process.Deny(context.Background(), "stopped-session")

	shared := sharedStreamDeny(process, unreachable())
	if shared != process {
		t.Fatal("router built its own deny marker store instead of the process's")
	}
	if !shared.Denied(context.Background(), "stopped-session") {
		t.Fatal("a marker cached by the process store does not refuse on the router's")
	}
	if own := sharedStreamDeny(nil, unreachable()); own == nil {
		t.Fatal("router without a process store built none from its Redis client")
	}
}

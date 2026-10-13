package historyimport

import (
	"context"
	"net/url"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestUpstreamRateLimiter_LimitsPerServer(t *testing.T) {
	t.Parallel()

	limiter := newUpstreamRateLimiter(rate.Every(200*time.Millisecond), 1)
	serverA, err := url.Parse("https://plex.example/library/metadata/1")
	if err != nil {
		t.Fatalf("Parse(serverA): %v", err)
	}
	serverB, err := url.Parse("https://emby.example/Users/1/Items")
	if err != nil {
		t.Fatalf("Parse(serverB): %v", err)
	}

	if err := limiter.Wait(context.Background(), serverA); err != nil {
		t.Fatalf("first wait: %v", err)
	}

	limitedCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = limiter.Wait(limitedCtx, serverA)
	if err == nil {
		t.Fatal("second wait succeeded, want same-server request to be limited")
	}

	if err := limiter.Wait(context.Background(), serverB); err != nil {
		t.Fatalf("different server wait: %v", err)
	}
}

func TestHistoryImportClients_ShareServerLimiter(t *testing.T) {
	t.Parallel()

	plex := NewPlexClient()
	emby := NewEmbyClient()
	jellyfin := NewJellyfinClient()
	if plex.limiter == nil || plex.limiter != emby.limiter || plex.limiter != jellyfin.limiter {
		t.Fatal("expected Plex, Emby, and Jellyfin clients to share the same upstream limiter")
	}
}

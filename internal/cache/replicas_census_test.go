package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newCensusClient(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

// Count includes the caller even when its marker is missing, so another
// process's lone marker is never read as the caller's own.
func TestAPIReplicaCountIncludesOwnMarker(t *testing.T) {
	_, client := newCensusClient(t)
	ctx := t.Context()
	first := NewAPIReplicaPresence(client, "api")
	second := NewAPIReplicaPresence(client, "api")
	if err := first.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// second never wrote a marker; first's lone marker must not count as second's.
	if got, err := second.Count(ctx); err != nil || got != 2 {
		t.Fatalf("Count with a foreign marker and no own marker = %d, %v; want 2, nil", got, err)
	}
	if got, err := first.Count(ctx); err != nil || got != 2 {
		t.Fatalf("Count after both are present = %d, %v; want 2, nil", got, err)
	}
}

// A marker expires on Redis time once its TTL passes, and withdrawal removes it.
func TestAPIReplicaMarkersExpireAndWithdraw(t *testing.T) {
	mr, client := newCensusClient(t)
	ctx := t.Context()
	base := time.Now()
	mr.SetTime(base)
	first := NewAPIReplicaPresence(client, "api")
	second := NewAPIReplicaPresence(client, "api")
	for _, p := range []*APIReplicaPresence{first, second} {
		if err := p.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := first.Count(ctx); err != nil || got != 2 {
		t.Fatalf("Count = %d, %v; want 2", got, err)
	}
	mr.SetTime(base.Add(APIReplicaTTL - time.Second))
	if got, err := first.Count(ctx); err != nil || got != 2 {
		t.Fatalf("Count before the TTL = %d, %v; want 2", got, err)
	}
	// Only first refreshes; second's marker lapses at base+TTL.
	if err := first.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	mr.SetTime(base.Add(APIReplicaTTL + time.Second))
	if got, err := first.Count(ctx); err != nil || got != 1 {
		t.Fatalf("Count after second's marker lapsed = %d, %v; want 1", got, err)
	}
	first.withdraw()
	if got, err := second.Count(context.WithoutCancel(ctx)); err != nil || got != 1 {
		t.Fatalf("Count after first withdrew = %d, %v; want 1 (second's own marker only)", got, err)
	}
	if got, err := first.Count(ctx); err != nil || got != 1 {
		t.Fatalf("withdrawn Count = %d, %v; want 1 and no marker written", got, err)
	}
}

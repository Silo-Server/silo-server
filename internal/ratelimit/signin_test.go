package ratelimit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// Once a name has used its budget, only addresses known for it sign in, each
// on a budget of its own; an IPv6 client counts by its /64, and a request
// with no address is never known.
func TestSignInLimiterKnownAddressesGetTheirOwnBudget(t *testing.T) {
	ctx := context.Background()
	policy := AttemptPolicy{MaxAttempts: 2, Window: time.Minute}
	l := NewMemorySignInLimiter(policy, policy)
	const name = "signin_name:alice"
	home, homeOtherAddr := "2001:db8:1:2::10", "2001:db8:1:2::99"
	stranger := "192.0.2.50"

	reserve := func(source string) (auth.SignInLane, bool) {
		t.Helper()
		lane, retryAfter, ok := l.Reserve(ctx, name, source)
		if !ok && retryAfter <= 0 {
			t.Fatalf("refusal from %q has no time left", source)
		}
		return lane, ok
	}

	l.Trust(ctx, []string{name}, home)
	for range policy.MaxAttempts {
		if lane, ok := reserve(stranger); !ok || lane != auth.SignInLaneName {
			t.Fatalf("stranger within the name's budget: lane %v ok %v", lane, ok)
		}
	}
	for _, source := range []string{stranger, "198.51.100.1", ""} {
		if _, ok := reserve(source); ok {
			t.Fatalf("locked name: unknown address %q allowed", source)
		}
	}
	for _, source := range []string{home, homeOtherAddr} {
		if lane, ok := reserve(source); !ok || lane != auth.SignInLaneSource {
			t.Fatalf("locked name: known address %s: lane %v ok %v, want its own budget", source, lane, ok)
		}
	}
	if _, ok := reserve(home); ok {
		t.Fatal("known address past its own budget allowed")
	}
	l.Reset(ctx, name, home, auth.SignInLaneSource)
	if lane, ok := reserve(homeOtherAddr); !ok || lane != auth.SignInLaneSource {
		t.Fatalf("after a reset: lane %v ok %v", lane, ok)
	}
	if _, ok := reserve(stranger); ok {
		t.Fatal("resetting a known address unlocked the name")
	}
}

// A name keeps only the addresses it used most recently, in either store, so
// refreshes that each name a new address cannot grow the store without
// bound; an address stops being known when it expires.
func TestSignInTrustKeepsTheMostRecentAddresses(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clock := time.Unix(1_800_000_000, 0)
	now := func() time.Time { return clock }
	const name = "signin_name:alice"
	entry := func(i int) signInTrustEntry {
		return signInTrustEntry{nameKey: name, pair: signInSourcePair(name, fmt.Sprintf("192.0.2.%d", i))}
	}

	for label, store := range map[string]signInTrustBackend{
		"memory": newMemorySignInTrust(now),
		"redis":  redisSignInTrust{client: client, now: now},
	} {
		t.Run(label, func(t *testing.T) {
			ctx := context.Background()
			for i := range SignInTrustMaxAddresses + 1 {
				clock = clock.Add(time.Second)
				if err := store.remember(ctx, []signInTrustEntry{entry(i)}, SignInTrustTTL); err != nil {
					t.Fatal(err)
				}
			}
			for i, want := range map[int]bool{0: false, 1: true, SignInTrustMaxAddresses: true} {
				if got, err := store.known(ctx, entry(i)); err != nil || got != want {
					t.Errorf("address %d known = %v (err %v), want %v", i, got, err, want)
				}
			}
			clock = clock.Add(SignInTrustTTL)
			if got, err := store.known(ctx, entry(SignInTrustMaxAddresses)); err != nil || got {
				t.Errorf("expired address known = %v (err %v)", got, err)
			}
		})
	}
}

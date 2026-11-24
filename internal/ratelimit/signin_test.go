package ratelimit

import (
	"context"
	"testing"
	"time"

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

package notifications

import (
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// The Redis store keeps the profile token beside the ticket, leaving the
// ticket value in the format older nodes read, and consumes a ticket an older
// node minted as one without PIN proof.
func TestRedisTicketStoreKeepsProfileToken(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("SILO_TEST_REDIS_URL required for the Redis ticket store")
	}
	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	store := NewTicketStore(client)

	minted := Ticket{UserID: 7, ProfileID: "profile-1", ProfileToken: "proof"}
	ticket, _, err := store.Mint(t.Context(), minted)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := client.Get(t.Context(), redisTicketPrefix+ticket).Result(); err != nil || value != "7|profile-1" {
		t.Fatalf("ticket value = %q, %v; want the format older nodes read", value, err)
	}
	if binding, ok := store.Consume(t.Context(), ticket); !ok || binding != minted {
		t.Fatalf("consume returned %+v %v", binding, ok)
	}
	if _, ok := store.Consume(t.Context(), ticket); ok {
		t.Fatal("tickets must be single-use")
	}

	if err := client.Set(t.Context(), redisTicketPrefix+"legacy-fixture", "7|profile-1", TicketTTL).Err(); err != nil {
		t.Fatal(err)
	}
	if binding, ok := store.Consume(t.Context(), "legacy-fixture"); !ok || binding != (Ticket{UserID: 7, ProfileID: "profile-1"}) {
		t.Fatalf("legacy value consumed as %+v %v", binding, ok)
	}
}

// A ticket's proof is never addressable as a ticket: the caller chooses the
// profile token, so a proof key read as "userID|profileID" would bind any
// profile on the account without its PIN, on this node or an older one.
func TestRedisTicketStoreProofIsNotATicket(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("SILO_TEST_REDIS_URL required for the Redis ticket store")
	}
	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	store := NewTicketStore(client)

	ticket, _, err := store.Mint(t.Context(), Ticket{UserID: 7, ProfileID: "open-profile", ProfileToken: "7|locked-profile"})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := client.Keys(t.Context(), redisTicketPrefix+ticket+"*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		suffix := strings.TrimPrefix(key, redisTicketPrefix)
		if suffix == ticket {
			continue
		}
		if binding, ok := store.Consume(t.Context(), suffix); ok {
			t.Fatalf("consumed %q as a ticket bound to %+v", suffix, binding)
		}
	}
	if binding, ok := store.Consume(t.Context(), ticket); !ok || binding.ProfileID != "open-profile" || binding.ProfileToken != "7|locked-profile" {
		t.Fatalf("consume returned %+v %v", binding, ok)
	}
}

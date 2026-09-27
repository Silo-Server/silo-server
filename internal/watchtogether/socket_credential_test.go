package watchtogether

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/redis/go-redis/v9"
)

func roomSocketRedis(t *testing.T) (*redis.Client, *redis.Client) {
	t.Helper()
	endpoint := os.Getenv("SILO_ROOM_SOCKET_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires isolated room socket Redis")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	a, b := redis.NewClient(options), redis.NewClient(options)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	if err = a.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	return a, b
}
func roomSocketCredential() RoomSocketCredential {
	return RoomSocketCredential{RoomID: "synthetic-room", Session: events.SocketIdentity{UserID: 7, SessionID: "login-session", ProfileID: "profile", ProfileToken: "synthetic-pin-proof", Role: "user", EffectiveRole: "user", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)}, RoomProofExpiresAt: time.Now().Add(time.Minute)}
}
func TestRoomSocketCredentialRedisSingleUse(t *testing.T) {
	a, b := roomSocketRedis(t)
	mint, consume := NewRoomSocketCredentialStore(a), NewRoomSocketCredentialStore(b)
	credential := roomSocketCredential()
	ticket, err := mint.Mint(t.Context(), credential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Del(t.Context(), roomSocketTicketPrefix+ticket).Err() })
	ttl, err := a.PTTL(t.Context(), roomSocketTicketPrefix+ticket).Result()
	if err != nil || ttl <= 0 || ttl > RoomSocketTicketTTL {
		t.Fatalf("ttl %v %v", ttl, err)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			got, err := consume.Consume(t.Context(), ticket, credential.RoomID)
			if err == nil {
				success.Add(1)
				if got.RoomID != credential.RoomID || got.Session.SessionID != credential.Session.SessionID || got.Session.ProfileToken != credential.Session.ProfileToken || got.Session.AccessFingerprint != credential.Session.AccessFingerprint {
					t.Error("authority changed")
				}
			}
		})
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("successful consumes %d", success.Load())
	}
	if n, err := a.Exists(t.Context(), roomSocketTicketPrefix+ticket).Result(); err != nil || n != 0 {
		t.Fatal("ticket retained", err)
	}
}
func TestRoomSocketCredentialRedisBoundsAndRefusals(t *testing.T) {
	a, b := roomSocketRedis(t)
	store := NewRoomSocketCredentialStore(a)
	for _, which := range []string{"access", "room"} {
		t.Run(which, func(t *testing.T) {
			credential := roomSocketCredential()
			expiry := time.Now().Add(10 * time.Second)
			if which == "access" {
				credential.Session.AccessExpiresAt = expiry
			} else {
				credential.RoomProofExpiresAt = expiry
			}
			ticket, err := store.Mint(t.Context(), credential)
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Consume(t.Context(), ticket, credential.RoomID)
			if err != nil || !got.ExpiresAt.Equal(expiry) {
				t.Fatalf("expiry %v %v", got.ExpiresAt, err)
			}
		})
	}
	credential := roomSocketCredential()
	ticket, err := store.Mint(t.Context(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Consume(t.Context(), ticket, "other-room"); !errors.Is(err, ErrRoomSocketCredential) {
		t.Fatal("foreign room accepted", err)
	}
	if _, err = store.Consume(t.Context(), ticket, credential.RoomID); !errors.Is(err, ErrRoomSocketCredential) {
		t.Fatal("mismatch did not burn", err)
	}
	for _, which := range []string{"ticket", "access", "room"} {
		t.Run("expired_"+which, func(t *testing.T) {
			value := roomSocketCredential()
			value.ExpiresAt = time.Now().Add(time.Minute)
			expired := time.Now().Add(-time.Second)
			switch which {
			case "ticket":
				value.ExpiresAt = expired
			case "access":
				value.Session.AccessExpiresAt = expired
			case "room":
				value.RoomProofExpiresAt = expired
			}
			// Persist an already-expired payload with a live Redis TTL: consumption
			// must enforce signed/delegated time bounds independently of key expiry.
			payload, _ := json.Marshal(value)
			key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			if err := a.Set(t.Context(), roomSocketTicketPrefix+key, payload, time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Consume(t.Context(), key, value.RoomID); !errors.Is(err, ErrRoomSocketCredential) {
				t.Fatal("expired accepted", err)
			}
		})
	}
	// An event credential key is neither read nor burned by the room store.
	key := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA"
	foreignKey := "silo:events:v2:ticket:" + key
	if err = a.Set(t.Context(), foreignKey, "synthetic-other-purpose", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Del(t.Context(), foreignKey).Err() }()
	if _, err = store.Consume(t.Context(), key, credential.RoomID); err == nil {
		t.Fatal("foreign namespace accepted")
	}
	if n, _ := a.Exists(t.Context(), foreignKey).Result(); n != 1 {
		t.Fatal("foreign namespace burned")
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	failed := NewRoomSocketCredentialStore(b)
	if _, err = failed.Mint(t.Context(), credential); !errors.Is(err, ErrRoomSocketCredential) {
		t.Fatal("storage failure mint", err)
	}
	if _, err = failed.Consume(t.Context(), key, credential.RoomID); !errors.Is(err, ErrRoomSocketCredential) {
		t.Fatal("storage failure consume", err)
	}
}
func TestRoomSocketCredentialRequiresAuthorityAndStorage(t *testing.T) {
	var absent *RoomSocketCredentialStore
	for _, store := range []*RoomSocketCredentialStore{absent, NewRoomSocketCredentialStore(nil)} {
		if _, err := store.Mint(t.Context(), roomSocketCredential()); !errors.Is(err, ErrRoomSocketCredential) {
			t.Fatal(err)
		}
		if _, err := store.Consume(t.Context(), "bad", "room"); !errors.Is(err, ErrRoomSocketCredential) {
			t.Fatal(err)
		}
	}
	a, _ := roomSocketRedis(t)
	store := NewRoomSocketCredentialStore(a)
	for _, field := range []string{"room", "session", "profile", "scope", "user", "access_expiry", "room_expiry"} {
		c := roomSocketCredential()
		switch field {
		case "room":
			c.RoomID = ""
		case "session":
			c.Session.SessionID = ""
		case "profile":
			c.Session.ProfileID = ""
		case "scope":
			c.Session.AccessFingerprint = ""
		case "user":
			c.Session.UserID = 0
		case "access_expiry":
			c.Session.AccessExpiresAt = time.Time{}
		case "room_expiry":
			c.RoomProofExpiresAt = time.Time{}
		}
		if _, err := store.Mint(t.Context(), c); !errors.Is(err, ErrRoomSocketCredential) {
			t.Fatalf("%s: %v", field, err)
		}
	}
}

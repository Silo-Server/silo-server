package cache

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestRedisClientsTakeDatabaseNumberFromRedisDB(t *testing.T) {
	for _, cfg := range []config.RedisConfig{
		{URL: "redis://localhost:6379", DB: "5"},
		{URL: "redis://localhost:6379/7", DB: "5"},
		{URL: "redis://sentinel-1:26379/7?master_name=mymaster", DB: "5"},
	} {
		bus := newRedisEventBus(cfg)
		if bus.db != 5 {
			t.Errorf("newRedisEventBus(%+v) scopes channels to database %d, want redis.db's 5", cfg, bus.db)
		}
		if err := bus.Close(); err != nil {
			t.Errorf("close bus for %+v: %v", cfg, err)
		}

		client, err := NewRedisClientForRole(cfg, "test")
		if err != nil {
			t.Fatalf("NewRedisClientForRole(%+v): %v", cfg, err)
		}
		if got := client.Options().DB; got != 5 {
			t.Errorf("NewRedisClientForRole(%+v) selects database %d, want redis.db's 5", cfg, got)
		}
		if err := client.Close(); err != nil {
			t.Errorf("close client for %+v: %v", cfg, err)
		}
	}

	// The URL is fine here, so the error must not blame it.
	client, err := NewRedisClientForRole(config.RedisConfig{URL: "redis://localhost:6379/7", DB: "five"}, "test")
	if err == nil {
		_ = client.Close()
		t.Fatal("NewRedisClientForRole built a client for a redis.db that is not a number")
	}
	if got := err.Error(); !strings.Contains(got, config.RedisDBSettingKey) || strings.Contains(got, "redis URL") {
		t.Errorf("error for a redis.db that is not a number = %q, want one that names redis.db and not the URL", got)
	}
}

// Runs without a Redis server: the name Redis receives must carry the database
// number that the same connection selected.
func TestRedisEventBusSendsScopedChannelToRedis(t *testing.T) {
	bare := []string{"SUBSCRIBE silo:admin on database 0", "PUBLISH silo:admin on database 0"}
	db5 := []string{"SUBSCRIBE silo:admin@db5 on database 5", "PUBLISH silo:admin@db5 on database 5"}
	for _, tc := range []struct {
		name string
		path string
		db   string
		want []string
		bare bool
	}{
		{"database 3", "/3", "", []string{"SUBSCRIBE silo:admin@db3 on database 3", "PUBLISH silo:admin@db3 on database 3"}, false},
		{"database 0", "/0", "", bare, false},
		{"no database number", "", "", bare, false},
		{"negative database number", "/-1", "", bare, false},
		{"redis.db replaces the number in the URL", "/3", "5", db5, false},
		{"redis.db on a URL without a number", "", "5", db5, false},
		{"redis.db 0 replaces the number in the URL", "/3", "0", bare, false},
		{"redis.db on a bare address", "", "5", db5, true},
		{"normalized redis.db on a bare address", "", " 05 ", db5, true},
		{"redis.db 0 on a bare address", "", "0", bare, true},
		{"no redis.db on a bare address", "", "", bare, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := startRESPTestServer(t, nil, respTestRedis)
			redisURL := "redis://" + server.addr + tc.path
			if tc.bare {
				redisURL = server.addr
			}
			bus := newRedisEventBus(config.RedisConfig{URL: redisURL, DB: tc.db})
			t.Cleanup(func() { _ = bus.Close() })

			if err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			if err := bus.Publish(t.Context(), ChannelAdmin, Event{Type: EventSettingsChanged}); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if got := server.pubsub(); !slices.Equal(got, tc.want) {
				t.Errorf("Redis received %q, want %q", got, tc.want)
			}
		})
	}
}

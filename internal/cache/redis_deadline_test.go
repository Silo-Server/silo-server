package cache

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// A deadline client lets a caller's context end a stalled socket read, for a
// single server and for a Sentinel master alike; the ordinary client keeps the
// URL's timeouts alone.
func TestDeadlineRedisClientHonorsContextDeadlines(t *testing.T) {
	for _, rawURL := range []string{
		"redis://127.0.0.1:6379/2?read_timeout=0",
		"redis://127.0.0.1:26379/2?master_name=mymaster",
	} {
		deadline, err := NewDeadlineRedisClientForRole(config.RedisConfig{URL: rawURL}, "api")
		if err != nil {
			t.Fatalf("%s: deadline client: %v", rawURL, err)
		}
		t.Cleanup(func() { _ = deadline.Close() })
		if !deadline.Options().ContextTimeoutEnabled {
			t.Errorf("%s: deadline client ignores context deadlines", rawURL)
		}

		ordinary, err := NewRedisClientForRole(config.RedisConfig{URL: rawURL}, "api")
		if err != nil {
			t.Fatalf("%s: ordinary client: %v", rawURL, err)
		}
		t.Cleanup(func() { _ = ordinary.Close() })
		if ordinary.Options().ContextTimeoutEnabled {
			t.Errorf("%s: ordinary client now honors context deadlines", rawURL)
		}
	}
}

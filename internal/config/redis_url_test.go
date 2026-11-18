package config

import (
	"strings"
	"testing"
)

// The URL carries passwords and its parse errors are logged.
func TestParseRedisURLErrorsDoNotQuotePasswords(t *testing.T) {
	for _, raw := range []string{
		// net/url rejects these two and quotes the whole URL in its error.
		"redis://watcher:sentinel%zzsecret@sentinel-1:26379?master_name=mymaster&password=redis-secret",
		"redis://silo:redis-secret/x@redis.example:6379",
		"redis://watcher:sentinel-secret@sentinel-1:26379?master_name=mymaster&password=redis-secret&route_randomly=true",
		"redis://watcher:sentinel-secret@sentinel-1:26379?master_name=mymaster&password=redis-secret;x",
		"redis://watcher:sentinel-secret@sentinel-1?master_name=mymaster&password=redis-secret",
		"redis://watcher:sentinel-secret@sentinel-1:26379?master_name=&password=redis-secret",
		"redis://silo:redis-secret@redis.example:6379?unknown=1",
		// A reserved character that is not percent-encoded splits a password,
		// and go-redis quotes the part it cannot place.
		"redis://redis.example:6379?password=redis&secret",
		"redis://silo:/secret@redis.example:6379",
		"redis://silo:?secret@redis.example:6379",
		"redis://sentinel-1:26379?master_name=mymaster&password=redis&secret",
		"redis://sentinel-1:26379/secret?master_name=mymaster",
		"redis://sentinel-1:26379?master_name=mymaster&addr=secret",
		"redis://sentinel-1:26379?master_name=mymaster&password=redis#secret",
		"redis://sentinel-1:26379?master_name=mymaster&password=redis+secret",
		"secret://redis.example:6379",
	} {
		_, _, err := ParseRedisURL(raw)
		if err == nil {
			t.Errorf("ParseRedisURL(%q) returned no error", raw)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("ParseRedisURL(%q) quotes a password in its error: %v", raw, err)
		}
	}
}

// A single-server URL that happens to contain the text master_name outside
// its query string worked before Sentinel URLs existed.
func TestParseRedisURLKeepsASingleServerURLThatMentionsTheParameterElsewhere(t *testing.T) {
	options, failover, err := ParseRedisURL("redis://:my_master_name_password@redis.example:6379/2#note")
	if err != nil || failover != nil || options == nil {
		t.Fatalf("ParseRedisURL = (%v, %v, %v), want single-server options", options, failover, err)
	}
	if options.Addr != "redis.example:6379" || options.DB != 2 || options.Password != "my_master_name_password" {
		t.Fatalf("options = addr %q db %d, want redis.example:6379 db 2 and the password", options.Addr, options.DB)
	}
}

package config

import (
	"slices"
	"strings"
	"testing"
	"time"

	redisv9 "github.com/redis/go-redis/v9"
)

func TestParseRedisURLSingleServer(t *testing.T) {
	options, failover, err := ParseRedisURL("  redis://user:secret@redis.example:6380/3\n")
	if err != nil {
		t.Fatal(err)
	}
	if failover != nil {
		t.Fatalf("a URL without master_name parsed as Sentinel: %+v", failover)
	}
	if options.Addr != "redis.example:6380" || options.DB != 3 || options.Username != "user" || options.Password != "secret" {
		t.Fatalf("options = addr %q db %d user %q, want redis.example:6380 db 3 user", options.Addr, options.DB, options.Username)
	}
	// Only a Sentinel URL has to keep its read timeout.
	if _, _, err := ParseRedisURL("redis://redis.example:6379?read_timeout=0"); err != nil {
		t.Fatalf("a single-server URL without a read timeout was refused: %v", err)
	}
}

func TestParseRedisURLSentinel(t *testing.T) {
	raw := "redis://watcher:sentinel-secret@sentinel-1:26379/2?master_name=mymaster" +
		"&addr=sentinel-2:26379&addr=sentinel-3:26379&username=silo&password=redis-secret"
	options, failover, err := ParseRedisURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if options != nil {
		t.Fatalf("a Sentinel URL also returned single-server options: %+v", options)
	}
	wantAddrs := []string{"sentinel-1:26379", "sentinel-2:26379", "sentinel-3:26379"}
	if failover.MasterName != "mymaster" || !slices.Equal(failover.SentinelAddrs, wantAddrs) {
		t.Fatalf("master %q via %v, want mymaster via %v", failover.MasterName, failover.SentinelAddrs, wantAddrs)
	}
	if failover.DB != 2 {
		t.Fatalf("database number = %d, want 2 from the URL path", failover.DB)
	}
	if failover.SentinelUsername != "watcher" || failover.SentinelPassword != "sentinel-secret" {
		t.Fatal("URL user info did not become the Sentinel credentials")
	}
	if failover.Username != "silo" || failover.Password != "redis-secret" {
		t.Fatal("username and password parameters did not become the Redis server credentials")
	}
	// go-redis leaves this at zero, and its Sentinel dialer then dials a
	// subscription's connection with no timeout at all.
	if failover.DialTimeout != 5*time.Second {
		t.Fatalf("dial timeout = %v, want the 5s go-redis uses for a single server", failover.DialTimeout)
	}
	_, failover, err = ParseRedisURL("redis://sentinel-1:26379?master_name=mymaster&dial_timeout=2s")
	if err != nil {
		t.Fatal(err)
	}
	if failover.DialTimeout != 2*time.Second {
		t.Fatalf("dial timeout = %v, want the 2s from the URL", failover.DialTimeout)
	}
	_, failover, err = ParseRedisURL("redis://sentinel-1:26379?master_name=mymaster&read_timeout=4s")
	if err != nil || failover.ReadTimeout != 4*time.Second {
		t.Fatalf("read timeout = %v (%v), want the 4s from the URL", failover.ReadTimeout, err)
	}
	// go-redis reads a duration of zero as "use the default".
	_, failover, err = ParseRedisURL("redis://sentinel-1:26379?master_name=mymaster&read_timeout=0s&dial_timeout=0s")
	if err != nil || failover.ReadTimeout != 0 || failover.DialTimeout != 5*time.Second {
		t.Fatalf("timeouts = read %v, dial %v (%v), want the defaults", failover.ReadTimeout, failover.DialTimeout, err)
	}

	_, failover, err = ParseRedisURL("redis://[2001:db8::1]:26379?master%5Fname=mymaster")
	if err != nil {
		t.Fatal(err)
	}
	if failover == nil || failover.MasterName != "mymaster" || !slices.Equal(failover.SentinelAddrs, []string{"[2001:db8::1]:26379"}) {
		t.Fatalf("a bracketed IPv6 Sentinel with a percent-encoded master_name parsed as %+v", failover)
	}
}

func TestParseRedisURLRejectsInvalid(t *testing.T) {
	for _, raw := range []string{
		"redis://sentinel-1:26379?master_name=",
		"redis://sentinel-1:26379?master_name=%20",
		"redis://sentinel-1:26379?master_name=mymaster&addr=sentinel-2",
		"redis://sentinel-1:26379?master_name=mymaster&unknown=1",
		// go-redis would dial the Redis port on the Sentinel host.
		"redis://sentinel-1?master_name=mymaster",
		"redis://sentinel-1:?master_name=mymaster",
		"redis://:26379?master_name=mymaster",
		// Further Sentinels belong in addr parameters, and an IPv6 address
		// needs brackets.
		"redis://sentinel-1:26379,sentinel-2:26379?master_name=mymaster",
		"redis://2001:db8::1:26379?master_name=mymaster",
		// go-redis panics when a single client is built with a routing option,
		// and replica_only sends writes to a replica.
		"redis://sentinel-1:26379?master_name=mymaster&route_by_latency=true",
		"redis://sentinel-1:26379?master_name=mymaster&route_randomly=true",
		"redis://sentinel-1:26379?master_name=mymaster&replica_only=true",
		"redis://sentinel-1:26379?master_name=mymaster&use_disconnected_replicas=true",
		// go-redis drops these pairs without an error.
		"redis://sentinel-1:26379?master_name=mymaster;addr=sentinel-2:26379",
		"redis://sentinel-1:26379?password=p;ss&master_name=mymaster",
		"redis://sentinel-1:26379?password=p;ss&master%5Fname=mymaster",
		// The same with the dropped pair holding a percent-encoded master_name.
		"redis://sentinel-1:26379?master%5Fname=mymaster;addr=sentinel-2:26379",
		"redis://sentinel-1:26379?addr=sentinel-2:26379;master%5Fname=mymaster",
		"redis://sentinel-1:26379?master%5Fname=my%zzmaster",
		"redis://sentinel-1:26379?%6Daster_name=mymaster;x",
		// A # ends the URL, so everything after it would be dropped.
		"redis://sentinel-1:26379?master_name=mymaster&password=p#ss&addr=sentinel-2:26379",
		"redis://sentinel-1:26379?password=p#ss&master_name=mymaster",
		"redis://sentinel-1:26379?dial_timeout=5s#&master_name=mymaster",
		// go-redis reads the first two as no timeout, and fails every dial at
		// once with the last two.
		"redis://sentinel-1:26379?master_name=mymaster&read_timeout=0",
		"redis://sentinel-1:26379?master_name=mymaster&read_timeout=-1s",
		"redis://sentinel-1:26379?master_name=mymaster&dial_timeout=0",
		"redis://sentinel-1:26379?master_name=mymaster&dial_timeout=-1s",
		// A + is a space in a query string.
		"redis://sentinel-1:26379?master_name=mymaster&password=p+ss",
		"redis://localhost:6379?unknown=1",
		"http://localhost:6379",
		// go-redis panics building a client with a negative pool size.
		"redis://localhost:6379?pool_size=-1",
		"redis://sentinel-1:26379?master_name=mymaster&pool_size=-1",
	} {
		if options, failover, err := ParseRedisURL(raw); err == nil {
			t.Errorf("ParseRedisURL(%q) = (%v, %v), want an error", raw, options, failover)
		}
	}
}

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

func TestParseRedisURLErrorsStillSayWhatIsWrong(t *testing.T) {
	const hint = "percent-encode"
	for _, tc := range []struct {
		raw, want string
		hint      bool
	}{
		{"redis://redis.example:6379?pool_size=many", "invalid pool_size number", false},
		{"redis://redis.example:6379?pool_size=-1", "pool_size must be 0 or more", false},
		{"redis://sentinel-1:26379?master_name=mymaster&pool_size=-5", "pool_size must be 0 or more", false},
		{"http://redis.example:6379", "invalid URL scheme", false},
		{"redis://sentinel-1:26379?master_name=mymaster&addr=nowhere", "unable to parse addr param", false},
		{"redis://sentinel-1?master_name=mymaster", "needs a host and a port", false},
		{"redis://sentinel-1:26379?master_name=mymaster&password=p+ss", "write %2B", false},
		{"redis://sentinel-1:26379?master_name=mymaster&read_timeout=0", "must be more than 0", false},
		// What an unencoded reserved character in a password turns into.
		{"redis://redis.example:6379?unknown=1", "unexpected option", true},
		{"redis://redis.example:6379/first", "invalid database number", true},
		{"redis://redis.example:6379/1/2", "invalid URL path", true},
		{"redis://silo:pass/word@redis.example:6379", "the URL is malformed", true},
		{"redis://sentinel-1:26379?master_name=mymaster&password=p;ss", "cannot be read", true},
	} {
		_, _, err := ParseRedisURL(tc.raw)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseRedisURL(%q) error = %v, want it to say %q", tc.raw, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), hint) != tc.hint {
			t.Errorf("ParseRedisURL(%q) error = %v, want the percent-encoding hint: %v", tc.raw, err, tc.hint)
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

func TestNormalizeRedisURLAcceptsSentinel(t *testing.T) {
	raw := "redis://sentinel-1:26379/1?master_name=mymaster&addr=sentinel-2:26379"
	got, err := NormalizeRedisURL("  " + raw + "\n")
	if err != nil || got != raw {
		t.Fatalf("NormalizeRedisURL = (%q, %v), want the trimmed URL", got, err)
	}
	if _, err := NormalizeRedisURL("redis://sentinel-1:26379?master_name="); err == nil {
		t.Fatal("NormalizeRedisURL accepted a Sentinel URL without a master name")
	}
}

// Every pool size ParseRedisURL accepts builds a client: go-redis panics on a
// negative one, in the connection check and at every start. 0 still means
// the default.
func TestParseRedisURLPoolSizeBuildsAClient(t *testing.T) {
	for _, raw := range []string{
		"redis://localhost:6379?pool_size=0",
		"redis://localhost:6379?pool_size=20",
		"redis://localhost:6379?pool_size=-1",
		"redis://sentinel-1:26379?master_name=mymaster&pool_size=0",
		"redis://sentinel-1:26379?master_name=mymaster&pool_size=-1",
	} {
		options, failover, err := ParseRedisURL(raw)
		if err != nil {
			continue
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("ParseRedisURL(%q) accepted options that panic go-redis: %v", raw, rec)
				}
			}()
			var client *redisv9.Client
			if failover != nil {
				client = redisv9.NewFailoverClient(failover)
			} else {
				client = redisv9.NewClient(options)
			}
			_ = client.Close()
		}()
	}
	if _, err := NormalizeRedisURL("redis://localhost:6379?pool_size=-1"); err == nil {
		t.Error("NormalizeRedisURL saved a negative pool_size")
	}
}

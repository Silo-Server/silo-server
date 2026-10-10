package config

import (
	"strings"
	"testing"
)

func TestNormalizeRedisDB(t *testing.T) {
	for raw, want := range map[string]string{
		"":     "",
		"  ":   "",
		"0":    "0",
		" 3 ":  "3",
		"03":   "3",
		"15\n": "15",
	} {
		got, err := NormalizeAdminSetting(RedisDBSettingKey, raw)
		if err != nil || got != want {
			t.Errorf("NormalizeAdminSetting(redis.db, %q) = (%q, %v), want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-1", "1.5", "three", "3 4", "0x3", "99999999999999999999"} {
		got, err := NormalizeAdminSetting(RedisDBSettingKey, raw)
		if err == nil {
			t.Errorf("NormalizeAdminSetting(redis.db, %q) = %q, want an error", raw, got)
			continue
		}
		if !strings.Contains(err.Error(), RedisDBSettingKey) {
			t.Errorf("the error for %q does not name the setting: %v", raw, err)
		}
	}
}

func TestRedisConfigOptionsReplacesTheDatabaseNumber(t *testing.T) {
	const sentinelURL = "redis://sentinel-1:26379/5?master_name=mymaster"
	for _, tc := range []struct {
		name string
		cfg  RedisConfig
		want int
	}{
		{"an empty redis.db keeps the number in the URL", RedisConfig{URL: "redis://redis.example:6379/3"}, 3},
		{"an empty redis.db keeps 0 for a URL without a number", RedisConfig{URL: "redis://redis.example:6379"}, 0},
		{"redis.db replaces the number in the path", RedisConfig{URL: "redis://redis.example:6379/3", DB: "5"}, 5},
		{"redis.db 0 replaces the number in the path", RedisConfig{URL: "redis://redis.example:6379/3", DB: "0"}, 0},
		{"redis.db applies to a URL without a number", RedisConfig{URL: "redis://redis.example:6379", DB: "5"}, 5},
		{"redis.db replaces a db parameter", RedisConfig{URL: "redis://redis.example:6379?db=4", DB: "5"}, 5},
		{"redis.db applies to a socket URL", RedisConfig{URL: "unix:///run/redis.sock?db=4", DB: "5"}, 5},
		{"an empty redis.db keeps the number in a Sentinel URL", RedisConfig{URL: sentinelURL}, 5},
		{"redis.db replaces the number in a Sentinel URL", RedisConfig{URL: sentinelURL, DB: "2"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, failover, err := tc.cfg.Options()
			if err != nil {
				t.Fatal(err)
			}
			sentinel := strings.Contains(tc.cfg.URL, redisSentinelMasterParam)
			if (failover != nil) != sentinel || (options != nil) == sentinel {
				t.Fatalf("Options() = (%v, %v), want exactly the set of options the URL names", options, failover)
			}
			var got int
			if failover != nil {
				got = failover.DB
			} else {
				got = options.DB
			}
			if got != tc.want {
				t.Errorf("database number = %d, want %d", got, tc.want)
			}
			if db, ok := tc.cfg.Database(); !ok || db != tc.want {
				t.Errorf("Database() = (%d, %v), want %d", db, ok, tc.want)
			}
		})
	}
}

func TestRedisConfigOptionsRejectsWhatItCannotParse(t *testing.T) {
	// The error says which of the two values cannot be read.
	for _, tc := range []struct {
		cfg    RedisConfig
		blames string
	}{
		{RedisConfig{URL: "redis://redis.example:6379/3", DB: "-1"}, RedisDBSettingKey},
		{RedisConfig{URL: "redis://redis.example:6379/3", DB: "three"}, RedisDBSettingKey},
		{RedisConfig{URL: "not-a-url", DB: "3"}, "redis URL"},
	} {
		options, failover, err := tc.cfg.Options()
		if err == nil {
			t.Errorf("Options() for %+v = (%v, %v), want an error", tc.cfg, options, failover)
			continue
		}
		for _, name := range []string{RedisDBSettingKey, "redis URL"} {
			if got := strings.Contains(err.Error(), name); got != (name == tc.blames) {
				t.Errorf("Options() error for %+v = %q, want it to name %s only", tc.cfg, err, tc.blames)
			}
		}
		if db, ok := tc.cfg.Database(); ok {
			t.Errorf("Database() for %+v = %d, want no number", tc.cfg, db)
		}
	}
	// Without a URL there is no Redis, whatever redis.db says.
	if db, ok := (RedisConfig{DB: "3"}).Database(); ok {
		t.Errorf("Database() without a URL = %d, want no number", db)
	}
	// go-redis leaves a connection on database 0 for a negative number.
	for _, rawURL := range []string{"redis://redis.example:6379/-1", "redis://sentinel-1:26379/-1?master_name=mymaster"} {
		if db, ok := (RedisConfig{URL: rawURL}).Database(); !ok || db != 0 {
			t.Errorf("Database() for %q = (%d, %v), want the 0 its connections are on", rawURL, db, ok)
		}
	}
}

func TestRedisConfigWithBootstrapURLDropsTheSavedDatabaseNumber(t *testing.T) {
	saved := RedisConfig{URL: "redis://saved.example:6379/3", DB: "5"}

	if got := saved.WithBootstrapURL(""); got != saved {
		t.Errorf("without REDIS_URL the config = %+v, want the saved %+v", got, saved)
	}

	got := saved.WithBootstrapURL("redis://env.example:6379/1")
	if want := (RedisConfig{URL: "redis://env.example:6379/1"}); got != want {
		t.Errorf("with REDIS_URL the config = %+v, want %+v", got, want)
	}
	if db, ok := got.Database(); !ok || db != 1 {
		t.Errorf("with REDIS_URL the database number = (%d, %v), want the 1 in REDIS_URL", db, ok)
	}
}

func TestLoadFromDBReadsRedisDB(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{"redis.url": "redis://redis.example:6379/3", RedisDBSettingKey: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (RedisConfig{URL: "redis://redis.example:6379/3", DB: "5"}); cfg.Redis != want {
		t.Errorf("Redis = %+v, want %+v", cfg.Redis, want)
	}

	cfg, err = LoadFromDB(map[string]string{"redis.url": "redis://redis.example:6379/3"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redis.DB != "" {
		t.Errorf("Redis.DB = %q for an install that never set redis.db, want it empty", cfg.Redis.DB)
	}
}

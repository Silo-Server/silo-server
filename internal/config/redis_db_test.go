package config

import (
	"testing"
)

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

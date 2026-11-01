package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

type fakeRedisFlusher struct {
	flushErr        error
	flushes, closes int
}

func (f *fakeRedisFlusher) FlushDB(context.Context) error { f.flushes++; return f.flushErr }
func (f *fakeRedisFlusher) Close() error                  { f.closes++; return nil }

// TestClearRedisAfterRestore: a replace clears the configured Redis database
// and nothing else, skips a deployment without Redis or with --keep-redis,
// and never fails the finished restore over Redis: it says what to do.
func TestClearRedisAfterRestore(t *testing.T) {
	tests := []struct {
		name      string
		plan      redisPlan
		dialErr   error
		flushErr  error
		wantDial  bool
		wantFlush int
		wantOut   string
	}{
		{name: "configured database is cleared", plan: redisPlan{config: config.RedisConfig{URL: "redis://cache:6379/3"}},
			wantDial: true, wantFlush: 1, wantOut: "Cleared Redis database 3"},
		{name: "redis.db picks the database", plan: redisPlan{config: config.RedisConfig{URL: "redis://cache:6379/3", DB: "5"}},
			wantDial: true, wantFlush: 1, wantOut: "Cleared Redis database 5"},
		{name: "no Redis configured", plan: redisPlan{}},
		{name: "kept", plan: redisPlan{keep: true, config: config.RedisConfig{URL: "redis://cache:6379/3"}}, wantOut: "--keep-redis"},
		{name: "settings unreadable", plan: redisPlan{readErr: errors.New("no settings")}, wantOut: "Warning: could not read the Redis settings"},
		{name: "unreachable", plan: redisPlan{config: config.RedisConfig{URL: "redis://cache:6379/2"}}, dialErr: errors.New("refused"),
			wantDial: true, wantOut: "Clear it before starting Silo: redis-cli -n 2 FLUSHDB"},
		{name: "flush fails", plan: redisPlan{config: config.RedisConfig{URL: "redis://cache:6379"}}, flushErr: errors.New("READONLY"),
			wantDial: true, wantFlush: 1, wantOut: "redis-cli -n 0 FLUSHDB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeRedisFlusher{flushErr: tt.flushErr}
			dialed := false
			dial := func(rc config.RedisConfig) (redisFlusher, error) {
				dialed = true
				if rc != tt.plan.config {
					t.Fatalf("dialed %+v, want the configured %+v", rc, tt.plan.config)
				}
				if tt.dialErr != nil {
					return nil, tt.dialErr
				}
				return client, nil
			}
			var out strings.Builder
			clearRedisAfterRestore(t.Context(), tt.plan, dial, &out)
			if dialed != tt.wantDial || client.flushes != tt.wantFlush || client.closes != client.flushes {
				t.Fatalf("dialed %v, flushes %d, closes %d; want dialed %v, flushes %d", dialed, client.flushes, client.closes, tt.wantDial, tt.wantFlush)
			}
			if tt.wantOut == "" && out.Len() != 0 || !strings.Contains(out.String(), tt.wantOut) {
				t.Fatalf("output %q, want %q", out.String(), tt.wantOut)
			}
		})
	}
}

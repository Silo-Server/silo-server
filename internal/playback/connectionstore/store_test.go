package connectionstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

func TestConnectionIdentity(t *testing.T) {
	key := func(ip, secret string) string {
		k, _ := Key(clientip.SetContext(context.Background(), ip), secret)
		return k
	}
	if key("192.168.1.1", "secret") != "local" || key("::1", "secret") != "local" {
		t.Fatal("private address must be local")
	}
	a := key("2001:db8:1234:abcd::1", "secret")
	if a != key("2001:db8:1234:abcd::2", "secret") {
		t.Fatal("IPv6 rotations must share the /64 key")
	}
	if a == key("2001:db8:1234:abce::1", "secret") || a == key("2001:db8:1234:abcd::1", "other") {
		t.Fatal("prefix and secret must isolate keys")
	}
	if strings.Contains(a, "2001") || !strings.HasPrefix(a, "remote:") || len(a) != 71 {
		t.Fatalf("unexpected opaque key %q", a)
	}
	if key("invalid", "secret") != "" || key("8.8.8.8", "") != "" {
		t.Fatal("unknown identity must not share a key")
	}
}

func TestStoreLifecycle(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("connection-test-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) }()
	s := &Store{Pool: pool}
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	limit, err := s.Get(ctx, user, "device", "local")
	if err != nil || limit == nil || limit.Kbps != 44100 || limit.ExpiresAt.Sub(limit.ObservedAt) != TTL {
		t.Fatalf("read: %+v %v", limit, err)
	}
	other, err := s.Get(ctx, user, "other", "local")
	if err != nil || other != nil {
		t.Fatal("device isolation failed")
	}
	if err := s.Observe(ctx, user, "device", "local", 22120); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET observed_at=now()-interval '8 days' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	limit, err = s.Get(ctx, user, "device", "local")
	if err != nil || limit != nil {
		t.Fatal("expired limit applied")
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET updated_at=now()-interval '31 days' WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playback_connections WHERE user_id=$1`, user).Scan(&count); err != nil || count != 0 {
		t.Fatal("retention failed", err)
	}
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(ctx, user, "device", "local"); err != nil {
		t.Fatal(err)
	}
	limit, err = s.Get(ctx, user, "device", "local")
	if err != nil || limit != nil {
		t.Fatal("reset failed")
	}
	if err := s.Observe(ctx, user, "device", "local", 44100); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_devices(user_id,profile_id,device_id) VALUES($1,'profile-1','device')`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM user_devices WHERE user_id=$1`, user); err != nil {
		t.Fatal(err)
	}
	limit, err = s.Get(ctx, user, "device", "local")
	if err != nil || limit != nil {
		t.Fatal("device deletion retained limit")
	}
}

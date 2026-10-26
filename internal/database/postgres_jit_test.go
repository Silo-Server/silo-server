package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Pool connections run with JIT off unless something configured jit: here the
// URL, its options, or the database. The test role needs CREATEDB.
func TestNewPoolDisablesJITUnlessConfiguredPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	base, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	showJIT := func(t *testing.T, u url.URL) string {
		t.Helper()
		pool, err := NewPoolForRole(ctx, config.DatabaseConfig{URL: u.String(), MaxConnections: 1}, "application")
		if err != nil {
			t.Fatal(err)
		}
		defer ClosePool(pool)
		var got string
		if err := pool.QueryRow(ctx, `SHOW jit`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	for _, tt := range []struct{ param, value, want string }{
		{want: "off"},
		{param: "jit", value: "on", want: "on"},
		{param: "options", value: "-c jit=on", want: "on"},
		{param: "options", value: "-ecjit=on", want: "on"},
		{param: "options", value: "-c application_name=jitter", want: "off"},
	} {
		u := *base
		if tt.param != "" {
			q := u.Query()
			q.Set(tt.param, tt.value)
			u.RawQuery = q.Encode()
		}
		if got := showJIT(t, u); got != tt.want {
			t.Errorf("jit = %s, want %s (%s=%q)", got, tt.want, tt.param, tt.value)
		}
	}

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("silo_jit_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+quoted+" SET jit = on"); err != nil {
		t.Fatal(err)
	}
	u := *base
	u.Path = "/" + name
	if got := showJIT(t, u); got != "on" {
		t.Errorf("jit = %s, want on from ALTER DATABASE", got)
	}
}

package notifications

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationDestinationPages(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, item := range []struct{ file, table string }{
		{"20260611150000_web_push_subscriptions.sql", "web_push_subscriptions"},
		{"20260611120000_notification_webhooks.sql", "notification_webhooks"},
		{"20260612020209_server_notification_channels.sql", "notification_server_channels"},
	} {
		data, err := os.ReadFile("../../migrations/sql/" + item.file)
		if err != nil {
			t.Fatal(err)
		}
		_, ddl, ok := strings.Cut(string(data), "CREATE TABLE public."+item.table+" (")
		if !ok {
			t.Fatal("missing table declaration")
		}
		ddl, _, ok = strings.Cut(ddl, "\n);")
		if !ok {
			t.Fatal("missing table terminator")
		}
		if _, err = pool.Exec(ctx, "CREATE TEMP TABLE "+item.table+" ("+ddl+"\n);"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE notification_webhooks ADD COLUMN notify_requests boolean NOT NULL DEFAULT false, ADD COLUMN revision bigint NOT NULL DEFAULT 1;
 ALTER TABLE notification_server_channels ADD COLUMN notify_new_audiobooks boolean NOT NULL DEFAULT true, ADD COLUMN notify_new_ebooks boolean NOT NULL DEFAULT true;
 INSERT INTO web_push_subscriptions (id,user_id,profile_id,endpoint,p256dh,auth,created_at) SELECT id,1,profile,'https://push.example.test/'||id,'key','auth','2026-09-01T00:00:00Z'::timestamptz FROM (VALUES ('01','owner'),('02','owner'),('03','owner'),('00','other')) AS v(id,profile);
 INSERT INTO notification_webhooks (id,user_id,profile_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,created_at) SELECT id,1,profile,id,'generic','ciphertext','example.test','secret','2026-09-01T00:00:00Z'::timestamptz FROM (VALUES ('01','owner'),('02','owner'),('03','owner'),('00','other')) AS v(id,profile);
 INSERT INTO notification_server_channels (id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,created_by_user_id,created_at) SELECT id,id,'generic','ciphertext','example.test','secret',1,'2026-09-01T00:00:00Z'::timestamptz FROM (VALUES ('01'),('02'),('03')) AS v(id);`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906025224_notification_destination_pages.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = pool.Exec(ctx, strings.ReplaceAll(up, "public.", "")); err != nil {
		t.Fatal(err)
	}
	key := &Cursor{CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ID: "02"}
	pushes := NewWebPushRepository(pool)
	hooks := NewWebhookRepository(pool)
	channels := NewServerChannelRepository(pool)
	a, err := pushes.ListPage(ctx, "owner", 2, nil)
	if err != nil || len(a) != 2 || a[0].ID != "01" || a[1].ID != "02" {
		t.Fatalf("push page: %+v %v", a, err)
	}
	b, err := hooks.ListPage(ctx, "owner", 2, nil)
	if err != nil || len(b) != 2 || b[0].ID != "01" || b[1].ID != "02" {
		t.Fatalf("webhook page: %+v %v", b, err)
	}
	c, err := channels.ListPage(ctx, 2, nil)
	if err != nil || len(c) != 2 || c[0].ID != "01" || c[1].ID != "02" {
		t.Fatalf("channel page: %+v %v", c, err)
	}
	// Cursor continuation must not depend on the boundary row still existing.
	if _, err = pool.Exec(ctx, `DELETE FROM web_push_subscriptions WHERE id='02'; DELETE FROM notification_webhooks WHERE id='02'; DELETE FROM notification_server_channels WHERE id='02'`); err != nil {
		t.Fatal(err)
	}
	a, err = pushes.ListPage(ctx, "owner", 2, key)
	if err != nil || len(a) != 1 || a[0].ID != "03" {
		t.Fatalf("push continuation: %+v %v", a, err)
	}
	b, err = hooks.ListPage(ctx, "owner", 2, key)
	if err != nil || len(b) != 1 || b[0].ID != "03" {
		t.Fatalf("webhook continuation: %+v %v", b, err)
	}
	c, err = channels.ListPage(ctx, 2, key)
	if err != nil || len(c) != 1 || c[0].ID != "03" {
		t.Fatalf("channel continuation: %+v %v", c, err)
	}
}

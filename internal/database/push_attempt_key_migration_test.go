package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// goose records the push attempt key migration only after its last statement,
// so a run that stopped there runs again. Each direction must recover an
// interrupted build and, on a retry, keep the key it already built rather than
// leave the table with neither key while rebuilding it.
func TestPushAttemptKeyMigrationRetriesKeepTheBuiltKeyPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("push_attempt_key_%d", time.Now().UnixNano())
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	exec("CREATE TABLE " + schema + `.push_delivery_attempts (id text PRIMARY KEY, notification_delivery_id text,
		push_device_id text NOT NULL, trigger_type text NOT NULL, attempt_number integer NOT NULL)`)
	exec("INSERT INTO " + schema + ".push_delivery_attempts VALUES ('own', 'delivery-1', 'device', 'delivery', 1), ('other', 'delivery-2', 'device', 'delivery', 1)")
	exec("CREATE UNIQUE INDEX " + pushAttemptKey + " ON " + schema + "." + pushAttemptKeyColumns + " WHERE notification_delivery_id IS NOT NULL")
	// An earlier attempt stopped mid-build and left an invalid copy of the
	// new key beside the old one.
	_, err = db.ExecContext(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+pushAttemptDeliveryKey+" ON "+schema+".push_delivery_attempts (push_device_id)")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("expected failed concurrent build, got %v", err)
	}

	type index struct {
		oid        uint32
		valid      bool
		definition string
	}
	lookup := func(name string) index {
		t.Helper()
		var got index
		if err := db.QueryRowContext(ctx, `SELECT coalesce(to_regclass($1)::oid, 0),
			coalesce((SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)), false),
			coalesce(pg_get_indexdef(to_regclass($1)), '')`, schema+"."+name).Scan(&got.oid, &got.valid, &got.definition); err != nil {
			t.Fatal(err)
		}
		return got
	}
	expect := func(step string, kept, gone, predicate string) index {
		t.Helper()
		key := lookup(kept)
		if !key.valid || !strings.HasPrefix(key.definition, "CREATE UNIQUE INDEX") || !strings.Contains(key.definition, predicate) {
			t.Fatalf("%s: %s = %+v, want a valid unique index %s", step, kept, key, predicate)
		}
		if lookup(gone).oid != 0 {
			t.Fatalf("%s: %s still exists", step, gone)
		}
		return key
	}

	if err := scopePushAttemptKey(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	scoped := expect("up", pushAttemptDeliveryKey, pushAttemptKey, "WHERE (trigger_type = 'delivery'::text)")
	if err := scopePushAttemptKey(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if again := expect("up retry", pushAttemptDeliveryKey, pushAttemptKey, "trigger_type"); again.oid != scoped.oid {
		t.Fatal("up retry rebuilt the key the first run built")
	}

	// A test attempt carrying a delivery shares the delivery's own key.
	exec("INSERT INTO " + schema + ".push_delivery_attempts VALUES ('test', 'delivery-1', 'device', 'test', 1)")
	if err := widenPushAttemptKey(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	widened := expect("down", pushAttemptKey, pushAttemptDeliveryKey, "WHERE (notification_delivery_id IS NOT NULL)")
	if err := widenPushAttemptKey(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	if again := expect("down retry", pushAttemptKey, pushAttemptDeliveryKey, "notification_delivery_id IS NOT NULL"); again.oid != widened.oid {
		t.Fatal("down retry rebuilt the key the first run built")
	}
	var detached bool
	if err := db.QueryRowContext(ctx, "SELECT notification_delivery_id IS NULL FROM "+schema+".push_delivery_attempts WHERE id = 'test'").Scan(&detached); err != nil || !detached {
		t.Fatalf("down kept the test attempt on its delivery (err %v)", err)
	}
}

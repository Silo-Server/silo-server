package handlers

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/demomode"
)

// TestDemoAccountSettingRefusedDB: the batch and the single-key settings
// writes refuse a demo.account_id that names no account or an
// administrator's, at the request member that carries it, and store
// nothing; an ordinary account is accepted. Catches a write route that
// skips the check, which would let the setting name an administrator.
func TestDemoAccountSettingRefusedDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	account := func(role string) string {
		t.Helper()
		var id int
		if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled)
			VALUES ($1, $2, 'x', $3, true) RETURNING id`, "demo-"+role+"-"+suffix, "demo-"+role+"-"+suffix+"@example.invalid", role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return strconv.Itoa(id)
	}
	admin, visitor := account("admin"), account("user")

	key := demomode.AccountSettingKey
	for _, single := range []bool{false, true} {
		field := "values." + key
		if single {
			field = "value"
		}
		store := &fakeServerSettingsStore{values: map[string]string{}}
		h := &AdminHandler{SettingsRepo: store, pool: pool}
		for _, value := range []string{admin, "2147483647"} {
			err := saveAdminSetting(t, h, key, value, single)
			requireAdminError(t, err, 400, adminInvalidSettingsCode)
			if apiErr, _ := errors.AsType[*APIError](err); apiErr.Field != field {
				t.Fatalf("single=%v %q: field %q, want %q", single, value, apiErr.Field, field)
			}
			if _, stored := store.values[key]; stored {
				t.Fatalf("single=%v: refused write stored %q", single, store.values[key])
			}
		}
		if err := saveAdminSetting(t, h, key, visitor, single); err != nil || store.values[key] != visitor {
			t.Fatalf("single=%v: ordinary account: %v, stored %v", single, err, store.values)
		}
	}
}

package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Turning local password sign-in off needs a break-glass admin, through the
// batch and the single-key settings writes alike.
func TestLocalLoginSettingRequiresBreakGlassDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var existing int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM users WHERE break_glass`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing > 0 {
		t.Skipf("%d break-glass accounts already in the database", existing)
	}

	key := config.AuthLocalPasswordLoginSettingKey
	store := &fakeServerSettingsStore{values: map[string]string{}}
	h := &AdminHandler{SettingsRepo: store, pool: pool}
	for _, single := range []bool{false, true} {
		requireAdminError(t, saveAdminSetting(t, h, key, "false", single), 409, "break_glass_required")
	}
	if _, stored := store.values[key]; stored {
		t.Fatalf("refused write stored %q", store.values[key])
	}
	// Without account storage the change cannot be checked, so it is refused.
	if err := saveAdminSetting(t, &AdminHandler{SettingsRepo: store}, key, "false", true); err == nil {
		t.Fatal("unchecked write accepted")
	}

	suffix := fmt.Sprint(time.Now().UnixNano())
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled, break_glass)
		VALUES ($1, $2, 'x', 'admin', true, true) RETURNING id`, "glass-"+suffix, "glass-"+suffix+"@example.invalid").Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	if err := saveAdminSetting(t, h, key, "false", true); err != nil || store.values[key] != "false" {
		t.Fatalf("with break-glass: %v, stored %v", err, store.values)
	}
}

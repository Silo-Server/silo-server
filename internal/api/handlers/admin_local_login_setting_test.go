package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
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
	put := func(h *AdminHandler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"values":{"`+key+`":"false"}}`)))
		return rec
	}
	putOne := func(h *AdminHandler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(`{"value":"false"}`))
		h.HandleUpdateSetting(rec, withURLParam(req, "key", key))
		return rec
	}

	store := &fakeServerSettingsStore{values: map[string]string{}}
	h := &AdminHandler{SettingsRepo: store, pool: pool}
	for name, rec := range map[string]*httptest.ResponseRecorder{"batch": put(h), "single": putOne(h)} {
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "break_glass_required") {
			t.Fatalf("%s without break-glass: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if _, stored := store.values[key]; stored {
		t.Fatalf("refused write stored %q", store.values[key])
	}
	// Without account storage the change cannot be checked, so it is refused.
	if rec := putOne(&AdminHandler{SettingsRepo: store}); rec.Code == http.StatusOK {
		t.Fatalf("unchecked write accepted: %s", rec.Body.String())
	}

	suffix := fmt.Sprint(time.Now().UnixNano())
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled, break_glass)
		VALUES ($1, $2, 'x', 'admin', true, true) RETURNING id`, "glass-"+suffix, "glass-"+suffix+"@example.invalid").Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	if rec := putOne(h); rec.Code != http.StatusOK || store.values[key] != "false" {
		t.Fatalf("with break-glass: %d %s %v", rec.Code, rec.Body.String(), store.values)
	}
}

// The frozen v1 admin user routes keep the break-glass rule v2 enforces: with
// local password sign-in off, the last usable break-glass admin cannot be
// demoted, disabled or deleted. An administrator's password write turns the
// account's local password sign-in back on.
func TestV1AdminUserChangesKeepBreakGlassDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var existing int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE break_glass`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing > 0 {
		t.Skipf("%d break-glass accounts already in the database", existing)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	insert := func(label, role string, owner, glass, localLogin bool) int {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled, is_owner, break_glass, local_password_login_enabled)
			VALUES ($1, $2, 'x', $3, true, $4, $5, $6) RETURNING id`, label+"-"+suffix, label+"-"+suffix+"@example.invalid", role, owner, glass, localLogin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	var ownerID int
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&ownerID); err != nil {
		ownerID = insert("owner", "admin", true, false, true)
	}
	glassID := insert("glass", "admin", false, true, true)
	linkedID := insert("linked", "user", false, false, false)

	key := config.AuthLocalPasswordLoginSettingKey
	var saved *string
	var value string
	if err := pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value); err == nil {
		saved = &value
	}
	t.Cleanup(func() {
		if saved == nil {
			_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, key)
			return
		}
		_, _ = pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, key, *saved)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key); err != nil {
		t.Fatal(err)
	}

	h := &AdminHandler{userRepo: auth.NewUserRepository(pool)}
	send := func(method string, id int, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/admin/users/"+fmt.Sprint(id), strings.NewReader(body))
		req = withURLParam(req, "id", fmt.Sprint(id))
		req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: ownerID, Role: "admin", TokenType: auth.TokenTypeAccess, SessionID: "s1"}))
		rec := httptest.NewRecorder()
		if method == http.MethodDelete {
			h.HandleDeleteUser(rec, req)
		} else {
			h.HandleUpdateUser(rec, req)
		}
		return rec
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"demote":  send(http.MethodPut, glassID, `{"role":"user"}`),
		"disable": send(http.MethodPut, glassID, `{"enabled":false}`),
		"delete":  send(http.MethodDelete, glassID, ""),
	} {
		if rec.Code != http.StatusConflict || decodeErrorCode(t, rec) != "break_glass_required" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	var role string
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT role, enabled FROM users WHERE id = $1`, glassID).Scan(&role, &enabled); err != nil || role != "admin" || !enabled {
		t.Fatalf("break-glass admin changed: %s %v %v", role, enabled, err)
	}

	if rec := send(http.MethodPut, linkedID, `{"password":"a-new-password-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("password write: %d %s", rec.Code, rec.Body.String())
	}
	var localLogin bool
	if err := pool.QueryRow(ctx, `SELECT local_password_login_enabled FROM users WHERE id = $1`, linkedID).Scan(&localLogin); err != nil || !localLogin {
		t.Fatalf("local password sign-in after a password write = %v, %v", localLogin, err)
	}
}

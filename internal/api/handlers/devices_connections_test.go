package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// connectionsTestUser creates an account and returns a counter of its
// playback connection rows for one device.
func connectionsTestUser(t *testing.T) (*pgxpool.Pool, int, *connectionstore.Store, func(device string) int) {
	t.Helper()
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
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("device-connections-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })
	rows := func(device string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM playback_connections WHERE user_id = $1 AND device_id = $2`, user, device).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return pool, user, &connectionstore.Store{Pool: pool}, rows
}

func forgetDeviceAs(t *testing.T, h *DeviceHandler, user int, profileID, deviceID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/devices/"+deviceID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("device_id", deviceID)
	ctx := apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: user}), profileID)
	rec := httptest.NewRecorder()
	h.HandleForgetDevice(rec, req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("forget %s on %s = %d: %s", deviceID, profileID, rec.Code, rec.Body.String())
	}
}

// TestForgetDeviceKeepsConnectionsUntilLastRegistrationDB: a device's playback
// connection rows outlive the forget of one of its registrations and go with
// the last, through the v1 and v2 forgets.
func TestForgetDeviceKeepsConnectionsUntilLastRegistrationDB(t *testing.T) {
	pool, user, connections, rows := connectionsTestUser(t)
	ctx := t.Context()
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"p1", "p2"} {
		seedDevice(t, store, profile, "tv", "TV")
	}
	if err := connections.Observe(ctx, user, "tv", "local", 44100); err != nil {
		t.Fatal(err)
	}
	h := NewDeviceHandler(testUserStoreProvider{store: store})
	h.Connections = connections

	forgetDeviceAs(t, h, user, "p1", "tv")
	if rows("tv") != 1 {
		t.Fatal("forgetting one of two registrations deleted the connection rows")
	}
	if err := h.RemoveDeviceSettings(ctx, DeviceSettingsActor{UserID: user, ProfileID: "p2"}, "p2", "tv", true); err != nil {
		t.Fatal(err)
	}
	if rows("tv") != 0 {
		t.Fatal("forgetting the last registration kept the connection rows")
	}
}

// TestConcurrentLastRegistrationDeletesDropConnectionsDB: two transactions
// each delete one of a device's two registrations. Under READ COMMITTED
// neither sees the other's delete before it commits, so a check inside either
// transaction finds the other registration and keeps the rows. The cleanup each
// deletion path runs after its commit must still drop them.
func TestConcurrentLastRegistrationDeletesDropConnectionsDB(t *testing.T) {
	pool, user, connections, rows := connectionsTestUser(t)
	ctx := t.Context()
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	registry := store.(userstore.DeviceRegistry)
	for _, profile := range []string{"p1", "p2"} {
		seedDevice(t, store, profile, "tv", "TV")
	}
	if err := connections.Observe(ctx, user, "tv", "local", 44100); err != nil {
		t.Fatal(err)
	}
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(ctx) }()
	const forget = `DELETE FROM user_devices WHERE user_id = $1 AND profile_id = $2 AND device_id = 'tv'`
	if _, err := first.Exec(ctx, forget, user, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Exec(ctx, forget, user, "p2"); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	forgetPlaybackConnections(ctx, connections, registry, user, []string{"tv"})
	if rows("tv") != 1 {
		t.Fatal("the first deletion's cleanup dropped the rows while the second registration was still committed")
	}
	if err := second.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	forgetPlaybackConnections(ctx, connections, registry, user, []string{"tv"})
	if rows("tv") != 0 {
		t.Fatal("no registration is left, but the connection rows remain")
	}
}

// TestSQLiteRegistrationRemovalDropsConnectionsDB: with the SQLite user store
// the registry lives outside Postgres, and Postgres user_devices holds only the
// download mirror. Deleting a profile keeps the rows of a device another
// profile still registers in SQLite and drops those of a device it alone had;
// forgetting the last registration drops the rest.
func TestSQLiteRegistrationRemovalDropsConnectionsDB(t *testing.T) {
	pool, user, connections, rows := connectionsTestUser(t)
	ctx := t.Context()
	db, err := sql.Open("sqlite3", "file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	for _, p := range []userstore.Profile{{ID: "p1", Name: "Sam", IsPrimary: true}, {ID: "p2", Name: "Robin"}} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	seedDevice(t, store, "p1", "tv", "TV")
	seedDevice(t, store, "p2", "tv", "TV")
	seedDevice(t, store, "p2", "phone", "Phone")
	repo := downloads.NewRepository(pool)
	for _, device := range []string{"tv", "phone"} {
		if err := repo.EnsureDevice(ctx, user, "p2", device, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := connections.Observe(ctx, user, device, "local", 44100); err != nil {
			t.Fatal(err)
		}
	}
	provider := testUserStoreProvider{store: store}
	profiles := NewProfileHandler(provider)
	profiles.DeviceLibraryPurger = repo
	profiles.Connections = connections
	if err := profiles.DeleteProfile(ctx, ProfileDeleteCommand{UserID: user, ProfileID: "p2", ActiveProfileID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if rows("tv") != 1 {
		t.Fatal("deleting a profile dropped the rows of a device another profile registers")
	}
	if rows("phone") != 0 {
		t.Fatal("deleting a profile kept the rows of a device only it registered")
	}
	devices := NewDeviceHandler(provider)
	devices.Connections = connections
	forgetDeviceAs(t, devices, user, "p1", "tv")
	if rows("tv") != 0 {
		t.Fatal("forgetting the last SQLite registration kept the connection rows")
	}
}

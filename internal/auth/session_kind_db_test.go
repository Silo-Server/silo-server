package auth

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestActiveSessionKindDB: the per-request session lookup says whether
// another app's key opened the session, and nothing else carries that kind.
// A session opened with a key enrolled through a grant carries
// SessionKindGrantedApp; a session the person's own device key opened, a
// password session and a device signed in by code carry none; a session
// naming a device key whose row is missing carries it, failing closed.
// Catches the join or its predicate drifting from ReadSessionOrigin, which
// would let another app's session through every refused operation, or
// refuse the person's own devices.
func TestActiveSessionKindDB(t *testing.T) {
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
	name := "sessionkind-" + uuid.NewString()[:8]
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions)
		VALUES ($1, $2, 'x', 'user', '{}') RETURNING id`, name, name+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var sessions []string
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_sessions WHERE id = ANY($1)`, sessions)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
	})
	key := func(enrolledVia string) string {
		t.Helper()
		publicKey := make([]byte, 65)
		if _, err := rand.Read(publicKey); err != nil {
			t.Fatal(err)
		}
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
			VALUES ($1::uuid, $2, $3, $4, 'app', 'app', $5)`, id, userID, publicKey, "key-"+id, enrolledVia); err != nil {
			t.Fatal(err)
		}
		return id
	}
	session := func(openedVia string, deviceKey *string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', $3, $4, $5::uuid)`, id, userID, time.Now().Add(time.Hour), openedVia, deviceKey); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, id)
		return id
	}
	grant, own := key(models.AppKeyEnrolledGrant), key(models.AppKeyEnrolledSession)
	// The reference to a key cascades, so a session naming a missing row
	// can only be written with the foreign key checks off.
	missing := uuid.NewString()
	dangling := uuid.NewString()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `SET session_replication_role = replica`)
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', $3, 'app_key', $4::uuid)`, dangling, userID, time.Now().Add(time.Hour), missing)
		sessions = append(sessions, dangling)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	}
	conn.Release()
	if err != nil {
		t.Fatalf("writing a session that names a missing key: %v", err)
	}

	repo := NewSessionRepository(pool)
	for _, tc := range []struct {
		name    string
		session string
		want    SessionKind
	}{
		{"another app's key", session(models.SessionOpenedAppKey, &grant), SessionKindGrantedApp},
		{"the person's own device key", session(models.SessionOpenedAppKey, &own), ""},
		{"password", session(models.SessionOpenedPassword, nil), ""},
		{"device signed in by code", session(models.SessionOpenedDeviceLogin, nil), ""},
		{"key row missing", dangling, SessionKindGrantedApp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority, active, err := repo.ActiveSession(ctx, tc.session, userID)
			if err != nil || !active {
				t.Fatalf("active = %v, err = %v", active, err)
			}
			if authority.Kind != tc.want {
				t.Fatalf("kind = %q, want %q", authority.Kind, tc.want)
			}
		})
	}
}

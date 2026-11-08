package watchtogether

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// visitorDB is a migrated test database with one host account.
type visitorDB struct {
	t        *testing.T
	pool     *pgxpool.Pool
	prefix   string
	hostID   int
	rooms    *Repository
	store    *VisitorStore
	sessions *auth.SessionRepository
}

func newVisitorDB(t *testing.T) *visitorDB {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var migrated bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('public.watch_together_visitors') IS NOT NULL`).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Skip("test database has not applied the watch party visitor migration")
	}
	db := &visitorDB{t: t, pool: pool, prefix: fmt.Sprintf("visitors-%d", time.Now().UnixNano()),
		rooms: NewRepository(pool), store: NewVisitorStore(pool), sessions: auth.NewSessionRepository(pool)}
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, role) VALUES ($1, 'admin') RETURNING id`, db.prefix).Scan(&db.hostID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, db.hostID) })
	return db
}

func (db *visitorDB) room(id string) Room {
	db.t.Helper()
	now := time.Now().UTC()
	room, err := db.rooms.CreateRoom(db.t.Context(), Room{ID: db.prefix + id, Code: db.prefix + id, JoinToken: db.prefix + id,
		HostUserID: db.hostID, HostProfileID: "host", Phase: RoomPhaseLobby, PlaybackState: RoomPlaybackStateIdle,
		SelectionMode: RoomSelectionModeHostPick, GuestControlPolicy: GuestControlPolicyHostOnly, IsPaused: true,
		AnchorUpdatedAt: now, Generation: 1, CreatedAt: now})
	if err != nil {
		db.t.Fatal(err)
	}
	return *room
}

// createSession inserts the visitor's session as auth.Service does.
func (db *visitorDB) createSession(ctx context.Context, tx pgx.Tx, v AcceptedVisitor) error {
	_, err := tx.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, purpose, bound_profile_id, absolute_expires_at, bound_room_id, bound_visitor_id)
		VALUES ($1, $2, 'test', $3, $4, $5, $3, $6, $7)`, v.SessionID, v.HostUserID, v.SessionEndsAt, models.SessionPurposeWatchPartyVisitor, v.HostProfileID, v.RoomID, v.VisitorID)
	return err
}

func (db *visitorDB) accept(token, name string) (AcceptedVisitor, error) {
	return db.store.AcceptInvite(db.t.Context(), token, name, db.createSession)
}

func (db *visitorDB) live(sessionID string) (auth.SessionAuthority, bool) {
	db.t.Helper()
	authority, active, err := db.sessions.ActiveSession(db.t.Context(), sessionID, db.hostID)
	if err != nil {
		db.t.Fatal(err)
	}
	return authority, active
}

// TestWatchPartyVisitorLifecycleDB pins what makes a visitor's session live:
// an invite that is usable when it is accepted, the visitor row and session
// committing together, the room staying open, and the host not removing the
// visitor. Revoking the link stops new visitors only.
func TestWatchPartyVisitorLifecycleDB(t *testing.T) {
	db := newVisitorDB(t)
	ctx := t.Context()
	room := db.room("-a")
	invite, token, err := db.store.CreateInvite(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	if preview, err := db.store.PreviewInvite(ctx, token); err != nil || preview.RoomID != room.ID || preview.HostProfileID != "host" {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := db.store.PreviewInvite(ctx, token+"x"); !errors.Is(err, ErrVisitorInviteNotFound) {
		t.Fatalf("preview of an unknown token: %v", err)
	}
	if _, err := db.accept(token, " \t "); !errors.Is(err, ErrInvalidVisitorName) {
		t.Fatalf("blank name: %v", err)
	}

	first, err := db.accept(token, "  Sam  ")
	if err != nil {
		t.Fatal(err)
	}
	authority, active := db.live(first.SessionID)
	if !active || !authority.IsWatchPartyVisitor() || authority.Role != models.RoleUser || authority.RoomID != room.ID ||
		authority.VisitorID != first.VisitorID || authority.BoundProfileID != "host" || first.DisplayName != "Sam" {
		t.Fatalf("first visitor: %+v active=%v accepted=%+v", authority, active, first)
	}
	if name := db.store.VisitorName(ctx, first.VisitorID); name != "Sam" {
		t.Fatalf("visitor name = %q", name)
	}

	// A session whose creation fails leaves no visitor behind.
	failing := func(context.Context, pgx.Tx, AcceptedVisitor) error { return errors.New("boom") }
	if _, err := db.store.AcceptInvite(ctx, token, "Lee", failing); err == nil {
		t.Fatal("accept with a failing session succeeded")
	}
	var visitors int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM watch_together_visitors WHERE room_id = $1`, room.ID).Scan(&visitors); err != nil || visitors != 1 {
		t.Fatalf("visitors after a failed accept = %d, %v", visitors, err)
	}
	second, err := db.accept(token, "Lee")
	if err != nil {
		t.Fatal(err)
	}

	// Revoking the link stops new visitors, not the ones already in.
	if err := db.store.RevokeInvite(ctx, room.ID, invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.accept(token, "Kim"); !errors.Is(err, ErrVisitorInviteNotFound) {
		t.Fatalf("accept through a revoked link: %v", err)
	}
	if _, active := db.live(first.SessionID); !active {
		t.Fatal("revoking the link ended a visitor's session")
	}
	checkRemovalAndClose(t, db, room, first, second)
}

// checkRemovalAndClose: removing a visitor ends its session only, and closing
// the room ends every visitor's session.
func checkRemovalAndClose(t *testing.T, db *visitorDB, room Room, first, second AcceptedVisitor) {
	t.Helper()
	ctx := t.Context()
	if err := db.store.RemoveVisitor(ctx, room.ID, first.VisitorID); err != nil {
		t.Fatal(err)
	}
	if _, active := db.live(first.SessionID); active {
		t.Fatal("removed visitor's session is still active")
	}
	if _, active := db.live(second.SessionID); !active {
		t.Fatal("removing one visitor ended another's session")
	}
	if err := db.store.RemoveVisitor(ctx, "another-room", second.VisitorID); !errors.Is(err, ErrVisitorNotFound) {
		t.Fatalf("remove through another room: %v", err)
	}
	if _, err := db.rooms.CloseRoom(ctx, room.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, active := db.live(second.SessionID); active {
		t.Fatal("visitor session outlived its room")
	}
	if live, err := db.sessions.VisitorBindingLive(ctx, second.SessionID); err != nil || live {
		t.Fatalf("binding after close = %v, %v", live, err)
	}
	if _, _, err := db.store.CreateInvite(ctx, room); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("invite to a closed room: %v", err)
	}
}

// TestWatchPartyVisitorLimitsDB pins the room's visitor limit and the schema
// refusing a visitor session without its room.
func TestWatchPartyVisitorLimitsDB(t *testing.T) {
	db := newVisitorDB(t)
	_, token, err := db.store.CreateInvite(t.Context(), db.room("-full"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxVisitorsPerRoom {
		if _, err := db.accept(token, fmt.Sprintf("v%d", i)); err != nil {
			t.Fatalf("visitor %d: %v", i, err)
		}
	}
	if _, err := db.accept(token, "one more"); !errors.Is(err, ErrVisitorLimitReached) {
		t.Fatalf("visitor past the limit: %v", err)
	}
	_, err = db.pool.Exec(t.Context(), `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, purpose, bound_profile_id, absolute_expires_at, bound_visitor_id)
		VALUES ($1, $2, 'test', NOW() + INTERVAL '1 hour', 'watch_party_visitor', 'host', NOW() + INTERVAL '1 hour', 'v')`, db.prefix+"-unbound", db.hostID)
	if err == nil {
		t.Fatal("schema accepted a visitor session without a room")
	}
}

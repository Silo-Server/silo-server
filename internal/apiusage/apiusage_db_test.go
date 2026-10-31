package apiusage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// apiUsageFixture is one account with three sessions and their activity,
// in a window of its own years ahead so other tests' rows stay out.
type apiUsageFixture struct {
	pool                  *pgxpool.Pool
	now                   time.Time
	tag                   string
	userID, adminID       int
	busy, quiet, revoked  string
	admin                 string
	lastHour, currentHour time.Time
}

func newAPIUsageFixture(t *testing.T) *apiUsageFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// A half-hour offset would split UTC hours if the summary bucketed in the
	// session's time zone.
	cfg.ConnConfig.RuntimeParams["timezone"] = "Asia/Kolkata"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tag := strconv.FormatInt(time.Now().UnixNano(), 10)
	f := &apiUsageFixture{pool: pool, now: time.Date(2031, 3, 1, 12, 30, 0, 0, time.UTC), tag: tag,
		busy: "busy-" + tag, quiet: "quiet-" + tag, revoked: "revoked-" + tag, admin: "admin-" + tag}
	f.lastHour, f.currentHour = f.now.Add(-time.Hour), f.now.Add(-10*time.Minute)
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role)
		VALUES ($1::text, $1::text || '@example.test', 'x', 'user') RETURNING id`, "apiusage-"+tag).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role)
		VALUES ($1::text, $1::text || '@example.test', 'x', 'admin') RETURNING id`, "apiusage-admin-"+tag).Scan(&f.adminID); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{f.busy, f.quiet, f.revoked} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, revoked_at)
			VALUES ($1, $2, 'Test TV', $3, CASE WHEN $4 THEN now() END)`,
			session, f.userID, f.now.Add(24*time.Hour), session == f.revoked); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM api_client_usage_hourly WHERE session_id LIKE '%' || $1`, tag)
		_, _ = pool.Exec(ctx, `DELETE FROM activity_log WHERE user_id = ANY($1)`, []int{f.userID, f.adminID})
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int{f.userID, f.adminID})
	})
	return f
}

// log writes n activity rows; an empty session writes rows without one. The
// admin session's rows belong to the admin account.
func (f *apiUsageFixture) log(t *testing.T, session string, at time.Time, path, pattern string, status, n int) {
	t.Helper()
	f.insert(t, session, at, path, pattern, status, n, nil)
}

func (f *apiUsageFixture) insert(t *testing.T, session string, at time.Time, path, pattern string, status, n int, impersonator *int) {
	t.Helper()
	var sessionArg any
	if session != "" {
		sessionArg = session
	}
	userID := f.userID
	if session == f.admin {
		userID = f.adminID
	}
	for range n {
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO activity_log
			("timestamp", client_ip, user_id, session_id, method, path, path_pattern, status_code, user_agent, impersonator_user_id)
			VALUES ($1, '192.0.2.1', $2, $3, 'GET', $4, $5, $6, $7, $8)`,
			at, userID, sessionArg, path, pattern, status, "agent-"+f.tag, impersonator); err != nil {
			t.Fatal(err)
		}
	}
}

// The summary leaves out what the session quota leaves out, buckets UTC hours
// whatever the database time zone, picks up rows the activity log writes late,
// and its reads attribute each session to its account and device.
func TestAPIUsageSummaryDB(t *testing.T) {
	f := newAPIUsageFixture(t)
	ctx := context.Background()
	store := NewStore(f.pool)
	const item, catalog, home = "/api/v2/catalog/items/{id}", "/api/v2/catalog", "/api/v2/home/sections"
	f.log(t, f.busy, f.lastHour, "/api/v2/catalog/items/series-1", item, 200, 5)
	f.log(t, f.busy, f.currentHour, "/api/v2/catalog/items/series-1", item, 200, 1)
	f.log(t, f.busy, f.currentHour, catalog, catalog, 429, 1)
	f.log(t, f.busy, f.currentHour, "/api/v2/stream/s1", "/api/v2/stream/{session_id}", 206, 3) // media: not counted
	f.log(t, f.busy, f.currentHour, "/api/v2/playback/p1/progress", "/api/v2/playback/{id}/progress", 204, 2)
	f.insert(t, f.busy, f.currentHour, catalog, catalog, 200, 2, &f.adminID) // impersonating: not counted
	f.log(t, f.admin, f.currentHour, catalog, catalog, 200, 3)               // admin: not counted
	f.log(t, f.quiet, f.currentHour, home, home, 200, 1)
	f.log(t, f.revoked, f.currentHour, home, home, 200, 1)
	f.log(t, "", f.currentHour, "/api/v2/system/info", "/api/v2/system/info", 200, 2) // no session: not counted
	f.log(t, f.busy, f.now.Add(-72*time.Hour), catalog, catalog, 200, 4)              // before the catch-up window

	if _, _, ran, err := store.Refresh(ctx, f.now); err != nil || !ran {
		t.Fatalf("refresh: ran %v, %v", ran, err)
	}
	// A later request in the current hour lands on the next pass, and so does
	// a row the activity log wrote late for the hour before.
	f.log(t, f.quiet, f.now.Add(-5*time.Minute), home, home, 200, 1)
	f.log(t, f.quiet, f.lastHour, home, home, 200, 1)
	if _, _, ran, err := store.Refresh(ctx, f.now); err != nil || !ran {
		t.Fatalf("refresh: ran %v, %v", ran, err)
	}

	got, err := store.TopClients(ctx, f.now, 24, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalRequests != 11 || len(got.Clients) != 3 {
		t.Fatalf("total = %d over %d clients, want 11 over 3: %+v", got.TotalRequests, len(got.Clients), got.Clients)
	}
	want := Client{SessionID: f.busy, UserID: f.userID, Username: "apiusage-" + f.tag, DeviceName: "Test TV",
		UserAgent: "agent-" + f.tag, Requests: 7, Refused: 1, PeakHour: 5,
		FirstHour: f.lastHour.Truncate(time.Hour), LastHour: f.now.Truncate(time.Hour), SessionActive: true}
	top := got.Clients[0]
	top.FirstHour, top.LastHour = top.FirstHour.UTC(), top.LastHour.UTC()
	if top != want {
		t.Fatalf("busiest client = %+v, want %+v", top, want)
	}
	summary := map[string]string{}
	for _, c := range got.Clients[1:] {
		summary[c.SessionID] = fmt.Sprintf("%d requests, active %v", c.Requests, c.SessionActive)
	}
	if summary[f.quiet] != "3 requests, active true" || summary[f.revoked] != "1 requests, active false" {
		t.Fatalf("other clients = %v", summary)
	}

	// A one-hour window also reads the hour it starts in, so it is not empty
	// while the current hour is still being summarized.
	if hour, err := store.TopClients(ctx, f.now, 1, 10); err != nil || len(hour.Clients) == 0 || hour.Clients[0].Requests != 7 {
		t.Fatalf("one-hour window: %+v, %v; want the busy session's 7 requests", hour, err)
	}

	endpoints, err := store.ClientEndpoints(ctx, f.now, f.busy, 24)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Endpoint{{"GET", item, 6, 0}, {"GET", catalog, 1, 1}}; fmt.Sprint(endpoints) != fmt.Sprint(want) {
		t.Fatalf("endpoints = %+v, want %+v", endpoints, want)
	}
	if _, err := store.ClientEndpoints(ctx, f.now, "unknown-"+f.tag, 24); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("endpoints of an unknown session: err = %v, want ErrSessionNotFound", err)
	}

	// The lock and the pass share one connection, so a one-connection pool
	// finishes; while another server holds the lock, the pass is skipped.
	single := f.pool.Config()
	single.MaxConns = 1
	singlePool, err := pgxpool.NewWithConfig(ctx, single)
	if err != nil {
		t.Fatal(err)
	}
	defer singlePool.Close()
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, _, ran, err := NewStore(singlePool).Refresh(bounded, f.now); err != nil || !ran {
		t.Fatalf("refresh on a one-connection pool: ran %v, %v", ran, err)
	}
	holder, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, refreshLock); err != nil {
		t.Fatal(err)
	}
	_, _, ran, err := store.Refresh(ctx, f.now)
	if _, unlockErr := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, refreshLock); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	holder.Release()
	if err != nil || ran {
		t.Fatalf("refresh while another server holds the lock: ran %v, %v; want skipped", ran, err)
	}

	if _, _, _, err := store.Refresh(ctx, f.now.Add(Retention+2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after, err := store.TopClients(ctx, f.now, 24, 10); err != nil || after.TotalRequests != 0 {
		t.Fatalf("after pruning past retention: %+v, %v", after, err)
	}
}

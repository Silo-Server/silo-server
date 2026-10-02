package notifications

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func inboxPageDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "notification_inbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.WithoutCancel(t.Context()), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 4
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err = p.Exec(t.Context(), `CREATE TABLE notification_deliveries (LIKE public.notification_deliveries INCLUDING ALL); CREATE TABLE notification_preferences (LIKE public.notification_preferences INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	// LIKE does not copy triggers. Apply the actual migration in this schema so
	// concurrency tests exercise the production ordering boundary.
	migration, err := os.ReadFile("../../migrations/sql/20260906014006_serialize_notification_inbox_order.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := tx.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestNotificationInboxCutoffReplayAndTies(t *testing.T) {
	p := inboxPageDB(t)
	r := NewDeliveryRepository(p)
	ctx := t.Context()
	profile := uuid.NewString()
	other := uuid.NewString()
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(id, profile string, at time.Time) {
		t.Helper()
		_, err := p.Exec(ctx, `INSERT INTO notification_deliveries(id,user_id,profile_id,type,reason_flags,status,created_at) VALUES($1,1,$2,'request.approved','{}','pending',$3)`, id, profile, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	low := "00000000-0000-0000-0000-000000000001"
	high := "00000000-0000-0000-0000-000000000002"
	fresh := "00000000-0000-0000-0000-000000000003"
	insert(low, profile, stamp)
	insert(high, profile, stamp)
	insert(uuid.NewString(), other, stamp)
	cutoff, err := r.InboxCutoff(ctx, profile)
	if err != nil || cutoff.ID != high {
		t.Fatalf("%+v %v", cutoff, err)
	}
	first, more, err := r.ListInboxWindow(ctx, profile, false, 1, nil, cutoff)
	if err != nil || len(first) != 1 || !more || first[0].ID != high {
		t.Fatalf("%+v %v %v", first, more, err)
	}
	insert(fresh, profile, stamp.Add(time.Microsecond))
	second, more, err := r.ListInboxWindow(ctx, profile, false, 1, &Cursor{CreatedAt: first[0].CreatedAt, ID: first[0].ID}, cutoff)
	if err != nil || len(second) != 1 || more || second[0].ID != low {
		t.Fatalf("%+v %v %v", second, more, err)
	}
	for n := range 2 {
		changed, err := r.MarkReadThrough(ctx, profile, cutoff)
		if err != nil || changed != int64(2*(1-n)) {
			t.Fatalf("changed=%d err=%v", changed, err)
		}
	}
	for _, check := range []struct {
		profile string
		want    int
	}{{profile, 1}, {other, 1}} {
		count, err := r.UnreadCount(ctx, check.profile)
		if err != nil || count != check.want {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
	empty, err := r.InboxCutoff(ctx, uuid.NewString())
	if err != nil || empty.ID != "" {
		t.Fatal(empty, err)
	}
	if changed, err := r.MarkReadThrough(ctx, profile, empty); err != nil || changed != 0 {
		t.Fatal(changed, err)
	}
}
func TestNotificationPreferencePatchConcurrentFields(t *testing.T) {
	p := inboxPageDB(t)
	r := NewPreferencesRepository(p)
	profile := uuid.NewString()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, patch := range []PreferencePatch{{Enabled: new(false)}, {NotifyFavorites: new(false)}} {
		wg.Go(func() { _, err := r.Patch(t.Context(), profile, patch); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	value, err := r.Get(t.Context(), profile)
	if err != nil || value.Enabled || value.NotifyFavorites || !value.NotifyWatchlist || !value.NotifyNextUp {
		t.Fatalf("%+v %v", value, err)
	}
}

func TestNotificationInboxEarlierTransactionCommitsAfterCheckpoint(t *testing.T) {
	p := inboxPageDB(t)
	r := NewDeliveryRepository(p)
	ctx := t.Context()
	profile := uuid.NewString()
	early, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = early.Rollback(context.WithoutCancel(ctx)) }()
	// Establish the older transaction timestamp without inserting yet.
	var started time.Time
	if err := early.QueryRow(ctx, `SELECT now()`).Scan(&started); err != nil {
		t.Fatal(err)
	}
	later, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = later.Rollback(context.WithoutCancel(ctx)) }()
	b, err := r.BulkInsert(ctx, later, []Delivery{{ID: uuid.NewString(), UserID: 1, ProfileID: profile, Type: "request.approved", ReasonFlags: []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := later.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	cutoff, err := r.InboxCutoff(ctx, profile)
	if err != nil || cutoff.ID != b[0].ID {
		t.Fatal(cutoff, err)
	}
	a, err := r.BulkInsert(ctx, early, []Delivery{{ID: uuid.NewString(), UserID: 1, ProfileID: profile, Type: "request.approved", ReasonFlags: []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := early.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !a[0].CreatedAt.After(cutoff.CreatedAt) {
		t.Fatalf("late commit %v behind cutoff %v (transaction began %v)", a[0].CreatedAt, cutoff.CreatedAt, started)
	}
	rows, err := r.ListSync(ctx, profile, &cutoff, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != a[0].ID {
		t.Fatalf("sync omitted late commit: %+v %v", rows, err)
	}
	window, more, err := r.ListInboxWindow(ctx, profile, false, 10, nil, cutoff)
	if err != nil || more || len(window) != 1 || window[0].ID != b[0].ID {
		t.Fatalf("snapshot expanded: %+v %v %v", window, more, err)
	}
	for _, want := range []int64{1, 0} {
		changed, err := r.MarkReadThrough(ctx, profile, cutoff)
		if err != nil || changed != want {
			t.Fatal(changed, err)
		}
	}
	count, err := r.UnreadCount(ctx, profile)
	if err != nil || count != 1 {
		t.Fatalf("late commit marked read: %d %v", count, err)
	}
}

func TestNotificationInboxWriterWaitsForCommit(t *testing.T) {
	p := inboxPageDB(t)
	r := NewDeliveryRepository(p)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	profile := uuid.NewString()
	first, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(context.WithoutCancel(ctx)) }()
	a, err := r.BulkInsert(ctx, first, []Delivery{{ID: uuid.NewString(), UserID: 1, ProfileID: profile, Type: "request.approved", ReasonFlags: []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(context.WithoutCancel(ctx)) }()
	pid := second.Conn().PgConn().PID()
	done := make(chan error, 1)
	go func() {
		_, err := r.BulkInsert(ctx, second, []Delivery{{ID: uuid.NewString(), UserID: 1, ProfileID: profile, Type: "request.approved", ReasonFlags: []byte(`{}`)}})
		if err == nil {
			err = second.Commit(ctx)
		}
		done <- err
	}()
	// Observe the database barrier, rather than assuming a scheduling delay.
	for {
		var blocked bool
		if err := p.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("second writer bypassed uncommitted first: %v", err)
		default:
			runtime.Gosched()
		}
	}
	cutoff, err := r.InboxCutoff(ctx, profile)
	if err != nil || cutoff.ID != "" {
		t.Fatal(cutoff, err)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListSync(ctx, profile, nil, 10)
	if err != nil || len(rows) != 2 || rows[0].ID != a[0].ID || !rows[1].CreatedAt.After(rows[0].CreatedAt) {
		t.Fatalf("commit ordering: %+v %v", rows, err)
	}
	if changed, err := r.MarkReadThrough(ctx, profile, cutoff); err != nil || changed != 0 {
		t.Fatal(changed, err)
	}
}

func TestNotificationInboxClockSurvivesRetentionAndBackwardClock(t *testing.T) {
	p := inboxPageDB(t)
	r := NewDeliveryRepository(p)
	ctx := t.Context()
	profile := uuid.NewString()
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if _, err := p.Exec(ctx, `INSERT INTO notification_inbox_clocks(profile_id,last_created_at) VALUES($1,$2)`, profile, future); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		tx, err := p.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := r.BulkInsert(ctx, tx, []Delivery{{ID: uuid.NewString(), UserID: 1, ProfileID: profile, Type: "request.approved", ReasonFlags: []byte(`{}`)}})
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if !rows[0].CreatedAt.After(future) {
			t.Fatalf("timestamp regressed: %v <= %v", rows[0].CreatedAt, future)
		}
		future = rows[0].CreatedAt
		if err := r.DeleteAllForProfile(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}
}

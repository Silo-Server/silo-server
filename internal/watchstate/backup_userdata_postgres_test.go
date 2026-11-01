package watchstate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// sameItems maps every content id to itself.
type sameItems struct{}

func (sameItems) Resolve(_ context.Context, id string) (string, bool, error) { return id, true, nil }

func backupTestProfile(t *testing.T) (*pgxpool.Pool, userdata.Owner) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "wsbk-" + hex.EncodeToString(b)
	var userID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, name).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, 'Main', true)`, name, userID); err != nil {
		t.Fatal(err)
	}
	return pool, userdata.Owner{UserID: userID, ProfileID: name}
}

func importWatch(t *testing.T, pool *pgxpool.Pool, owner userdata.Owner, doc watchHistoryDocument) userdata.Result {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	res, err := BackupData{}.Import(t.Context(), tx, userdata.Target{Owner: owner, Items: sameItems{}, Now: time.Now()}, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return res
}

func at(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

var negInfinity = pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.NegativeInfinity}

// TestHistoryHideMergeDB: a history hide's two cutoffs merge independently,
// as marking watched and unwatched raises them. An equal hidden_before does
// not stop a later removed_before, a legacy NULL removed_before counts as
// its hidden_before, -infinity ("nothing removed") round-trips, and a merge
// that advances neither cutoff changes nothing.
func TestHistoryHideMergeDB(t *testing.T) {
	pool, owner := backupTestProfile(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seed := func(item string, hidden time.Time, removed any) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, removed_before, updated_at)
			VALUES ($1, $2, $3, $4, $5, $4)`, owner.UserID, owner.ProfileID, item, hidden, removed); err != nil {
			t.Fatal(err)
		}
	}
	read := func(item string) (hidden, removed pgtype.Timestamptz) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT hidden_before, removed_before FROM user_history_hidden_items WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
			owner.UserID, owner.ProfileID, item).Scan(&hidden, &removed); err != nil {
			t.Fatal(err)
		}
		return hidden, removed
	}
	seed("equal", base, negInfinity)
	seed("legacy", base, nil)
	seed("legacy-later", base, nil)
	res := importWatch(t, pool, owner, watchHistoryDocument{Hidden: []watchHiddenEntry{
		{Item: "equal", HiddenBefore: at(base), RemovedBefore: at(base.Add(time.Hour)), UpdatedAt: base},
		{Item: "legacy", HiddenBefore: at(base.Add(-time.Hour)), RemovedBefore: at(base.Add(-time.Hour)), UpdatedAt: base},
		{Item: "legacy-later", HiddenBefore: at(base.Add(-time.Hour)), RemovedBefore: at(base.Add(2 * time.Hour)), UpdatedAt: base},
		{Item: "new-unmarked", HiddenBefore: at(base), RemovedBefore: negInfinity, UpdatedAt: base},
	}})
	if res.Updated != 2 || res.Unchanged != 1 || res.Added != 1 {
		t.Fatalf("merge = %+v, want 2 updated, 1 unchanged, 1 added", res)
	}
	if hidden, removed := read("equal"); !hidden.Time.Equal(base) || !removed.Time.Equal(base.Add(time.Hour)) {
		t.Fatalf("equal hidden_before blocked the later removed_before: %v %v", hidden, removed)
	}
	if hidden, removed := read("legacy"); !hidden.Time.Equal(base) || removed.Valid {
		t.Fatalf("an older hide changed a legacy row: %v %v", hidden, removed)
	}
	if hidden, removed := read("legacy-later"); !hidden.Time.Equal(base) || !removed.Time.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("a later removed_before over a legacy NULL: %v %v", hidden, removed)
	}
	if _, removed := read("new-unmarked"); removed.InfinityModifier != pgtype.NegativeInfinity {
		t.Fatalf("-infinity did not round-trip: %v", removed)
	}
	if again := importWatch(t, pool, owner, watchHistoryDocument{Hidden: []watchHiddenEntry{
		{Item: "equal", HiddenBefore: at(base), RemovedBefore: at(base.Add(time.Hour)), UpdatedAt: base},
		{Item: "new-unmarked", HiddenBefore: at(base), RemovedBefore: negInfinity, UpdatedAt: base},
	}}); again.Changed() {
		t.Fatalf("merging the same hides again = %+v", again)
	}
}

// TestProgressMergeKeepsHeartbeatDB: a playback heartbeat that commits while
// a restore merges the same item's progress is newer and is kept; the
// restore's older row does not overwrite it.
func TestProgressMergeKeepsHeartbeatDB(t *testing.T) {
	pool, owner := backupTestProfile(t)
	ctx := t.Context()
	item := "hb-" + owner.ProfileID
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at) VALUES ($1, $2, $3, 10, 100, $4)`,
		owner.UserID, owner.ProfileID, item, old); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = heartbeat.Rollback(ctx) }()
	if _, err := heartbeat.Exec(ctx, `UPDATE user_watch_progress SET position_seconds = 90, updated_at = $4 WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		owner.UserID, owner.ProfileID, item, old.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	restore, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restore.Rollback(ctx) }()
	var pid int
	if err := restore.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(watchHistoryDocument{Progress: []watchProgressEntry{{Item: item, Position: 50, Duration: 100, UpdatedAt: old.Add(time.Hour)}}})
	done := make(chan userdata.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := BackupData{}.Import(ctx, restore, userdata.Target{Owner: owner, Items: sameItems{}, Now: time.Now()}, data)
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()
	// Wait until the restore's merge waits on the heartbeat's row lock.
	for deadline := time.Now().Add(10 * time.Second); ; {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the restore never waited on the heartbeat")
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_locks, the observable state, between checks
	}
	if err := heartbeat.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	case res := <-done:
		if res.Changed() || res.Unchanged != 1 {
			t.Fatalf("merge over a newer heartbeat = %+v, want unchanged", res)
		}
	}
	if err := restore.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var position float64
	if err := pool.QueryRow(ctx, `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		owner.UserID, owner.ProfileID, item).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 90 {
		t.Fatalf("position = %v, want the heartbeat's 90", position)
	}
}

// TestWatchSourcesValidate: a play's source is one of the fixed sources or
// a watch-sync plugin's, plugin:<installation id>:<capability id>; anything
// else makes the document invalid.
func TestWatchSourcesValidate(t *testing.T) {
	play := func(source string) json.RawMessage {
		data, _ := json.Marshal(watchHistoryDocument{Progress: []watchProgressEntry{}, Hidden: []watchHiddenEntry{},
			History: []watchPlayEntry{{Item: "movie-1", WatchedAt: time.Unix(1700000000, 0).UTC(), Source: source}}})
		return data
	}
	for source, ok := range map[string]bool{
		"playback": true, "plugin:2147483647:a.b-c_d": true,
		"somewhere": false, "plugin:01:x": false, "plugin:14:MDBList": false, "plugin:2147483648:x": false,
		"plugin:14:" + strings.Repeat("a", 129): false,
	} {
		if _, err := (BackupData{}).Validate(play(source)); (err == nil) != ok {
			t.Errorf("source %q: valid %v, want %v", source, err == nil, ok)
		}
	}
}

// TestPluginSourcedPlaysDB: plays a watch-sync plugin imported carry their
// source. On the server that wrote them it stays; on another it names the
// installation here that is the same plugin with the capability, or becomes
// an import when there is none. A source the reader would refuse is
// exported as an import. A play is the same play whatever its source, so
// importing again adds nothing.
func TestPluginSourcedPlaysDB(t *testing.T) {
	pool, source := backupTestProfile(t)
	ctx := t.Context()
	item := "wsbk-item-" + source.ProfileID
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, item); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, item) })
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for i, s := range []string{"plugin:14:mdblist", "somewhere-new"} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed, source)
			VALUES (gen_random_uuid()::text, $1, $2, $3, $4, true, $5)`, source.UserID, source.ProfileID, item, when.Add(time.Duration(i)*time.Hour), s); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := BackupData{}.Export(ctx, tx, source)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (BackupData{}).Validate(exported.Data); err != nil {
		t.Fatalf("the export does not read back: %v", err)
	}
	importInto := func(target userdata.Target) userdata.Result {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		target.Items, target.Now = sameItems{}, time.Now()
		res, err := BackupData{}.Import(ctx, tx, target, exported.Data)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return res
	}
	sources := func(owner userdata.Owner) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT source FROM user_watch_history WHERE user_id = $1 AND profile_id = $2 ORDER BY watched_at`, owner.UserID, owner.ProfileID)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	mapped := func(installation int, capability string) (int, bool) {
		return 77, installation == 14 && capability == "mdblist"
	}
	for _, c := range []struct {
		name   string
		target func(userdata.Owner) userdata.Target
		want   []string
	}{
		{"same server", func(o userdata.Owner) userdata.Target { return userdata.Target{Owner: o, SameServer: true} }, []string{"plugin:14:mdblist", "import"}},
		{"another server, installation mapped", func(o userdata.Owner) userdata.Target {
			return userdata.Target{Owner: o, PluginInstallation: mapped}
		}, []string{"plugin:77:mdblist", "import"}},
		{"another server, no installation", func(o userdata.Owner) userdata.Target { return userdata.Target{Owner: o} }, []string{"import", "import"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, owner := backupTestProfile(t)
			if res := importInto(c.target(owner)); res.Added != 2 {
				t.Fatalf("first import = %+v", res)
			}
			if got := sources(owner); !slices.Equal(got, c.want) {
				t.Fatalf("sources = %v, want %v", got, c.want)
			}
			if res := importInto(c.target(owner)); res.Added != 0 || res.Unchanged != 2 {
				t.Fatalf("importing again = %+v, want nothing added", res)
			}
		})
	}
}

package watchlist

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

type sameItems struct{}

func (sameItems) Resolve(_ context.Context, id string) (string, bool, error) { return id, true, nil }

// TestPrepareCreatesAndLocksTitlesDB: before an import writes any watchlist
// row, Prepare holds every title it names across all profiles, creating the
// ones this server lacks. Another connection creating one of them meanwhile
// waits for the import, never the other way round, so the import writes
// both profiles without waiting on a title.
func TestPrepareCreatesAndLocksTitlesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "wlbk-"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user) })
	p1, p2 := "p1-"+sfx, "p2-"+sfx
	for i, p := range []string{p1, p2} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, $1, $3)`, p, user, i == 0); err != nil {
			t.Fatal(err)
		}
	}
	base := 700000000 + int(time.Now().UnixNano()%1000000)
	x, y := base, base+1
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE tmdb_id = ANY($1)`, []int{x, y})
	})
	docFor := func(tmdb int) json.RawMessage {
		data, _ := json.Marshal(favoritesDocument{Favorites: []keyedEntry{}, Watchlist: []keyedEntry{},
			Titles: []watchedTitle{{MediaType: mediaTypeMovie, TMDBID: tmdb, Title: "Soon", AddedAt: time.Now()}}})
		return data
	}
	docs := []userdata.Prepared{{Owner: userdata.Owner{UserID: user, ProfileID: p1}, Data: docFor(x)}, {Owner: userdata.Owner{UserID: user, ProfileID: p2}, Data: docFor(y)}}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := (BackupData{}).Prepare(ctx, tx, docs); err != nil {
		t.Fatal(err)
	}
	// Another writer adds the second profile's title now: it must wait for
	// the import, which already holds it.
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, `SET LOCAL lock_timeout = '1s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := AddSnapshotInTx(ctx, other, user, p1, Snapshot{MediaType: mediaTypeMovie, TMDBID: y, Title: "Soon"}, time.Now(), time.Now()); err == nil {
		_ = other.Rollback(context.Background())
		t.Fatal("another writer created a title the import had not taken first")
	}
	_ = other.Rollback(context.Background())
	for _, d := range docs {
		if _, err := (BackupData{}).Import(ctx, tx, userdata.Target{Owner: d.Owner, Items: sameItems{}, Now: time.Now()}, d.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var titles, entries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watchlist_titles WHERE tmdb_id = ANY($1)`, []int{x, y}).Scan(&titles); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_watchlist_titles WHERE user_id = $1`, user).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if titles != 2 || entries != 2 {
		t.Fatalf("titles %d entries %d, want 2 and 2", titles, entries)
	}

	checkUntrustedImport(t, pool, userdata.Owner{UserID: user, ProfileID: p2}, x, y, base+2)
}

// checkUntrustedImport: a profile's own export is untrusted. A title the
// profile already lists (own) is unchanged; one another profile lists (x)
// and one no one does (z) are both left out the same way, so the outcome
// says nothing about other accounts' watchlists. It writes none of its
// snapshot to a shared title and creates none.
func checkUntrustedImport(t *testing.T, pool *pgxpool.Pool, owner userdata.Owner, x, own, z int) {
	t.Helper()
	ctx := t.Context()
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE tmdb_id = $1`, z) })
	forged, _ := json.Marshal(favoritesDocument{Favorites: []keyedEntry{}, Watchlist: []keyedEntry{}, Titles: []watchedTitle{
		{MediaType: mediaTypeMovie, TMDBID: x, Title: "Forged", Certification: "G", AddedAt: time.Now()},
		{MediaType: mediaTypeMovie, TMDBID: own, Title: "Forged too", AddedAt: time.Now()},
		{MediaType: mediaTypeMovie, TMDBID: z, Title: "Invented", AddedAt: time.Now()},
	}})
	untrusted, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = untrusted.Rollback(context.Background()) }()
	if err := (BackupData{}).Prepare(ctx, untrusted, []userdata.Prepared{{Owner: owner, Data: forged, Untrusted: true}}); err != nil {
		t.Fatal(err)
	}
	res, err := (BackupData{}).Import(ctx, untrusted, userdata.Target{Owner: owner, Items: sameItems{}, Now: time.Now(), Untrusted: true}, forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusted.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var title, certification string
	if err := pool.QueryRow(ctx, `SELECT title, certification FROM watchlist_titles WHERE tmdb_id = $1`, x).Scan(&title, &certification); err != nil {
		t.Fatal(err)
	}
	var invented int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watchlist_titles WHERE tmdb_id = $1`, z).Scan(&invented); err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Unchanged != 1 || res.LeftOut != 2 || title != "Soon" || certification != "" || invented != 0 {
		t.Fatalf("untrusted import = %+v, title %q %q, invented titles %d", res, title, certification, invented)
	}
}

// TestExportLeavesOutTitlesAboveTheCeilingDB: a profile's own export keeps
// only the watchlisted titles outside the library its watchlist shows under
// its rating ceiling, a title without a certification failing closed, while
// an administrator backup (the zero Keep) keeps them all.
func TestExportLeavesOutTitlesAboveTheCeilingDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "wlcap-"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user) })
	profile := "p-" + sfx
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, $1, true)`, profile, user); err != nil {
		t.Fatal(err)
	}
	base := 710000000 + int(time.Now().UnixNano()%1000000)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE tmdb_id = ANY($1)`, []int{base, base + 1, base + 2})
	})
	for i, cert := range []string{"PG", "R", ""} {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO watchlist_titles (id, media_type, tmdb_id, title, certification) VALUES ($1::bigint, 'movie', $1::int, $2, $3) RETURNING id`,
			base+i, "Title "+cert, cert).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_watchlist_titles (user_id, profile_id, title_id) VALUES ($1, $2, $3)`, user, profile, id); err != nil {
			t.Fatal(err)
		}
	}
	exported := func(keep userdata.Keep) []string {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		exp, err := BackupData{}.Export(ctx, tx, userdata.Owner{UserID: user, ProfileID: profile}, keep)
		if err != nil {
			t.Fatal(err)
		}
		var doc favoritesDocument
		if err := json.Unmarshal(exp.Data, &doc); err != nil {
			t.Fatal(err)
		}
		var certs []string
		for _, title := range doc.Titles {
			certs = append(certs, title.Certification)
		}
		return certs
	}
	if got := exported(userdata.Keep{MaxContentRating: "PG-13"}); len(got) != 1 || got[0] != "PG" {
		t.Fatalf("profile export under PG-13 = %q, want only the PG title", got)
	}
	if got := exported(userdata.Keep{}); len(got) != 3 {
		t.Fatalf("administrator backup = %q, want every title", got)
	}
}

package activitylog

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIPPagesFixedWindowAndTies(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	_, err = p.Exec(t.Context(), `CREATE TEMP TABLE activity_log(timestamp timestamptz,client_ip inet,user_id int); CREATE TEMP TABLE users(id int,username text); INSERT INTO users VALUES(1,'one'),(2,'two')`)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	last := until.Add(-time.Hour)
	_, err = p.Exec(t.Context(), `INSERT INTO activity_log VALUES($1,'192.0.2.1',1),($1,'192.0.2.2',1),($1,'192.0.2.3',1),($1,'192.0.2.3',2),($2,'192.0.2.3',1),($3,'192.0.2.4',1)`, last, last.Add(-time.Hour), until.Add(-31*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	repo := &Repo{pool: p}
	pos := IPPagePosition{Until: until}
	seen := map[string]bool{}
	for range 3 {
		rows, more, err := repo.UserIPsPage(t.Context(), 1, 30, 1, pos)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		r := rows[0]
		if seen[r.ClientIP] {
			t.Fatal("duplicate IP")
		}
		if strings.Contains(r.ClientIP, "/") {
			t.Fatal("IP included a network prefix", r.ClientIP)
		}
		seen[r.ClientIP] = true
		if r.ClientIP == "192.0.2.3" && r.RequestCount != 2 {
			t.Fatal(r)
		}
		pos.LastSeen = r.LastSeen
		pos.IP = r.ClientIP
		// A new event for an unseen group must not move it across the cursor.
		if len(seen) == 1 {
			if _, err = p.Exec(t.Context(), `INSERT INTO activity_log VALUES($1,'192.0.2.1',1)`, until.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if more != (len(seen) < 3) {
			t.Fatalf("more=%v seen=%v", more, seen)
		}
	}
	users, more, err := repo.IPUsersPage(t.Context(), "192.0.2.3", 30, 1, IPPagePosition{Until: until})
	if err != nil || !more || len(users) != 1 || users[0].UserID != 2 {
		t.Fatalf("%+v %v %v", users, more, err)
	}
	users, more, err = repo.IPUsersPage(t.Context(), "192.0.2.3", 30, 1, IPPagePosition{Until: until, LastSeen: users[0].LastSeen, UserID: 2})
	if err != nil || more || len(users) != 1 || users[0].UserID != 1 || users[0].RequestCount != 2 {
		t.Fatalf("%+v %v %v", users, more, err)
	}
}

package auth

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestUserWithoutEmailOrUsernameDB: an account may have only one of a
// username and an email, and no password hash (the columns are nullable).
// Reading one returns the missing values empty instead of failing, in the account reads and the
// admin API key listings.
func TestUserWithoutEmailOrUsernameDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var ids []int
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = ANY($1)`, ids) })
	insert := func(username, email *string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, role, enabled) VALUES ($1, $2, 'user', true) RETURNING id`,
			username, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		return id
	}
	name, mail := "no-email-"+suffix, "no-username-"+suffix+"@example.invalid"
	noEmail, noUsername := insert(&name, nil), insert(nil, &mail)

	users := NewUserRepository(pool)
	for id, want := range map[int][2]string{noEmail: {name, ""}, noUsername: {"", mail}} {
		u, err := users.GetByID(ctx, id)
		if err != nil || u.Username != want[0] || u.Email != want[1] {
			t.Fatalf("GetByID(%d) = %+v, %v; want username %q, email %q", id, u, err, want[0], want[1])
		}
	}
	all, err := users.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := 0
	for _, u := range all {
		if slices.Contains(ids, u.ID) {
			listed++
		}
	}
	if listed != 2 {
		t.Fatalf("List returned %d of the two accounts", listed)
	}

	// An email-only account with an API key keeps the admin key listings
	// readable.
	keys := NewAPIKeyRepository(pool)
	key, err := keys.Create(ctx, noUsername, "email-only "+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	listedKey := func(list []*models.APIKeyMetadataWithUser) bool {
		for _, k := range list {
			if k.ID == key.ID {
				return k.Username == ""
			}
		}
		return false
	}
	all2, err := keys.ListAll(ctx)
	if err != nil || !listedKey(all2) {
		t.Fatalf("ListAll with an email-only account's key = %v (listed %v)", err, listedKey(all2))
	}
}

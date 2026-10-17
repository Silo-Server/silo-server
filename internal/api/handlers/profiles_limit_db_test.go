package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// profileLimitDBFixture is one account on a real Postgres user store with a
// primary profile, served by a ProfileHandler wired as production wires it.
type profileLimitDBFixture struct {
	pool    *pgxpool.Pool
	handler *ProfileHandler
	store   userstore.UserStore
	userID  int
	primary string
}

func newProfileLimitDBFixture(t *testing.T, maxProfiles int) *profileLimitDBFixture {
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

	name := fmt.Sprintf("profile-limit-%d", time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, role, max_profiles)
		 VALUES ($1, $2, 'x', 'user', $3) RETURNING id`,
		name, name+"@example.invalid", maxProfiles,
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewProfileHandler(provider)
	handler.UserRepo = auth.NewUserRepository(pool)

	f := &profileLimitDBFixture{pool: pool, handler: handler, store: store, userID: userID}
	if code, body := f.create("Parent"); code != http.StatusCreated {
		t.Fatalf("bootstrap primary profile: status = %d, body = %s", code, body)
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil || len(profiles) != 1 || !profiles[0].IsPrimary {
		t.Fatalf("bootstrap profiles = %+v, err = %v", profiles, err)
	}
	f.primary = profiles[0].ID
	return f
}

// create posts one profile through the v1 HTTP handler, acting as the
// primary profile once it exists.
func (f *profileLimitDBFixture) create(name string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/profiles", strings.NewReader(fmt.Sprintf(`{"name":%q}`, name)))
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID: f.userID, Role: "user", TokenType: auth.TokenTypeAccess,
	})
	if f.primary != "" {
		ctx = apimw.SetProfileID(ctx, f.primary)
	}
	rr := httptest.NewRecorder()
	f.handler.HandleCreateProfile(rr, req.WithContext(ctx))
	return rr.Code, rr.Body.String()
}

func (f *profileLimitDBFixture) profileCount(t *testing.T) int {
	t.Helper()
	profiles, err := f.store.ListProfiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return len(profiles)
}

func assertProfileLimitReached(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", code, body)
	}
	var resp errorResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if resp.Error != "profile_limit_reached" {
		t.Fatalf("error = %q, want profile_limit_reached", resp.Error)
	}
}

// TestCreateProfileLimitDB pins the household profile limit on the real
// Postgres store: an account under its limit creates profiles, and one at
// its limit is refused with 409 profile_limit_reached.
func TestCreateProfileLimitDB(t *testing.T) {
	f := newProfileLimitDBFixture(t, 3)

	for _, name := range []string{"Kid One", "Kid Two"} {
		if code, body := f.create(name); code != http.StatusCreated {
			t.Fatalf("create %s under the limit: status = %d, body = %s", name, code, body)
		}
	}
	code, body := f.create("Kid Three")
	assertProfileLimitReached(t, code, body)
	if got := f.profileCount(t); got != 3 {
		t.Fatalf("profiles = %d, want 3", got)
	}
}

// TestCreateProfileLimitConcurrentDB is issue #2423: concurrent creates on an
// account one below its limit must not all pass a count taken before the
// write. Exactly one commits; every other request gets the documented 409.
// Each request runs on its own pool connection, as it would from separate API
// replicas, so only a database-level serialization can keep the limit.
func TestCreateProfileLimitConcurrentDB(t *testing.T) {
	const limit, attempts = 3, 12
	f := newProfileLimitDBFixture(t, limit)
	if code, body := f.create("Kid One"); code != http.StatusCreated {
		t.Fatalf("create to limit-1: status = %d, body = %s", code, body)
	}

	type result struct {
		code int
		body string
	}
	results := make([]result, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			<-start
			code, body := f.create(fmt.Sprintf("Racer %d", i))
			results[i] = result{code, body}
		})
	}
	close(start)
	wg.Wait()

	created := 0
	for _, r := range results {
		if r.code == http.StatusCreated {
			created++
			continue
		}
		assertProfileLimitReached(t, r.code, r.body)
	}
	if created != 1 {
		t.Fatalf("created = %d of %d concurrent requests, want exactly 1", created, attempts)
	}
	if got := f.profileCount(t); got != limit {
		t.Fatalf("profiles = %d, want the limit %d", got, limit)
	}
}

package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auth"
)

type resetDB struct {
	repo *Repository
	pool *pgxpool.Pool
}

// newResetDB copies the tables a reset touches into a throwaway schema.
func newResetDB(t *testing.T) resetDB {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("password_reset_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+q+" CASCADE"); admin.Close() })
	for _, table := range []string{"users", "auth_sessions", "password_reset_tokens", "abs_sessions", "device_login_requests"} {
		if _, err = admin.Exec(ctx, "CREATE TABLE "+q+"."+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+q+".users ALTER COLUMN id DROP IDENTITY IF EXISTS; CREATE SEQUENCE "+q+".user_fixture_seq; ALTER TABLE "+q+".users ALTER COLUMN id SET DEFAULT nextval('"+q+".user_fixture_seq')"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return resetDB{NewRepository(pool), pool}
}

// account inserts an account holding password "old-password" with the given
// state, and one login session it owns.
func (d resetDB) account(t *testing.T, name string, enabled, localLogin bool) int {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	err = d.pool.QueryRow(t.Context(), `INSERT INTO users(username, email, password_hash, role, enabled, local_password_login_enabled, password_change_required)
		VALUES ($1, $2, $3, 'user', $4, $5, true) RETURNING id`, name, name+"@example.invalid", string(hash), enabled, localLogin).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(t.Context(), `INSERT INTO auth_sessions(id, user_id, device_name, expires_at) VALUES ($1, $2, 'test', now() + interval '1 day')`, name+"-session", id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (d resetDB) issue(t *testing.T, userID int, token string, expiresAt time.Time) {
	t.Helper()
	if err := d.repo.Issue(t.Context(), userID, auth.HashLinkToken(token), nil, expiresAt); err != nil {
		t.Fatal(err)
	}
}

func TestResetLinkCompletesOnceAndSignsOutEverywhere(t *testing.T) {
	d := newResetDB(t)
	ctx := t.Context()
	id := d.account(t, "reset", true, true)
	d.issue(t, id, "tok", time.Now().Add(time.Hour))

	link, err := d.repo.Lookup(ctx, auth.HashLinkToken("tok"))
	if err != nil || link.UserID != id || link.Username != "reset" {
		t.Fatalf("lookup = %+v, %v", link, err)
	}
	// Credentials a login minted without the password: an Audiobookshelf
	// session and a device sign-in approved but not yet collected.
	if _, err := d.pool.Exec(ctx, `INSERT INTO abs_sessions(user_id, token_hash, device_id) VALUES ($1, 'abs-token', 'abs-device')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `INSERT INTO device_login_requests(id, device_code_hash, browser_code_hash, user_code_hash, match_code, device_name, status, approved_by_user_id, expires_at)
		VALUES (gen_random_uuid(), 'dev', 'browser', 'user', 'MATCH', 'tv', 'approved', $1, now() + interval '10 minutes')`, id); err != nil {
		t.Fatal(err)
	}
	user, err := d.repo.Complete(ctx, auth.HashLinkToken("tok"), "new-password")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(user, "new-password") || user.PasswordChangeRequired {
		t.Fatalf("password not reset or still temporary: %+v", user)
	}
	var live, liveABS, approved int
	err = d.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL),
		(SELECT count(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL),
		(SELECT count(*) FROM device_login_requests WHERE approved_by_user_id = $1 AND status = 'approved')`, id).Scan(&live, &liveABS, &approved)
	if err != nil || live != 0 || liveABS != 0 || approved != 0 {
		t.Fatalf("survived the reset: %d sessions, %d Audiobookshelf sessions, %d approved device sign-ins (%v)", live, liveABS, approved, err)
	}
	if _, err := d.repo.Complete(ctx, auth.HashLinkToken("tok"), "another-password"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second use: %v", err)
	}
	if _, err := d.repo.Lookup(ctx, auth.HashLinkToken("tok")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("spent link still resolves: %v", err)
	}
}

func TestResetLinkRetiresWithoutBeingUsed(t *testing.T) {
	d := newResetDB(t)
	ctx := t.Context()
	usable := func(token string) bool {
		_, err := d.repo.Lookup(ctx, auth.HashLinkToken(token))
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}

	id := d.account(t, "replaced", true, true)
	d.issue(t, id, "first", time.Now().Add(time.Hour))
	d.issue(t, id, "second", time.Now().Add(time.Hour))
	if usable("first") || !usable("second") {
		t.Fatal("a newer link must cancel the older one")
	}

	// Any other password change outdates the link.
	if _, err := d.pool.Exec(ctx, `UPDATE users SET password_hash = 'changed' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if usable("second") {
		t.Fatal("link survived a password change")
	}
	if _, err := d.repo.Complete(ctx, auth.HashLinkToken("second"), "new-password"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outdated link completed: %v", err)
	}

	expired := d.account(t, "expired", true, true)
	d.issue(t, expired, "late", time.Now().Add(-time.Minute))
	if usable("late") {
		t.Fatal("expired link resolves")
	}

	disabled := d.account(t, "disabled-later", true, true)
	d.issue(t, disabled, "frozen", time.Now().Add(time.Hour))
	if _, err := d.pool.Exec(ctx, `UPDATE users SET enabled = false WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	if usable("frozen") {
		t.Fatal("link of a disabled account resolves")
	}
}

func TestResetLinkRefusesIneligibleAccounts(t *testing.T) {
	d := newResetDB(t)
	for name, id := range map[string]int{
		"disabled": d.account(t, "disabled", false, true),
		"external": d.account(t, "external", true, false),
	} {
		if err := d.repo.Issue(t.Context(), id, auth.HashLinkToken(name), nil, time.Now().Add(time.Hour)); !errors.Is(err, ErrNotEligible) {
			t.Fatalf("%s: issue = %v", name, err)
		}
	}
	if err := d.repo.Issue(t.Context(), 999999, auth.HashLinkToken("ghost"), nil, time.Now().Add(time.Hour)); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("missing account: issue = %v", err)
	}
}

func TestResetLinkConcurrentCompletionsHaveOneWinner(t *testing.T) {
	d := newResetDB(t)
	id := d.account(t, "race", true, true)
	d.issue(t, id, "race", time.Now().Add(time.Hour))

	const racers = 4
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for i := range racers {
		wg.Go(func() {
			_, err := d.repo.Complete(t.Context(), auth.HashLinkToken("race"), fmt.Sprintf("password-%d", i))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrNotFound):
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d completions succeeded, want exactly 1", won)
	}
}

func TestIssueUnlessRecentHoldsTheCooldownDB(t *testing.T) {
	d := newResetDB(t)
	ctx := t.Context()
	alice := d.account(t, "alice", true, true)
	hashOf := func() string {
		t.Helper()
		var hash string
		if err := d.pool.QueryRow(ctx, `SELECT token_hash FROM password_reset_tokens WHERE user_id = $1`, alice).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	expires := time.Now().Add(time.Hour)

	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "first", expires, time.Minute); err != nil || !stored {
		t.Fatalf("first request: %v, %v", stored, err)
	}
	// Inside the cooldown the live link survives, whoever asks.
	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "second", expires, time.Minute); err != nil || stored || hashOf() != "first" {
		t.Fatalf("request inside cooldown: %v, %v, link %q", stored, err, hashOf())
	}
	if _, err := d.pool.Exec(ctx, `UPDATE password_reset_tokens SET created_at = now() - interval '2 minutes' WHERE user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	var issuedBy *int
	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "third", expires, time.Minute); err != nil || !stored || hashOf() != "third" {
		t.Fatalf("request after cooldown: %v, %v, link %q", stored, err, hashOf())
	}
	// A requested link records the account as its own issuer.
	if err := d.pool.QueryRow(ctx, `SELECT issued_by FROM password_reset_tokens WHERE user_id = $1`, alice).Scan(&issuedBy); err != nil || issuedBy == nil || *issuedBy != alice {
		t.Fatalf("requested link has issuer %v, %v", issuedBy, err)
	}
	// An administrator's link is not subject to the cooldown.
	admin := d.account(t, "admin", true, true)
	if err := d.repo.Issue(ctx, alice, "admin-sent", &admin, expires); err != nil || hashOf() != "admin-sent" {
		t.Fatalf("admin issue inside cooldown: %v, link %q", err, hashOf())
	}
	// A request never retires a live admin link, however old.
	if _, err := d.pool.Exec(ctx, `UPDATE password_reset_tokens SET created_at = now() - interval '2 hours' WHERE user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "over-admin", expires, time.Minute); err != nil || stored || hashOf() != "admin-sent" {
		t.Fatalf("request replaced a live admin link: %v, %v, link %q", stored, err, hashOf())
	}
	// Nor one whose issuing administrator was deleted (issued_by set NULL).
	if _, err := d.pool.Exec(ctx, `UPDATE password_reset_tokens SET issued_by = NULL WHERE user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "over-orphan", expires, time.Minute); err != nil || stored || hashOf() != "admin-sent" {
		t.Fatalf("request replaced an admin link whose issuer was deleted: %v, %v, link %q", stored, err, hashOf())
	}
	// Once that link is dead, expired or outdated by a password change, it can.
	if _, err := d.pool.Exec(ctx, `UPDATE password_reset_tokens SET password_fingerprint = 'outdated' WHERE user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "after-outdated", expires, time.Minute); err != nil || !stored || hashOf() != "after-outdated" {
		t.Fatalf("request after an outdated admin link: %v, %v, link %q", stored, err, hashOf())
	}
	if err := d.repo.Issue(ctx, alice, "admin-again", &admin, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE password_reset_tokens SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute' WHERE user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if stored, err := d.repo.IssueUnlessRecent(ctx, alice, "after-expired", expires, time.Minute); err != nil || !stored || hashOf() != "after-expired" {
		t.Fatalf("request after an expired admin link: %v, %v, link %q", stored, err, hashOf())
	}
	// Withdrawing takes only the named link.
	if err := d.repo.Withdraw(ctx, alice, "someone-else"); err != nil || hashOf() != "after-expired" {
		t.Fatalf("withdraw of another digest: %v, link %q", err, hashOf())
	}
	if err := d.repo.Withdraw(ctx, alice, "after-expired"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, alice).Scan(&left); err != nil || left != 0 {
		t.Fatalf("withdrawn link still stored: %d, %v", left, err)
	}

	for name, id := range map[string]int{
		"disabled":          d.account(t, "off", false, true),
		"external provider": d.account(t, "sso", true, false),
		"unknown":           999999,
	} {
		if stored, err := d.repo.IssueUnlessRecent(ctx, id, "never-"+name, expires, time.Minute); err != nil || stored {
			t.Errorf("%s: stored=%v err=%v", name, stored, err)
		}
	}
}

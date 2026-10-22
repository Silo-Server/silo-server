package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// TestPasswordResetAuditDB runs against a migrated database: issuing a link
// and completing it record domain rows without the link, its digest or the
// new password, and a refused completion records nothing.
func TestPasswordResetAuditDB(t *testing.T) {
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
	repo := NewRepository(pool)
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var ids []int
	emails := map[int]string{}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM password_reset_tokens WHERE user_id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = ANY($1)`, ids)
	})
	account := func(role string, enabled bool) int {
		t.Helper()
		name := fmt.Sprintf("reset-audit-%s-%d", suffix, len(ids))
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO users(username, email, password_hash, role, enabled) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			name, name+"@example.invalid", string(hash), role, enabled).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		emails[id] = name + "@example.invalid"
		return id
	}
	admin, user, disabled := account("admin", true), account("user", true), account("user", false)
	userID := strconv.Itoa(user)
	issue := func(target int) context.Context {
		path := "/api/v2/admin/users/" + strconv.Itoa(target) + "/password-reset"
		ctx, _ := audittest.Context(t, pool, audittest.Request{Method: http.MethodPost, Pattern: "/api/v2/admin/users/{id}/password-reset", Path: path, UserID: &admin})
		return ctx
	}

	token := "reset-audit-token-" + suffix
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := repo.Issue(issue(disabled), disabled, emails[disabled], auth.HashLinkToken("other-"+token), &admin, expires, DeliveryLink); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("ineligible account: %v", err)
	}
	if rows := audittest.Rows(t, pool, auditmutation.ActionPasswordResetLinkCreated, auditmutation.TargetUser, strconv.Itoa(disabled)); len(rows) != 0 {
		t.Fatalf("refused link recorded: %+v", rows)
	}
	if err := repo.Issue(issue(user), user, emails[user], auth.HashLinkToken(token), &admin, expires, DeliveryEmail); err != nil {
		t.Fatal(err)
	}
	created := audittest.One(t, pool, auditmutation.ActionPasswordResetLinkCreated, auditmutation.TargetUser, userID)
	if *created.UserID != admin || created.StatusCode != http.StatusCreated ||
		audittest.Value(created.Change(t, "delivery").After) != `"email"` ||
		audittest.Value(created.Change(t, "expires_at").After) != `"`+expires.Format(time.RFC3339)+`"` {
		t.Fatalf("link row: %s", created.Raw)
	}
	created.AssertNoSecrets(t, token, auth.HashLinkToken(token))

	complete := func(token string) context.Context {
		ctx, _ := audittest.Context(t, pool, audittest.Request{Method: http.MethodPost, Pattern: "/api/v2/password-resets/{token}/complete",
			Path: "/api/v2/password-resets/" + token + "/complete", OptIn: true})
		return ctx
	}
	if _, err := repo.Complete(complete("wrong-"+token), auth.HashLinkToken("wrong-"+token), "margin fossil quench"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown link: %v", err)
	}
	if rows := audittest.Rows(t, pool, auditmutation.ActionPasswordResetCompleted, auditmutation.TargetUser, userID); len(rows) != 0 {
		t.Fatalf("refused completion recorded: %+v", rows)
	}
	if _, err := repo.Complete(complete(token), auth.HashLinkToken(token), "margin fossil quench"); err != nil {
		t.Fatal(err)
	}
	done := audittest.One(t, pool, auditmutation.ActionPasswordResetCompleted, auditmutation.TargetUser, userID)
	var newHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, user).Scan(&newHash); err != nil {
		t.Fatal(err)
	}
	done.AssertNoSecrets(t, token, auth.HashLinkToken(token), "margin fossil quench", newHash)
	if done.Path != "/api/v2/password-resets/[redacted]/complete" || done.UserID != nil || done.StatusCode != http.StatusOK ||
		len(done.Changes) != 1 || done.Changes[0].Field != "password" || done.Changes[0].Before != nil || done.Changes[0].After != nil {
		t.Fatalf("completion row: %s", done.Raw)
	}
}
